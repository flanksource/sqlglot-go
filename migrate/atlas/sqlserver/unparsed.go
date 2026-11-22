package sqlserver

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/flanksource/sqlglot-go/sqlglot"
)

// UnparsedError is a definition sqlglot does not read whole, so nothing but its
// header is known. SQL Server may still accept it.
type UnparsedError struct {
	Diagnostics []sqlglot.ParseDiagnostic
}

func (e *UnparsedError) Error() string {
	var problems []string
	for _, d := range e.Diagnostics {
		problem := fmt.Sprintf("%s in %s", d.Label, d.StatementKind)
		if !slices.Contains(problems, problem) {
			problems = append(problems, problem)
		}
	}
	return "expected one completely parsed definition: " + strings.Join(problems, "; ")
}

// Header is the kind and name a CREATE [OR ALTER] module definition declares.
// Schema is empty when the name is unqualified.
type Header struct {
	// Kind is PROCEDURE, FUNCTION, VIEW or TRIGGER.
	Kind, Schema, Name string
}

var moduleKinds = []string{"PROCEDURE", "FUNCTION", "VIEW", "TRIGGER"}

// HeaderOf reads a module's header from its tokens, without reading its body.
func HeaderOf(definition string) (Header, error) {
	header, _, err := readHeader(definition)
	return header, err
}

// readHeader reads definition's header and the tokens after it.
func readHeader(definition string) (Header, []sqlglot.Token, error) {
	batches, err := sqlglot.SplitBatches(definition, Dialect)
	if err != nil {
		return Header{}, nil, fmt.Errorf("sqlserver: split module definition: %w", err)
	}
	if len(batches) != 1 {
		return Header{}, nil, fmt.Errorf("sqlserver: expected one module definition, got %d batches", len(batches))
	}
	tokens, err := sqlglot.Tokenize(definition, Dialect)
	if err != nil {
		return Header{}, nil, fmt.Errorf("sqlserver: tokenize module definition: %w", err)
	}
	at, header := moduleKeyword(tokens), Header{}
	if at < 0 || at+1 >= len(tokens) {
		return Header{}, nil, errors.New("sqlserver: expected a CREATE or ALTER module definition")
	}
	header.Kind = moduleKind(tokens[at].Text)
	if header.Kind == "" {
		return Header{}, nil, fmt.Errorf("sqlserver: unsupported module definition kind %s", tokens[at].Text)
	}
	at++
	header.Name = tokens[at].Text
	if !unqualified(tokens, at) && at+2 < len(tokens) {
		at += 2
		header.Schema, header.Name = header.Name, tokens[at].Text
	}
	return header, tokens[at+1:], nil
}

// moduleKeyword is where the kind follows CREATE, ALTER or CREATE OR ALTER, -1
// when the definition starts with none of them.
func moduleKeyword(tokens []sqlglot.Token) int {
	is := func(i int, text string) bool { return i < len(tokens) && strings.EqualFold(tokens[i].Text, text) }
	switch {
	case is(0, "CREATE") && is(1, "OR") && is(2, "ALTER"):
		return 3
	case is(0, "CREATE"), is(0, "ALTER"):
		return 1
	}
	return -1
}

func moduleKind(word string) string {
	word = strings.ToUpper(word)
	if word == "PROC" {
		return "PROCEDURE"
	}
	if !slices.Contains(moduleKinds, word) {
		return ""
	}
	return word
}

// UnparsedRoutine is the routine definition declares, known by its header
// alone: it has no arguments or return type, and is created as written.
func UnparsedRoutine(definition string) (*Routine, error) {
	header, err := HeaderOf(definition)
	if err != nil {
		return nil, err
	}
	if header.Kind != "PROCEDURE" && header.Kind != "FUNCTION" {
		return nil, fmt.Errorf("sqlserver: CREATE %s makes no routine", header.Kind)
	}
	return &Routine{Kind: header.Kind, Schema: header.Schema, Name: header.Name, Text: definition, unparsed: true}, nil
}

// tokenDefinition is what an unparsed definition is compared by: its tokens
// after its header, so comments and layout do not differ, but keyword case and
// brackets do.
func tokenDefinition(definition string) (string, error) {
	_, tokens, err := readHeader(definition)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, token := range tokens {
		fmt.Fprintf(&out, "%d:%s\n", token.Type, strconv.Quote(token.Text))
	}
	return out.String(), nil
}
