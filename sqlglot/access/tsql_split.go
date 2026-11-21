package access

import (
	"regexp"
	"slices"
	"strings"
)

// segmentKind is what a segment of a T-SQL body is: statements, or a marker
// of the control flow around them.
type segmentKind int

const (
	segmentStatements segmentKind = iota
	segmentBegin
	segmentBeginTry
	segmentBeginCatch
	segmentEnd
	segmentElse
)

// segment is one cut of a T-SQL body: a run of statements [start, end), or a
// BEGIN, END or ELSE marker.
type segment struct {
	kind       segmentKind
	start, end int
}

// sessionOption is a SET of session options (SET NOCOUNT ON, SET ANSI_WARNINGS,
// ARITHABORT OFF): a statement that touches no data.
var sessionOption = regexp.MustCompile(`(?is)\ASET\s+[A-Z_]+(?:\s*,\s*[A-Z_]+)*\s+(?:ON|OFF)\b`)

// statementKeywords are the words a T-SQL statement starts with.
var statementKeywords = []string{
	"SELECT", "INSERT", "UPDATE", "DELETE", "MERGE", "EXEC", "EXECUTE", "DECLARE", "SET", "IF", "WHILE",
	"RETURN", "PRINT", "RAISERROR", "THROW", "TRUNCATE", "WITH", "GOTO", "BREAK", "CONTINUE", "OPEN",
	"FETCH", "CLOSE", "DEALLOCATE", "WAITFOR", "REVERT", "CREATE", "ALTER", "DROP", "BULK",
}

// sqlLabel is a GOTO target, `name:`; `::` is a scope qualifier, not a label.
const sqlLabel = `[A-Z_][A-Z0-9_]*:(?:[^:]|$)`

// statementLineStart is a line opening with a word a T-SQL statement starts
// with, or with a label: where a run holding statements with no semicolon
// between them may be cut.
var statementLineStart = regexp.MustCompile(`(?im)^[ \t]*(?:(?:` + strings.Join(statementKeywords, "|") + `)\b|` + sqlLabel + `)`)

// scanner cuts a comment-blanked T-SQL body into segments at its control
// flow: semicolons, BEGIN/END (with TRY or CATCH), BEGIN/COMMIT/ROLLBACK
// TRAN, ELSE and session SET options. Only words outside quotes and
// parentheses count, and an END closing a CASE stays in its statement.
type scanner struct {
	text     string
	from     int
	cases    int
	segments []segment
}

func splitSegments(text string, start int) []segment {
	s := &scanner{text: text, from: start}
	depth := 0
	var quote byte
	for i := start; i < len(text); i++ {
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
			depth--
		case c == ';' && depth == 0:
			s.cut(i, i+1)
		case depth == 0 && wordStartsAt(text, i):
			i = s.word(i) - 1
		}
	}
	s.cut(len(text), len(text))
	return s.segments
}

// word reads the word at i, cutting the body when it is a separator, and
// returns the offset scanning resumes at.
func (s *scanner) word(i int) int {
	end := wordEnd(s.text, i)
	switch strings.ToUpper(s.text[i:end]) {
	case "CASE":
		s.cases++
	case "END":
		if s.cases > 0 {
			s.cases--
			return end
		}
		return s.mark(i, s.optionalWord(end, "TRY", "CATCH"), segmentEnd)
	case "BEGIN":
		resume := s.transaction(end, "TRY", "CATCH", "TRAN", "TRANSACTION")
		switch strings.ToUpper(strings.TrimSpace(s.text[end:resume])) {
		case "":
			return s.mark(i, resume, segmentBegin)
		case "TRY":
			return s.mark(i, resume, segmentBeginTry)
		case "CATCH":
			return s.mark(i, resume, segmentBeginCatch)
		}
		return s.cut(i, resume)
	case "COMMIT", "ROLLBACK":
		return s.cut(i, s.transaction(end, "TRAN", "TRANSACTION", "WORK"))
	case "ELSE":
		if s.cases == 0 {
			return s.mark(i, end, segmentElse)
		}
	case "SET":
		if option := sessionOption.FindStringIndex(s.text[i:]); option != nil {
			return s.cut(i, i+option[1])
		}
	}
	return end
}

// mark cuts the body at at and records the marker [at, resume) of kind.
func (s *scanner) mark(at, resume int, kind segmentKind) int {
	s.cut(at, resume)
	s.segments = append(s.segments, segment{kind: kind, start: at, end: resume})
	return resume
}

// cut closes the statements before at, from their first non-blank byte and
// unless they are blank, and starts the next ones at resume.
func (s *scanner) cut(at, resume int) int {
	from := s.from
	for from < at && isSpace(s.text[from]) {
		from++
	}
	if from < at {
		s.segments = append(s.segments, segment{kind: segmentStatements, start: from, end: at})
	}
	s.from = resume
	return resume
}

// optionalWord is the end of the word after offset when it is one of words,
// otherwise offset.
func (s *scanner) optionalWord(offset int, words ...string) int {
	next := offset
	for next < len(s.text) && isSpace(s.text[next]) {
		next++
	}
	if next == len(s.text) || !wordStartsAt(s.text, next) {
		return offset
	}
	end := wordEnd(s.text, next)
	for _, word := range words {
		if strings.EqualFold(s.text[next:end], word) {
			return end
		}
	}
	return offset
}

// transaction is the end of the optional word after offset (optionalWord) and,
// when that word is TRAN or TRANSACTION, of the transaction's name: a word or
// @variable on the same line that opens no statement.
func (s *scanner) transaction(offset int, words ...string) int {
	next := s.optionalWord(offset, words...)
	if word := strings.ToUpper(strings.TrimSpace(s.text[offset:next])); word != "TRAN" && word != "TRANSACTION" {
		return next
	}
	name := next
	for name < len(s.text) && (s.text[name] == ' ' || s.text[name] == '\t') {
		name++
	}
	end := wordEnd(s.text, name)
	if name == next || end == name || slices.Contains(statementKeywords, strings.ToUpper(s.text[name:end])) {
		return next
	}
	return end
}

// statementStarts are the offsets in [start, end) a statement may start at:
// start, and every line opening with a statement keyword or a label.
func statementStarts(text string, start, end int) []int {
	starts := []int{start}
	for _, line := range statementLineStart.FindAllStringIndex(text[start:end], -1) {
		if at := start + line[0]; at > start {
			starts = append(starts, at)
		}
	}
	return starts
}

func wordStartsAt(text string, i int) bool {
	c := text[i]
	if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && c != '_' {
		return false
	}
	return i == 0 || !isWordByte(text[i-1]) && text[i-1] != '.'
}

func wordEnd(text string, i int) int {
	for i < len(text) && isWordByte(text[i]) {
		i++
	}
	return i
}

func isWordByte(c byte) bool {
	return c == '_' || c == '@' || c == '#' || c == '$' || c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}
