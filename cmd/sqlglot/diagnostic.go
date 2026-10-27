package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/sqlglot-go/sqlglot"
	"github.com/mattn/go-runewidth"
)

type SourceDiagnostic struct {
	Kind           string `json:"kind"`
	Label          string `json:"label"`
	StatementKind  string `json:"statement_kind"`
	Path           string `json:"path"`
	Line           int    `json:"line"`
	Column         int    `json:"column"`
	Start          int    `json:"start"`
	End            int    `json:"end"`
	StatementStart int    `json:"statement_start"`
	Excerpt        string `json:"excerpt"`
}

type ParseFailure struct {
	Dialect     string             `json:"dialect"`
	Diagnostics []SourceDiagnostic `json:"diagnostics"`
}

func (f *ParseFailure) Unwrap() error {
	status := entity.NewStatusError(400, "invalid_sql", f.Error())
	status.Context = map[string]any{"diagnostics": f.Diagnostics}
	status.Hint = "Correct the highlighted SQL and try again."
	return status
}

func (f *ParseFailure) Error() string {
	if len(f.Diagnostics) == 1 {
		return fmt.Sprintf("%s: 1 SQL diagnostic", f.Dialect)
	}
	return fmt.Sprintf("%s: %d SQL diagnostics", f.Dialect, len(f.Diagnostics))
}

func (f *ParseFailure) Pretty() api.Text {
	out := clicky.Text(f.Error())
	for _, diagnostic := range f.Diagnostics {
		out = out.NewLine().Add(clicky.Text(fmt.Sprintf("%s: %s:%d:%d: %s: %s", f.Dialect, diagnostic.Path,
			diagnostic.Line, diagnostic.Column, diagnostic.Kind, diagnostic.Label)))
		out = out.NewLine().Add(clicky.CodeBlock("sql", diagnostic.Excerpt))
	}
	return out
}

func newParseFailure(opts InputOptions, sql string, result sqlglot.ScriptResult) *ParseFailure {
	name := opts.Dialect
	if name == "" {
		name = "neutral"
	}
	path := opts.File
	if path == "" && opts.SQL != "" {
		path = "<sql>"
	} else if path == "" {
		path = "<stdin>"
	}
	source := []rune(sql)
	starts := []int{0}
	for i, r := range source {
		if r == '\n' {
			starts = append(starts, i+1)
		}
	}
	failure := &ParseFailure{Dialect: name}
	for _, diagnostic := range result.Diagnostics {
		line, column := sourcePosition(starts, diagnostic.Start)
		failure.Diagnostics = append(failure.Diagnostics, SourceDiagnostic{
			Kind: diagnostic.Kind, Label: diagnostic.Label, StatementKind: diagnostic.StatementKind,
			Path: path, Line: line, Column: column, Start: diagnostic.Start, End: diagnostic.End,
			StatementStart: diagnostic.StatementStart,
			Excerpt:        sourceExcerpt(source, starts, line, column),
		})
	}
	return failure
}

func sourcePosition(starts []int, offset int) (int, int) {
	line := sort.Search(len(starts), func(i int) bool { return starts[i] > offset })
	return line, offset - starts[line-1] + 1
}

func sourceExcerpt(source []rune, starts []int, line, column int) string {
	first := max(1, line-2)
	last := min(len(starts), line+2)
	var out strings.Builder
	for number := first; number <= last; number++ {
		end := len(source)
		if number < len(starts) {
			end = starts[number] - 1
		}
		if end > starts[number-1] && source[end-1] == '\r' {
			end--
		}
		focus := -1
		if number == line {
			focus = column - 1
		}
		clipped, caret := clipSourceLine(source[starts[number-1]:end], focus)
		prefix := fmt.Sprintf("%d | ", number)
		out.WriteString(prefix + clipped + "\n")
		if focus >= 0 {
			out.WriteString(strings.Repeat(" ", len(prefix)+caret) + "^\n")
		}
	}
	return strings.TrimSuffix(out.String(), "\n")
}

func clipSourceLine(line []rune, focus int) (string, int) {
	const maxColumns = 160
	columns := make([]int, len(line)+1)
	for i, r := range line {
		width := runewidth.RuneWidth(r)
		if r == '\t' {
			width = 4 - columns[i]%4
		}
		columns[i+1] = columns[i] + width
	}
	if focus < 0 {
		focus = 0
	}
	focus = min(focus, len(line))
	left := max(0, columns[focus]-maxColumns/2)
	left = min(left, max(0, columns[len(line)]-maxColumns))
	start := sort.Search(len(columns), func(i int) bool { return columns[i] >= left })
	end := sort.Search(len(columns), func(i int) bool { return columns[i] > left+maxColumns }) - 1
	end = max(start, min(end, len(line)))
	var out strings.Builder
	if start > 0 {
		out.WriteString("…")
	}
	for i := start; i < end; i++ {
		if line[i] == '\t' {
			out.WriteString(strings.Repeat(" ", columns[i+1]-columns[i]))
		} else {
			out.WriteRune(line[i])
		}
	}
	if end < len(line) {
		out.WriteString("…")
	}
	caret := max(0, columns[focus]-columns[start])
	if start > 0 {
		caret++
	}
	return out.String(), caret
}
