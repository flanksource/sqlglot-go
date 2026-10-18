package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/sqlglot-go/sqlglot"
)

type InputOptions struct {
	SQL     string `json:"sql" args:"true" help:"SQL text (omit to read stdin)"`
	File    string `json:"file" flag:"file,f" help:"Read SQL from a file instead"`
	Dialect string `json:"dialect" flag:"dialect,d" help:"SQL dialect; use 'dialects' to list available names"`
}

type ParseResult struct {
	Class string          `json:"class"`
	Tree  json.RawMessage `json:"tree"`
	SQL   string          `json:"-"`
}

func (r ParseResult) Pretty() api.Text {
	return clicky.Text(r.Class).NewLine().Add(clicky.CodeBlock("sql", r.SQL))
}

type ValidationResult struct {
	Valid bool   `json:"valid"`
	Class string `json:"class"`
}

func (r ValidationResult) Pretty() api.Text {
	return clicky.Text("valid (" + r.Class + ")")
}

type DialectResult struct {
	Name string `json:"name"`
}

func availableDialects() []DialectResult {
	names := sqlglot.Dialects()
	result := make([]DialectResult, 0, len(names))
	for _, name := range names {
		if name == "" {
			name = "neutral"
		}
		result = append(result, DialectResult{Name: name})
	}
	return result
}

func readSQL(opts InputOptions, stdin io.Reader) (string, error) {
	if opts.SQL != "" && opts.File != "" {
		return "", fmt.Errorf("provide exactly one SQL source: positional SQL or --file")
	}
	var data []byte
	var err error
	switch {
	case opts.File != "":
		data, err = os.ReadFile(opts.File)
		if err != nil {
			return "", fmt.Errorf("read SQL file %q: %w", opts.File, err)
		}
	case opts.SQL != "":
		data = []byte(opts.SQL)
	default:
		data, err = io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("read SQL from stdin: %w", err)
		}
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", fmt.Errorf("empty SQL input")
	}
	return string(data), nil
}

func parseSQL(opts InputOptions, stdin io.Reader) (ParseResult, error) {
	sql, err := readSQL(opts, stdin)
	if err != nil {
		return ParseResult{}, dialectError(opts.Dialect, err)
	}
	result, err := sqlglot.ParseScript(sql, dialectName(opts.Dialect))
	if err != nil {
		return ParseResult{}, dialectError(opts.Dialect, err)
	}
	if len(result.Diagnostics) > 0 || !result.Complete {
		return ParseResult{}, newParseFailure(opts, sql, result)
	}
	formatted, err := sqlglot.FormatScript(sql, dialectName(opts.Dialect))
	if err != nil {
		return ParseResult{}, dialectError(opts.Dialect, err)
	}
	return ParseResult{Class: result.Tree.Class, Tree: result.Tree.DumpJSON(), SQL: formatted}, nil
}

func parseInput(opts InputOptions, stdin io.Reader) (*sqlglot.Expression, error) {
	sql, err := readSQL(opts, stdin)
	if err != nil {
		return nil, err
	}
	return sqlglot.ParseOne(sql, dialectName(opts.Dialect))
}

func validateSQL(opts InputOptions, stdin io.Reader) (ValidationResult, error) {
	tree, err := parseInput(opts, stdin)
	if err != nil {
		return ValidationResult{}, dialectError(opts.Dialect, err)
	}
	switch tree.Class {
	case "Select", "Union", "Intersect", "Except":
		return ValidationResult{Valid: true, Class: tree.Class}, nil
	default:
		return ValidationResult{}, dialectError(opts.Dialect, fmt.Errorf("expected a single query, got %s", tree.Class))
	}
}

func formatSQL(opts InputOptions, stdin io.Reader) (string, error) {
	sql, err := readSQL(opts, stdin)
	if err != nil {
		return "", dialectError(opts.Dialect, err)
	}
	formatted, err := sqlglot.Format(sql, dialectName(opts.Dialect))
	return formatted, dialectError(opts.Dialect, err)
}

type TokenResult struct {
	Type      string   `json:"type"`
	Text      string   `json:"text"`
	Line      int      `json:"line"`
	EndColumn int      `json:"end_column"`
	Start     int      `json:"start"`
	End       int      `json:"end"`
	Comments  []string `json:"comments,omitempty"`
}

func tokenizeSQL(opts InputOptions, stdin io.Reader) ([]TokenResult, error) {
	sql, err := readSQL(opts, stdin)
	if err != nil {
		return nil, dialectError(opts.Dialect, err)
	}
	tokens, err := sqlglot.Tokenize(sql, dialectName(opts.Dialect))
	if err != nil {
		return nil, dialectError(opts.Dialect, err)
	}
	result := make([]TokenResult, 0, len(tokens))
	for _, token := range tokens {
		result = append(result, TokenResult{
			Type: token.Type.String(), Text: token.Text, Line: token.Line,
			EndColumn: token.Col, Start: token.Start, End: token.End, Comments: token.Comments,
		})
	}
	return result, nil
}

func dialectName(name string) string {
	if name == "neutral" {
		return ""
	}
	return name
}

func dialectError(name string, err error) error {
	if err == nil {
		return nil
	}
	if name == "" {
		name = "neutral"
	}
	return fmt.Errorf("%s: %w", name, err)
}
