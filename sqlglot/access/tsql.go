package access

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/flanksource/sqlglot-go/sqlglot"
)

// File is what one T-SQL file does.
type File struct {
	// Routines holds each CREATE (OR ALTER) / ALTER PROCEDURE or FUNCTION, in
	// the order they are read: a routine the string literal of an EXEC
	// defines is read where that EXEC is, before the routine holding it.
	Routines []Routine `json:"routines,omitempty"`
	// Script is what the file's anonymous statements, across GO batches, do
	// when it runs. It is nil when every batch defines a routine, or is
	// setup that accesses nothing, beside one that does.
	Script *Script `json:"script,omitempty"`
	// Diagnostics are every statement, in a routine or not, whose accesses
	// could not be read. None is ever dropped.
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// Routine is one routine a file creates or alters, and what its body does
// when it runs.
type Routine struct {
	// Object is the routine's name, in dbo when no schema is written.
	Object Object `json:"object"`
	// Definition is the routine's signature, read off its header.
	Definition sqlglot.Routine `json:"-"`
	// Line is the line its CREATE or ALTER starts on.
	Line        int          `json:"line"`
	Body        []Node       `json:"body,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// Script is a file's anonymous statements: the line the first starts on, and
// what they do.
type Script struct {
	Line int    `json:"line"`
	Body []Node `json:"body,omitempty"`
}

// tsqlDefaultSchema is the schema SQL Server resolves an unqualified name in.
const tsqlDefaultSchema = "dbo"

// procedureHeader is a CREATE/ALTER PROCEDURE or FUNCTION header up to the AS
// its body follows. A parameter written `@p AS int` ends it early; the body is
// then cut short and reported unparsed, never guessed at.
var procedureHeader = regexp.MustCompile(`(?is)\A\s*(?:CREATE|ALTER)\s+(?:OR\s+ALTER\s+)?(?:PROC|PROCEDURE|FUNCTION)\b.*?\bAS\b`)

// executeAsOpen is the EXECUTE of a header's `WITH EXECUTE AS <principal>`,
// ending where its AS starts; executeAsPrincipal is the principal after that
// AS, and the AS the body follows.
var (
	executeAsOpen      = regexp.MustCompile(`(?is)\bEXEC(?:UTE)?\s+\z`)
	executeAsPrincipal = regexp.MustCompile(`(?is)\A\s*(?:N?'[^']*'|\w+)(?:\s*,\s*\w+)*\s+AS\b`)
)

// headerStub is the body a header is parsed with: sqlglot reads a routine's
// signature only from a whole definition, and every procedure and function
// kind accepts this one.
const headerStub = "\nBEGIN RETURN END"

const nonBreakingSpace = " "

// ReadTSQL reads what a T-SQL file does: the routines it defines and its
// anonymous statements. A statement that cannot be read is a diagnostic, and
// reading goes on; only a file that does not split into GO batches is an
// error.
func ReadTSQL(src string) (File, error) {
	f := &fileReader{src: src}
	if err := f.read(); err != nil {
		return File{}, fmt.Errorf("access: %w", err)
	}
	return f.file, nil
}

// fileReader is the state of one ReadTSQL, or of the dynamic SQL one
// statement runs.
type fileReader struct {
	src  string
	file File
}

// sourceBatch is one GO batch, comments blanked, and the line it starts on.
type sourceBatch struct {
	text      string
	firstLine int
}

// read reads each batch's routine and the anonymous batches, together, as
// the file's script.
func (f *fileReader) read() error {
	batches, err := f.batches()
	if err != nil {
		return err
	}
	var anonymous []sourceBatch
	seenRoutine := false
	for _, batch := range batches {
		if procedureBodyStart(batch.text) == 0 {
			if strings.TrimSpace(batch.text) != "" {
				anonymous = append(anonymous, batch)
			}
			continue
		}
		seenRoutine = true
		f.readRoutine(batch.text, batch.firstLine)
	}
	if len(anonymous) == 0 {
		return nil
	}
	diagnosticsBefore := len(f.file.Diagnostics)
	var body []Node
	for _, batch := range anonymous {
		body = append(body, f.body(batch.text, batch.firstLine).read(0)...)
	}
	if !seenRoutine || len(body) > 0 || len(f.file.Diagnostics) > diagnosticsBefore {
		first := anonymous[0]
		f.file.Script = &Script{Line: first.firstLine + lineAt(first.text, firstNonSpace(first.text, 0)) - 1, Body: body}
	}
	return nil
}

// batches splits the file at GO, each batch located on the line it starts.
func (f *fileReader) batches() ([]sourceBatch, error) {
	batches, err := sqlglot.SplitBatches(f.src, "tsql")
	if err != nil {
		return nil, err
	}
	out := make([]sourceBatch, 0, len(batches))
	cursor := 0
	for _, batch := range batches {
		at := strings.Index(f.src[cursor:], batch)
		if at < 0 {
			return nil, fmt.Errorf("batch %q is not in the source", batch)
		}
		offset := cursor + at
		cursor = offset + len(batch)
		out = append(out, sourceBatch{text: blankComments(batch), firstLine: lineAt(f.src, offset)})
	}
	return out, nil
}

func (f *fileReader) body(text string, firstLine int) *tsqlBody {
	return &tsqlBody{f: f, text: text, firstLine: firstLine}
}

// readRoutine reads the routine one comment-blanked batch defines, which
// starts on firstLine of the file.
func (f *fileReader) readRoutine(text string, firstLine int) {
	start := procedureBodyStart(text)
	if start == 0 {
		return
	}
	line := firstLine + lineAt(text, firstNonSpace(text, 0)) - 1
	header := strings.TrimSpace(text[:start])
	tree, err := sqlglot.ParseOne(header+headerStub, "tsql")
	if err != nil {
		f.diagnose(line, header, "routine header: "+err.Error())
		return
	}
	definition, err := sqlglot.RoutineOf(tree)
	if err != nil {
		f.diagnose(line, header, err.Error())
		return
	}
	diagnosticsBefore := len(f.file.Diagnostics)
	body := f.body(text, firstLine).read(start)
	f.file.Routines = append(f.file.Routines, Routine{
		Object:      tableRef{schema: definition.Schema, name: definition.Name}.object(tsqlDefaultSchema),
		Definition:  definition,
		Line:        line,
		Body:        body,
		Diagnostics: slices.Clone(f.file.Diagnostics[diagnosticsBefore:]),
	})
}

// readDynamic reads the T-SQL an EXEC written on line runs from a string
// literal as a batch of its own, starting on that line: a routine it defines
// joins the file, and the statements it runs are returned to stand at the
// EXEC, under its guards.
func (f *fileReader) readDynamic(sql string, line int) []Node {
	text := blankComments(sql)
	if procedureBodyStart(text) > 0 {
		f.readRoutine(text, line)
		return nil
	}
	return f.body(text, line).read(0)
}

func (f *fileReader) diagnose(line int, statement, message string) {
	f.file.Diagnostics = append(f.file.Diagnostics, Diagnostic{Line: line, Statement: statement, Message: message})
}

// procedureBodyStart is the offset a definition's body starts at: after its
// header, an `EXECUTE AS` option not taken for the AS that ends it. A
// definition with no header is 0.
func procedureBodyStart(text string) int {
	header := procedureHeader.FindStringIndex(text)
	if header == nil {
		return 0
	}
	start := header[1]
	if executeAsOpen.MatchString(text[:start-len("AS")]) {
		if principal := executeAsPrincipal.FindStringIndex(text[start:]); principal != nil {
			start += principal[1]
		}
	}
	return start
}

// blankComments replaces every comment outside a string literal with spaces,
// keeping its newlines, and every UTF-8 non-breaking space outside one with
// two, as SQL Server reads it as whitespace, so offsets and lines in the
// result are those of sql.
func blankComments(sql string) string {
	out := []byte(sql)
	inString := false
	for i := 0; i < len(out); i++ {
		if out[i] == '\'' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if strings.HasPrefix(sql[i:], nonBreakingSpace) {
			out[i], out[i+1] = ' ', ' '
			i++
			continue
		}
		end := commentEnd(sql, i)
		for j := i; j < end; j++ {
			if out[j] != '\n' {
				out[j] = ' '
			}
		}
		if end > i {
			i = end - 1
		}
	}
	return string(out)
}

func commentEnd(query string, i int) int {
	switch {
	case strings.HasPrefix(query[i:], "--"):
		if end := strings.IndexByte(query[i:], '\n'); end >= 0 {
			return i + end
		}
		return len(query)
	case strings.HasPrefix(query[i:], "/*"):
		if end := strings.Index(query[i+2:], "*/"); end >= 0 {
			return i + 2 + end + 2
		}
		return len(query)
	}
	return i
}

func firstNonSpace(text string, offset int) int {
	for offset < len(text) && isSpace(text[offset]) {
		offset++
	}
	return offset
}

// lineAt is the 1-based line of offset in text.
func lineAt(text string, offset int) int {
	return strings.Count(text[:offset], "\n") + 1
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
