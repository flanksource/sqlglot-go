package atlasdriver

import (
	"fmt"
	"slices"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// partialAttr is the table attribute that marks a declaration partial. The
// upstream Atlas evaluators ignore an attribute they do not know, so it would
// never reach the Atlas schema, and a value that is not a boolean would be
// accepted without a word. The declaration is therefore read from the parsed HCL
// before evaluation and carried on migrate.Table.Partial; Native never holds it.
const partialAttr = "partial"

// PartialTable is the `partial` attribute of one table block.
type PartialTable struct {
	// Name is the block's name (its last label).
	Name    string
	Partial bool
	// Range is the attribute's source, so evaluation can be given the file without it.
	Range hcl.Range
}

// ReadPartial reads the `partial` attribute of every table block of one parsed
// file. ctx evaluates variables the value may use; nil allows literals only. A
// value that is not true or false is an error naming the file, line and table.
func ReadPartial(file string, body *hclsyntax.Body, ctx *hcl.EvalContext) ([]PartialTable, error) {
	var out []PartialTable
	for _, block := range body.Blocks {
		attr, ok := block.Body.Attributes[partialAttr]
		if block.Type != "table" || len(block.Labels) == 0 || !ok {
			continue
		}
		name := block.Labels[len(block.Labels)-1]
		value, diagnostics := attr.Expr.Value(ctx)
		if diagnostics.HasErrors() || !value.IsKnown() || value.IsNull() || !value.Type().Equals(cty.Bool) {
			return nil, fmt.Errorf("%s:%d: table %q: partial must be true or false", file, attr.SrcRange.Start.Line, name)
		}
		out = append(out, PartialTable{Name: name, Partial: value.True(), Range: attr.SrcRange})
	}
	return out, nil
}

// BlankPartial returns data with the partial attributes of tables replaced by
// spaces, keeping every line and offset, so the upstream evaluator never sees them.
func BlankPartial(data []byte, tables []PartialTable) []byte {
	out := slices.Clone(data)
	for _, t := range tables {
		for i := t.Range.Start.Byte; i < t.Range.End.Byte && i < len(out); i++ {
			if out[i] != '\n' && out[i] != '\r' {
				out[i] = ' '
			}
		}
	}
	return out
}

// PartialNames lists the tables declared `partial = true`.
func PartialNames(tables []PartialTable) map[string]bool {
	names := map[string]bool{}
	for _, t := range tables {
		if t.Partial {
			names[t.Name] = true
		}
	}
	return names
}

// WithPartial marks the named tables of s partial. A name s does not hold was
// left out of the schema on purpose (an exclusion), so it is skipped.
func WithPartial(s migrate.Schema, names map[string]bool) migrate.Schema {
	s.Tables = slices.Clone(s.Tables)
	for i := range s.Tables {
		s.Tables[i].Partial = names[s.Tables[i].Name.Name]
	}
	return s
}

// SplitPartial reads the partial declarations of one HCL document (an error
// names the file, line and table of a value that is not a boolean) and returns
// the document without them, and the names of the tables declared partial.
func SplitPartial(file, source string) (string, map[string]bool, error) {
	parsed, diagnostics := hclsyntax.ParseConfig([]byte(source), file, hcl.Pos{Line: 1, Column: 1})
	if diagnostics.HasErrors() {
		return "", nil, fmt.Errorf("parse %s: %s", file, diagnostics.Error())
	}
	tables, err := ReadPartial(file, parsed.Body.(*hclsyntax.Body), nil)
	if err != nil {
		return "", nil, err
	}
	return string(BlankPartial([]byte(source), tables)), PartialNames(tables), nil
}
