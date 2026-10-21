package sqlglot

import (
	"errors"
	"fmt"
	"strings"
)

type ParseDiagnostic struct {
	Kind           string `json:"kind"`
	Label          string `json:"label"`
	StatementKind  string `json:"statement_kind"`
	Start          int    `json:"start"`
	End            int    `json:"end"`
	StatementStart int    `json:"statement_start"`
}

type ScriptResult struct {
	Tree        *Expression       `json:"-"`
	Diagnostics []ParseDiagnostic `json:"diagnostics"`
	Complete    bool              `json:"complete"`
}

func ParseScript(sql, dialect string) (ScriptResult, error) {
	tk, err := NewTokenizer(dialect)
	if err != nil {
		return ScriptResult{}, err
	}
	if dialect == "tsql" {
		tk.noCommandPayload = true
	}
	tokens, err := tk.Tokenize(sql)
	if err != nil {
		return ScriptResult{}, err
	}
	if len(tokens) == 0 {
		return ScriptResult{}, fmt.Errorf("empty SQL input")
	}
	result := ScriptResult{Complete: true}
	var expressions []*Expression
	batchStart := 0
	for i := 0; i <= len(tokens); i++ {
		if i < len(tokens) && (dialect != "tsql" || !scriptBatchGO(tokens, i)) {
			continue
		}
		if batchStart < i {
			batch := &parser{tokens: tokens[batchStart:i], cfg: tk.Config(), tables: tk.Config().Tables, dialect: dialect, sql: sql, nativeTSQLModules: true}
			trees, diagnostics, complete := batch.parseScriptBatch()
			expressions = append(expressions, trees...)
			result.Diagnostics = append(result.Diagnostics, diagnostics...)
			result.Complete = result.Complete && complete
		}
		batchStart = i + 1
	}
	if len(expressions) == 0 && len(result.Diagnostics) == 0 {
		return ScriptResult{}, fmt.Errorf("empty SQL input")
	}
	if len(result.Diagnostics) == 0 {
		if len(expressions) == 1 {
			result.Tree = expressions[0]
		} else {
			result.Tree = New("Block", Arg{"expressions", expressions})
		}
	}
	return result, nil
}

func (p *parser) parseScriptBatch() ([]*Expression, []ParseDiagnostic, bool) {
	blockEnds := p.scriptBlockEnds()
	ignoredEnds := map[int]bool{}
	var expressions []*Expression
	var diagnostics []ParseDiagnostic
	for p.curr() != nil {
		p.inRoutineBody = false
		for closing := range ignoredEnds {
			if closing > p.index {
				p.inRoutineBody = true
				break
			}
		}
		if ignoredEnds[p.index] {
			p.advance()
			continue
		}
		if p.match(TokSEMICOLON) {
			continue
		}
		if p.atScriptGO() {
			p.advance()
			continue
		}
		start := p.index
		var tree *Expression
		var err error
		if p.dialect == "tsql" && ((p.at(TokBEGIN) && p.opensBlockAt(p.index)) || p.atWords("IF") || p.atWords("WHILE") ||
			p.atWords("RETURN") || p.atWords("THROW")) {
			tree, err = p.parseTSQLStatement()
		} else {
			tree, err = p.parseStatement()
		}
		if err == nil && tree != nil && tree.Class == "Command" {
			token := p.tokens[start]
			err = &UnsupportedError{Construct: "a statement kept only as text", Start: token.Start, End: token.End, HasRange: true}
		}
		if err == nil && p.index == start {
			err = p.unsupported("a statement that reads nothing")
		}
		if err == nil && p.curr() != nil && !p.at(TokSEMICOLON) && !p.atScriptGO() && !p.atNextStatement() {
			err = p.unsupported("trailing tokens")
		}
		if err == nil && p.dialect == "tsql" && len(expressions) > 0 &&
			(tsqlBatchOnlyRoutine(tree) || tsqlBatchOnlyRoutine(expressions[len(expressions)-1])) {
			token := p.tokens[start]
			err = &UnsupportedError{Construct: "a routine definition sharing a batch with another statement",
				Start: token.Start, End: token.End, HasRange: true}
		}
		if err == nil {
			expressions = append(expressions, tree)
			continue
		}
		diagnostics = append(diagnostics, p.scriptDiagnostic(start, err))
		for opening, closing := range blockEnds {
			if opening >= start && opening < p.index && closing >= p.index {
				ignoredEnds[closing] = true
			}
		}
		if p.tokens[start].Type == TokBEGIN && p.opensBlockAt(start) && blockEnds[start] == 0 {
			diagnostics = append(diagnostics, ParseDiagnostic{
				Kind: "scan_gap", Label: "BEGIN block has no matching END",
				Start: p.tokens[start].Start, End: p.tokens[len(p.tokens)-1].End,
				StatementStart: p.tokens[start].Start, StatementKind: scriptStatementKind(p.tokens[start]),
			})
			return expressions, diagnostics, false
		}
		p.recoverScript(start)
	}
	return expressions, diagnostics, true
}

func (p *parser) atScriptGO() bool {
	return p.dialect == "tsql" && p.curr() != nil && scriptBatchGO(p.tokens, p.index)
}

func scriptBatchGO(tokens []Token, index int) bool {
	token := tokens[index]
	return token.Type == TokCOMMAND && strings.EqualFold(token.Text, "GO") &&
		(index == 0 || tokens[index-1].Line < token.Line) &&
		(index+1 == len(tokens) || tokens[index+1].Line > token.Line)
}

func (p *parser) scriptDiagnostic(start int, err error) ParseDiagnostic {
	token := p.tokens[start]
	where := p.curr()
	if where == nil {
		where = &p.tokens[len(p.tokens)-1]
	}
	var unsupported *UnsupportedError
	errorAt := where.Start
	if errors.As(err, &unsupported) && unsupported.HasRange {
		errorAt = unsupported.Start
	}
	for i := start + 1; i <= p.index && i < len(p.tokens); i++ {
		if p.tokens[i].Start <= errorAt && scriptStatementStart(p.tokens[i]) && p.tokens[i].Line > p.tokens[i-1].Line {
			token = p.tokens[i]
		}
	}
	if unsupported != nil && unsupported.Construct == "a statement this port does not read in a routine body" && where.Start == errorAt {
		token = *where
	}
	if errors.As(err, &unsupported) && unsupported.HasRange {
		return ParseDiagnostic{Kind: "unsupported", Label: unsupported.Label(), StatementKind: scriptStatementKind(token),
			Start: unsupported.Start, End: unsupported.End, StatementStart: token.Start}
	}
	return ParseDiagnostic{Kind: "unsupported", Label: err.Error(), StatementKind: scriptStatementKind(token),
		Start: where.Start, End: where.End, StatementStart: token.Start}
}

func (p *parser) scriptBlockEnds() map[int]int {
	ends := map[int]int{}
	var stack []int
	for i, token := range p.tokens {
		switch token.Type {
		case TokCASE:
			stack = append(stack, -1)
		case TokBEGIN:
			if p.opensBlockAt(i) {
				stack = append(stack, i)
			}
		case TokEND:
			if len(stack) > 0 {
				opening := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if opening >= 0 {
					ends[opening] = i
				}
			}
		}
	}
	return ends
}

func scriptStatementKind(token Token) string {
	if token.Type == TokIDENTIFIER || token.Type == TokSTRING || token.Type == TokNUMBER {
		return "EXPRESSION"
	}
	return strings.ToUpper(token.Text)
}

func (p *parser) recoverScript(start int) {
	for i := max(start+1, p.index); i < len(p.tokens); i++ {
		token := p.tokens[i]
		if token.Type == TokSEMICOLON || scriptBatchGO(p.tokens, i) {
			p.index = i + 1
			return
		}
		if token.Line > p.tokens[i-1].Line && scriptStatementStart(token) {
			p.index = i
			return
		}
	}
	p.index = len(p.tokens)
}

func scriptStatementStart(token Token) bool {
	switch token.Type {
	case TokSELECT, TokINSERT, TokUPDATE, TokDELETE, TokMERGE, TokEXECUTE, TokDECLARE, TokSET,
		TokCREATE, TokDROP, TokALTER, TokBEGIN, TokEND, TokELSE, TokTRUNCATE, TokFETCH:
		return true
	}
	if token.Type == TokIDENTIFIER {
		return false
	}
	switch strings.ToUpper(token.Text) {
	case "IF", "WHILE", "PRINT", "OPEN", "CLOSE", "DEALLOCATE", "RETURN", "THROW":
		return true
	}
	return false
}
