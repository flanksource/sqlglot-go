package sqlglot

import (
	"fmt"
	"strings"
)

const formatIndent = "  "

var clauseTokens = map[TokenType]bool{
	TokSELECT: true, TokFROM: true, TokWHERE: true, TokGROUP_BY: true, TokORDER_BY: true,
	TokHAVING: true, TokUNION: true, TokEXCEPT: true, TokINTERSECT: true, TokVALUES: true,
	TokSET: true, TokUPDATE: true, TokINSERT: true, TokDELETE: true, TokWITH: true,
}

var joinTokens = map[TokenType]bool{
	TokLEFT: true, TokRIGHT: true, TokINNER: true, TokFULL: true, TokCROSS: true,
	TokNATURAL: true, TokOUTER: true, TokJOIN: true,
}

// listClauses are the clauses whose comma-separated items go one per line.
var listClauses = map[TokenType]bool{TokSELECT: true, TokSET: true}

// formatFrame is one parenthesised level. A subquery frame indents and has
// clauses of its own; a plain one (a call, an IN list, a grouping) keeps
// everything inside it on one line.
type formatFrame struct {
	subquery bool
	clause   TokenType
}

// Format lays a statement out one clause per line, with subqueries indented.
//
// It works on tokens rather than on a parse, so a fragment the parser refuses
// still formats. It only ever moves whitespace: every token keeps the text it
// was written with, keyword case included, and comments stay where they stood.
// A statement the tokenizer cannot lex is an error.
func Format(sql, dialect string) (string, error) {
	return formatSQL(sql, dialect, false)
}

// FormatScript keeps existing line boundaries while formatting clauses.
func FormatScript(sql, dialect string) (string, error) {
	return formatSQL(sql, dialect, true)
}

func formatSQL(sql, dialect string, preserveLineBreaks bool) (string, error) {
	tk, err := NewTokenizer(dialect)
	if err != nil {
		return "", fmt.Errorf("format: %w", err)
	}
	if preserveLineBreaks && dialect == "tsql" {
		tk.noCommandPayload = true
	}
	toks, err := tk.Tokenize(sql)
	if err != nil {
		return "", fmt.Errorf("format: %w", err)
	}
	src := []rune(sql)
	f := &formatter{frames: []formatFrame{{subquery: true}}, preserveLineBreaks: preserveLineBreaks}
	prevEnd := -1
	for i, tok := range toks {
		var next TokenType
		if i+1 < len(toks) {
			next = toks[i+1].Type
		}
		f.token(tok, next, string(src[prevEnd+1:tok.Start]), tokenText(string(src[tok.Start:tok.End+1])))
		prevEnd = tok.End
	}
	if trailing := strings.TrimSpace(string(src[prevEnd+1:])); trailing != "" {
		f.separate(trailing, false, 0)
	}
	return f.out.String(), nil
}

type formatter struct {
	out                strings.Builder
	frames             []formatFrame
	prev               TokenType
	caseDepth          int
	afterBetween       bool
	breakPending       bool
	preserveLineBreaks bool
}

func (f *formatter) top() *formatFrame { return &f.frames[len(f.frames)-1] }

func (f *formatter) depth() int {
	depth := -1
	for _, frame := range f.frames {
		if frame.subquery {
			depth++
		}
	}
	return depth
}

func (f *formatter) token(tok Token, next TokenType, gap, text string) {
	atClause := f.top().subquery
	depth := f.depth()
	breakAt := -1
	switch {
	case tok.Type == TokR_PAREN && atClause && len(f.frames) > 1:
		breakAt = depth - 1
	case !atClause:
	case clauseTokens[tok.Type]:
		deleteFrom := tok.Type == TokFROM && f.prev == TokDELETE
		tableHint := tok.Type == TokWITH && next == TokL_PAREN
		if !deleteFrom && !tableHint {
			breakAt = depth
		}
	case joinTokens[tok.Type] && !joinTokens[f.prev] && next != TokL_PAREN:
		breakAt = depth
	case tok.Type == TokON && (!f.preserveLineBreaks || f.top().clause != TokSET):
		breakAt = depth + 1
	case (tok.Type == TokAND || tok.Type == TokOR) && f.caseDepth == 0 && !f.afterBetween &&
		(!f.preserveLineBreaks || tok.Type != TokOR || f.prev != TokCREATE):
		breakAt = depth + 1
	case f.prev == TokCOMMA && listClauses[f.top().clause] && f.caseDepth == 0:
		breakAt = depth + 1
	}
	if f.breakPending && breakAt < 0 {
		breakAt = depth
	}
	if f.preserveLineBreaks && breakAt < 0 && strings.ContainsAny(gap, "\r\n") {
		breakAt = depth
	}
	f.breakPending = false
	f.separate(strings.TrimSpace(gap), gap != "", breakAt)
	f.out.WriteString(text)
	f.advance(tok.Type, next)
	if f.preserveLineBreaks && tok.Type == TokCOMMAND && strings.EqualFold(tok.Text, "GO") {
		f.frames = []formatFrame{{subquery: true}}
		f.caseDepth, f.afterBetween, f.breakPending = 0, false, false
	}
}

// separate writes what stands between the previous token and the next one: a
// comment if there is one, then a line break at the given indent level when
// breakAt is not negative, a space when spaced, and nothing otherwise.
func (f *formatter) separate(comment string, spaced bool, breakAt int) {
	if comment != "" {
		if f.out.Len() > 0 {
			f.out.WriteByte(' ')
		}
		f.out.WriteString(comment)
		if endsInLineComment(comment) && breakAt < 0 {
			breakAt = f.depth()
		}
	}
	switch {
	case f.out.Len() == 0:
	case breakAt >= 0:
		f.out.WriteByte('\n')
		f.out.WriteString(strings.Repeat(formatIndent, breakAt))
	case spaced:
		f.out.WriteByte(' ')
	}
}

func (f *formatter) advance(typ, next TokenType) {
	switch {
	case typ == TokL_PAREN:
		f.frames = append(f.frames, formatFrame{subquery: next == TokSELECT || next == TokWITH})
	case typ == TokR_PAREN && len(f.frames) > 1:
		f.frames = f.frames[:len(f.frames)-1]
	case typ == TokCASE:
		f.caseDepth++
	case typ == TokEND && f.caseDepth > 0:
		f.caseDepth--
	case typ == TokBETWEEN:
		f.afterBetween = true
	case typ == TokAND && f.afterBetween:
		f.afterBetween = false
	case typ == TokSEMICOLON:
		f.frames = []formatFrame{{subquery: true}}
		f.caseDepth, f.afterBetween, f.breakPending = 0, false, true
	case clauseTokens[typ] && f.top().subquery:
		f.top().clause = typ
	}
	f.prev = typ
}

// tokenText is a token as written, with the whitespace inside a multi-word
// keyword (GROUP  BY) collapsed. Quoted text keeps its whitespace.
func tokenText(text string) string {
	if strings.ContainsAny(text, "'\"[`") {
		return text
	}
	return strings.Join(strings.Fields(text), " ")
}

func endsInLineComment(comment string) bool {
	last := comment[strings.LastIndex(comment, "\n")+1:]
	if end := strings.LastIndex(last, "*/"); end >= 0 {
		last = last[end+2:]
	}
	return strings.Contains(last, "--")
}
