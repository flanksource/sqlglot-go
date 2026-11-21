package access

import (
	"regexp"
	"slices"
	"strings"
)

// condition is the IF or WHILE a statement opens: its condition text, and
// whether a jump on its own line is all it runs.
type condition struct {
	text  string
	loop  bool
	jumps bool
}

// tsqlStatement is one statement of a T-SQL body: where it is written, what
// it does, the IF or WHILE it opens, or why it could not be read.
type tsqlStatement struct {
	line int
	text string
	read statementRead
	cond *condition
	err  error
	// defined is what the dynamic SQL the statement runs defines beyond its
	// statements: the routines and the diagnostics. It joins the file only
	// when the statement is the one read.
	defined File
}

// runStatements reads one run of a body as the statements it holds. T-SQL
// needs no semicolon between statements, so the run is cut at every line a
// statement may start with, and each statement is read as the shortest run
// of pieces that parses: an INSERT whose SELECT opens a line, or a WITH whose
// SELECT does, joins the piece after it. A piece that parses in no run is
// returned with its error, and reading resumes at the next piece.
func (b *tsqlBody) runStatements(start, end int) []tsqlStatement {
	starts := statementStarts(b.text, start, end)
	ends := append(slices.Clone(starts[1:]), end)
	var out []tsqlStatement
	for k := 0; k < len(starts); {
		m, read := b.shortestParsingRun(starts[k], ends[k:])
		if read.err == nil {
			b.f.file.Routines = append(b.f.file.Routines, read.defined.Routines...)
			b.f.file.Diagnostics = append(b.f.file.Diagnostics, read.defined.Diagnostics...)
		}
		out = append(out, read)
		k += m + 1
	}
	return out
}

// shortestParsingRun reads the text from start to the nearest of ends it
// parses to, returning which end that was; when it parses to none, the first
// piece with its error.
func (b *tsqlBody) shortestParsingRun(start int, ends []int) (int, tsqlStatement) {
	first := b.statementAccess(start, ends[0])
	if first.err == nil {
		return 0, first
	}
	for m := 1; m < len(ends); m++ {
		if read := b.statementAccess(start, ends[m]); read.err == nil {
			return m, read
		}
	}
	return 0, first
}

// statementAccess reads the statement [start, end). A data-free statement
// reads nothing; an IF or WHILE is read for its condition, a RETURN for what
// it returns, a PRINT, RAISERROR or THROW for its arguments, and a cursor's
// declaration for its query.
func (b *tsqlBody) statementAccess(start, end int) tsqlStatement {
	statement := strings.TrimRight(b.text[start:end], " \t\r\n;")
	out := tsqlStatement{line: b.firstLine + lineAt(b.text, start) - 1, text: statement}
	read := func(text string) {
		dynamic := &fileReader{src: b.f.src}
		ctx := statementContext{
			StatementOptions: StatementOptions{Dialect: "tsql", DefaultSchema: tsqlDefaultSchema, Line: out.line},
			dynamic:          func(sql string) ([]Node, error) { return dynamic.readDynamic(sql, out.line), nil },
		}
		out.read, out.err = readStatement(text, ctx)
		out.defined = dynamic.file
	}
	switch {
	case dataFreeStatement.MatchString(statement):
	case conditionStatement.MatchString(statement):
		keyword := conditionStatement.FindStringSubmatchIndex(statement)
		cond := &condition{text: strings.TrimSpace(statement[keyword[1]:]), loop: strings.EqualFold(statement[keyword[2]:keyword[3]], "WHILE")}
		if jump := conditionJump.FindStringIndex(cond.text); jump != nil {
			cond.text, cond.jumps = strings.TrimSpace(cond.text[:jump[0]]), true
		}
		out.cond = cond
		read("SELECT 1 WHERE " + cond.text)
	case returnStatement.MatchString(statement):
		if returned, ok := returnedQuery(statement[returnStatement.FindStringIndex(statement)[1]:]); ok {
			read(returned)
		}
	case messageStatement.MatchString(statement):
		m := messageStatement.FindStringSubmatch(statement)
		read("SELECT " + m[1] + m[2] + m[3])
	case cursorDeclaration.MatchString(statement):
		read(cursorDeclaration.FindStringSubmatch(statement)[1])
	default:
		read(statement)
	}
	return out
}

// conditionStatement is the IF or WHILE that opens a statement, and what
// follows it: the condition, read as the WHERE of a SELECT so a subquery in it
// is read like any other.
var conditionStatement = regexp.MustCompile(`(?is)\A\s*(IF|WHILE)\b`)

// sqlName is a T-SQL name, bare or bracketed, or a @variable.
const sqlName = `(?:@?[A-Z_#][A-Z0-9_#$@]*|\[[^\]]+\])`

// dataFreeStatement is a whole statement that touches no data: a label, a
// jump, a bare THROW (a CATCH's rethrow), statistics, a wait, a security
// context switch, or moving through a cursor whose query its declaration
// read. A data-touching statement written after one on the same line fails the
// match, so it is never hidden.
var dataFreeStatement = regexp.MustCompile(`(?is)\A\s*(?:` + strings.Join([]string{
	`[A-Z_][A-Z0-9_]*:`,
	`GOTO\s+[A-Z_][A-Z0-9_]*`,
	`BREAK|CONTINUE|REVERT|THROW`,
	`UPDATE\s+STATISTICS\s+` + sqlName + `(?:\.` + sqlName + `)*(?:\s+` + sqlName + `|\s*\([^()]*\))?(?:\s+WITH\s+(?:FULLSCAN|RESAMPLE|NORECOMPUTE|ALL|COLUMNS|INDEX)(?:\s*,\s*(?:FULLSCAN|RESAMPLE|NORECOMPUTE|ALL|COLUMNS|INDEX))*)?`,
	`(?:OPEN|CLOSE|DEALLOCATE)\s+(?:GLOBAL\s+)?` + sqlName,
	`FETCH\s+(?:(?:NEXT|PRIOR|FIRST|LAST|(?:ABSOLUTE|RELATIVE)\s+[-@\w]+)\s+)?(?:FROM\s+)?(?:GLOBAL\s+)?` + sqlName + `(?:\s+INTO\s+@\w+(?:\s*,\s*@\w+)*)?`,
	`WAITFOR\s+(?:DELAY|TIME)\s+(?:N?'[^']*'|@\w+)`,
	`EXEC(?:UTE)?\s+AS\s+(?:CALLER|SELF|OWNER|(?:LOGIN|USER)\s*=\s*N?'[^']*')`,
}, "|") + `)\s*;?\s*\z`)

// conditionJump is a jump written on its IF's line, after the condition.
var conditionJump = regexp.MustCompile(`(?is)\s(?:GOTO\s+[A-Z_][A-Z0-9_]*|BREAK|CONTINUE|RETURN)\s*;?\s*\z`)

// messageStatement is a PRINT, RAISERROR or THROW: what it reads is what its
// arguments read, captured as one of the three groups.
var messageStatement = regexp.MustCompile(`(?is)\A\s*(?:PRINT\s+(.+?)|RAISERROR\s*\((.+)\)(?:\s+WITH\s+(?:NOWAIT|LOG|SETERROR)(?:\s*,\s*(?:NOWAIT|LOG|SETERROR))*)?|THROW\s+(.+?))\s*;?\s*\z`)

// cursorDeclaration is a cursor's DECLARE, or a cursor variable SET to a
// CURSOR: the query it runs is the first group, without the FOR READ ONLY or
// FOR UPDATE that may close it.
var cursorDeclaration = regexp.MustCompile(`(?is)\A\s*(?:DECLARE\s+` + sqlName + `\s+(?:(?:INSENSITIVE|SCROLL)\s+)*CURSOR|SET\s+@\w+\s*=\s*CURSOR)\b(?:\s+[A-Z_]+)*?\s+FOR\s+((?:SELECT|WITH)\b.+?)(?:\s+FOR\s+(?:READ\s+ONLY|UPDATE(?:\s+OF\s+[\w\s,]+)?))?\s*;?\s*\z`)

// returnStatement is the RETURN that opens a statement.
var returnStatement = regexp.MustCompile(`(?is)\A\s*RETURN\b`)

// queryStart is a query: what an inline table-valued function returns.
var queryStart = regexp.MustCompile(`(?is)\A\s*(?:SELECT|WITH)\b`)

// returnedQuery is what a RETURN returns, as a statement to read: an inline
// table-valued function's query, parenthesised or not, or a scalar function's
// expression as the one column of a SELECT. A bare RETURN reads nothing.
func returnedQuery(returned string) (string, bool) {
	returned = strings.TrimSpace(returned)
	if returned == "" {
		return "", false
	}
	if inner, ok := parenthesised(returned); ok && queryStart.MatchString(inner) {
		returned = inner
	}
	if queryStart.MatchString(returned) {
		return returned, true
	}
	return "SELECT " + returned, true
}

// parenthesised is what text holds between its parentheses when it is one
// parenthesised whole: its first byte opens the parenthesis its last closes.
func parenthesised(text string) (string, bool) {
	if !strings.HasPrefix(text, "(") || !strings.HasSuffix(text, ")") {
		return "", false
	}
	depth := 0
	var quote byte
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case quote != 0:
			if (quote == '[' && c == ']') || (quote != '[' && c == quote) {
				quote = 0
			}
		case c == '\'' || c == '"' || c == '[':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			if depth--; depth == 0 && i < len(text)-1 {
				return "", false
			}
		}
	}
	return text[1 : len(text)-1], depth == 0
}
