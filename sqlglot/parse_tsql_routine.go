package sqlglot

import "strings"

// T-SQL routine bodies: the statements of a CREATE PROCEDURE or CREATE
// FUNCTION, as sys.sql_modules stores them.
//
// The reference reads a body one statement per semicolon and gives up -- or
// silently misreads -- most of what a real routine holds: a statement with no
// semicolon after it, an IF whose branch is a single statement (it takes the
// statement's first word for the condition's ALIAS), RETURN and THROW, a
// scalar function with more than one statement. Each of those is read here
// into a real node rather than a Command, and where the reference does read a
// form -- `IF <cond> BEGIN ... END`, `AS BEGIN ...; END` -- the tree is the
// reference's own: a Block of statements whose last is an EndStatement. The
// divergences are listed in docs/upstream-issues.md.

// parseTSQLStatements reads statements until the END that closes their block
// (closed) or the end of the batch (not closed). The END is kept as the
// reference keeps it, as an EndStatement at the end of the block.
func (p *parser) parseTSQLStatements(closed bool) (*Expression, error) {
	was := p.inRoutineBody
	p.inRoutineBody = true
	defer func() { p.inRoutineBody = was }()
	var statements []*Expression
	for {
		for p.match(TokSEMICOLON) {
		}
		switch {
		case p.curr() == nil && closed:
			return nil, p.unsupported("BEGIN without END")
		case p.curr() == nil:
			if len(statements) == 0 {
				return nil, p.unsupported("a routine body with no statements")
			}
			return New("Block", Arg{"expressions", statements}), nil
		case p.at(TokEND) && !closed:
			return nil, p.unsupported("END without BEGIN")
		case p.at(TokEND):
			if len(statements) == 0 {
				return nil, p.unsupported("BEGIN END with no statements")
			}
			p.advance()
			statements = append(statements, New("EndStatement"))
			return New("Block", Arg{"expressions", statements}), nil
		}
		run, err := p.parseTSQLRun()
		if err != nil {
			return nil, err
		}
		statements = append(statements, run...)
	}
}

// parseTSQLRun reads the statements up to the next separator: a semicolon, or
// the END of the enclosing block. Several may stand there with nothing between
// them -- the semicolon is optional in T-SQL -- and each reader stops where
// the next statement begins.
func (p *parser) parseTSQLRun() ([]*Expression, error) {
	whole := p.tokens
	p.tokens = whole[:p.endOfTSQLStatement()]
	defer func() { p.tokens = whole }()
	var run []*Expression
	for p.curr() != nil {
		statement, err := p.parseTSQLStatement()
		if err != nil {
			return nil, err
		}
		run = append(run, statement)
	}
	return run, nil
}

// endOfTSQLStatement finds where the statements at the cursor end: the next
// semicolon or END outside parentheses and outside any BEGIN … END or CASE …
// END they open themselves. A semicolon followed by ELSE ends an IF's first
// branch, not the IF, so the scan goes on past it.
func (p *parser) endOfTSQLStatement() int {
	parens, blocks := 0, 0
	for i := p.index; i < len(p.tokens); i++ {
		switch p.tokens[i].Type {
		case TokL_PAREN:
			parens++
		case TokR_PAREN:
			parens--
		case TokCASE:
			blocks++
		case TokBEGIN:
			if p.opensBlockAt(i) {
				blocks++
			}
		case TokEND:
			if blocks == 0 && parens == 0 {
				return i
			}
			blocks--
		case TokSEMICOLON:
			if blocks == 0 && parens == 0 && !p.tokenIsWordAt(i+1, "ELSE") {
				return i
			}
		}
	}
	return len(p.tokens)
}

// atReservedStatementWord reports a word T-SQL reserves that begins a
// statement in a routine body, and so is never an implicit alias there:
// `SELECT a FROM t RETURN` is a query and a RETURN. (THROW is not reserved --
// T-SQL itself needs a `;` before it -- so it is left to be an alias.)
func (p *parser) atReservedStatementWord() bool {
	return p.inRoutineBody && (p.atWords("RETURN") || p.atWords("IF") || p.atWords("PRINT") || p.atWords("WHILE") ||
		p.atWords("OPEN") || p.atWords("CLOSE") || p.atWords("DEALLOCATE") || p.atWords("FETCH"))
}

// opensABlock reports whether any BEGIN in the tokens opens a BEGIN … END
// block.
func opensABlock(tokens []Token) bool {
	p := &parser{tokens: tokens}
	for i, token := range tokens {
		if token.Type == TokBEGIN && p.opensBlockAt(i) {
			return true
		}
	}
	return false
}

// opensBlockAt reports whether the BEGIN at i opens a BEGIN … END block rather
// than a transaction: `BEGIN TRAN[SACTION]` and `BEGIN DISTRIBUTED
// TRANSACTION` have no END.
func (p *parser) opensBlockAt(i int) bool {
	for _, word := range []string{"TRAN", "TRANSACTION", "DISTRIBUTED"} {
		if p.tokenIsWordAt(i+1, word) {
			return false
		}
	}
	return true
}

// tokenIsWordAt reports whether the token at i spells the unquoted word.
func (p *parser) tokenIsWordAt(i int, word string) bool {
	if i >= len(p.tokens) || p.tokens[i].Type == TokIDENTIFIER {
		return false
	}
	return strings.EqualFold(p.tokens[i].Text, word)
}

// parseTSQLStatement reads one statement of a routine body.
//
// A body holds statements, never a bare expression: a word the port does not
// know as a statement is refused rather than read as a column, and so is a
// statement the port would only keep as raw text (a Command). A routine may
// not create another routine -- that takes a new batch, after GO.
func (p *parser) parseTSQLStatement() (*Expression, error) {
	start := p.index
	statement, err := p.parseTSQLStatementKind()
	if err != nil {
		return nil, err
	}
	if p.index == start {
		return nil, p.unsupported("a statement that reads nothing")
	}
	if statement.Class == "Command" {
		token := p.tokens[start]
		return nil, &UnsupportedError{Construct: "a statement kept only as text in a routine body",
			Start: token.Start, End: token.End, HasRange: true}
	}
	return statement, nil
}

func (p *parser) parseTSQLStatementKind() (*Expression, error) {
	c := p.curr()
	if c.Type == TokBEGIN && p.atWords("BEGIN", "TRY") {
		return p.parseTSQLTryCatch()
	}
	if p.atWords("FETCH") {
		return p.parseTSQLFetchCursor()
	}
	switch {
	case c.Type == TokVAR || c.Type == TokIDENTIFIER || c.Type == TokPARAMETER ||
		c.Type == TokSTRING || c.Type == TokNUMBER:
		if p.atWords("RETURN") {
			return p.parseTSQLReturn()
		}
		if p.atWords("THROW") {
			return p.parseTSQLThrow()
		}
		if p.atWords("PRINT") {
			return p.parseTSQLPrint()
		}
		if p.atWords("IF") {
			return p.parseTSQLIf()
		}
		if p.atWords("WHILE") {
			return p.parseTSQLWhile()
		}
		if p.atWords("OPEN") || p.atWords("CLOSE") || p.atWords("DEALLOCATE") {
			return p.parseTSQLCursorAction()
		}
		return nil, p.unsupported("a statement this port does not read in a routine body")
	case c.Type == TokBEGIN && p.opensBlockAt(p.index):
		p.advance()
		block, err := p.parseTSQLStatements(true)
		if err != nil {
			return nil, err
		}
		block.Set("begin", true)
		return block, nil
	case c.Type == TokCREATE && p.createsARoutine():
		return nil, p.unsupported("a routine inside a routine body (batches are separated by GO)")
	case c.Type == TokELSE:
		return nil, p.unsupported("ELSE without IF")
	}
	return p.parseStatement()
}

func (p *parser) parseTSQLTryCatch() (*Expression, error) {
	p.advance() // BEGIN
	p.advance() // TRY
	tryBody, err := p.parseTSQLTrySection("TRY")
	if err != nil {
		return nil, err
	}
	if !p.atWords("BEGIN", "CATCH") {
		return nil, p.unsupported("END TRY without BEGIN CATCH")
	}
	p.advance()
	p.advance()
	catchBody, err := p.parseTSQLTrySection("CATCH")
	if err != nil {
		return nil, err
	}
	return New("TryCatch", Arg{"try", tryBody}, Arg{"catch", catchBody}), nil
}

func (p *parser) parseTSQLTrySection(closing string) (*Expression, error) {
	was := p.inRoutineBody
	p.inRoutineBody = true
	defer func() { p.inRoutineBody = was }()
	var statements []*Expression
	for {
		for p.match(TokSEMICOLON) {
		}
		if p.curr() == nil || p.at(TokEND) {
			if !p.atWords("END", closing) || len(statements) == 0 {
				return nil, p.unsupported("BEGIN " + closing + " without a matching nonempty END " + closing)
			}
			p.advance()
			p.advance()
			return New("Block", Arg{"expressions", statements}), nil
		}
		run, err := p.parseTSQLRun()
		if err != nil {
			return nil, err
		}
		statements = append(statements, run...)
	}
}

func (p *parser) parseTSQLPrint() (*Expression, error) {
	keyword := p.curr()
	p.advance()
	if p.atStatementEnd() || p.at(TokEND) {
		return nil, &UnsupportedError{Construct: "PRINT without an expression",
			Start: keyword.Start, End: keyword.End, HasRange: true}
	}
	value, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	return New("Print", Arg{"this", value}), nil
}

// createsARoutine reports whether the CREATE here makes a module of its own:
// a procedure, function, view or trigger, each of which must open a batch.
func (p *parser) createsARoutine() bool {
	for i := p.index + 1; i < len(p.tokens) && i <= p.index+3; i++ {
		for _, word := range []string{"PROCEDURE", "PROC", "FUNCTION", "VIEW", "TRIGGER"} {
			if p.tokenIsWordAt(i, word) {
				return true
			}
		}
	}
	return false
}

// parseTSQLIf reads `IF <condition> <branch> [ELSE <branch>]`, where a branch
// is a BEGIN … END block or a single statement.
//
// A block branch is the reference's tree: a Block ending in an EndStatement.
// A single-statement branch is the statement itself, which the reference
// cannot read -- it takes the statement's first word for the condition's
// alias. The one form it reads that way on purpose, `IF <cond>
// EXEC('<sql>')`, is in its corpus and is still read its way.
func (p *parser) parseTSQLIf() (*Expression, error) {
	mark := p.index
	p.advance() // IF
	condition, err := p.parseTSQLCondition()
	if err != nil {
		return nil, err
	}
	if p.at(TokEXECUTE) && p.next() != nil && p.next().Type == TokL_PAREN {
		p.index = mark
		return p.parseIfStatement()
	}
	branch, err := p.parseTSQLBranch()
	if err != nil {
		return nil, err
	}
	if p.at(TokSEMICOLON) && p.tokenIsWordAt(p.index+1, "ELSE") {
		p.advance()
	}
	var otherwise any = false
	if p.match(TokELSE) {
		if otherwise, err = p.parseTSQLBranch(); err != nil {
			return nil, err
		}
	}
	return New("IfBlock", Arg{"this", condition}, Arg{"true", branch}, Arg{"false", otherwise}), nil
}

func (p *parser) parseTSQLWhile() (*Expression, error) {
	p.advance()
	condition, err := p.parseTSQLCondition()
	if err != nil {
		return nil, err
	}
	if p.curr() == nil || p.atAny(TokSEMICOLON, TokEND, TokELSE) {
		return nil, p.unsupported("WHILE without a statement to run")
	}
	body, err := p.parseTSQLBranch()
	if err != nil {
		return nil, err
	}
	return New("While", Arg{"this", condition}, Arg{"expression", body}), nil
}

// parseTSQLCondition reads an IF's condition. Parentheses round the WHOLE of
// it belong to the syntax and leave no Paren in the tree, as the reference
// reads them.
func (p *parser) parseTSQLCondition() (*Expression, error) {
	condition, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	if condition.Class == "Paren" {
		return condition.This(), nil
	}
	return condition, nil
}

// parseTSQLBranch reads what an IF or ELSE runs.
func (p *parser) parseTSQLBranch() (*Expression, error) {
	if p.curr() == nil || p.atAny(TokSEMICOLON, TokEND, TokELSE) {
		return nil, p.unsupported("IF without a statement to run")
	}
	if p.at(TokBEGIN) && p.opensBlockAt(p.index) {
		p.advance()
		return p.parseTSQLStatements(true)
	}
	was := p.inRoutineBody
	p.inRoutineBody = true
	defer func() { p.inRoutineBody = was }()
	return p.parseTSQLStatement()
}

// parseTSQLReturn reads `RETURN [<value>]`, which leaves a procedure (with a
// status) or a scalar function (with its result).
func (p *parser) parseTSQLReturn() (*Expression, error) {
	p.advance() // RETURN
	if p.atStatementEnd() || p.at(TokEND) {
		return New("Return"), nil
	}
	value, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	return New("Return", Arg{"this", value}), nil
}

// parseTSQLThrow reads `THROW <number>, <message>, <state>`, or a bare THROW
// that re-raises the error a CATCH block is handling. The reference has no
// node for it; Throw is the port's own.
func (p *parser) parseTSQLThrow() (*Expression, error) {
	p.advance() // THROW
	if p.atStatementEnd() || p.at(TokEND) {
		return New("Throw"), nil
	}
	var parts [3]*Expression
	for i := range parts {
		if i > 0 && !p.match(TokCOMMA) {
			return nil, p.unsupported("THROW without its number, message and state")
		}
		part, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		parts[i] = part
	}
	return New("Throw", Arg{"this", parts[0]}, Arg{"message", parts[1]}, Arg{"state", parts[2]}), nil
}
