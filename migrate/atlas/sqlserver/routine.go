package sqlserver

import (
	"fmt"
	"slices"
	"strings"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/sqlglot"
)

// Definition is the T-SQL text a routine is created from: the declared .sql
// batch for a desired routine, sys.sql_modules.definition for a live one. The
// differ compares definitions by their sqlglot canonical form, and the planner
// executes a declared definition as written.
type Definition struct {
	schema.Attr
	Text string
}

// Routine is one CREATE PROCEDURE or CREATE FUNCTION read from its definition.
type Routine struct {
	// Kind is PROCEDURE or FUNCTION.
	Kind string
	// Schema is the schema the definition names, empty when unqualified.
	Schema string
	Name   string
	// Text is the definition as written.
	Text   string
	parsed sqlglot.Routine
}

// ParseRoutine parses a single CREATE [OR ALTER] PROCEDURE|FUNCTION batch. A
// batch holding anything else, or more than one statement, is an error.
func ParseRoutine(definition string) (*Routine, error) {
	script, err := sqlglot.ParseScript(definition, Dialect)
	if err != nil {
		return nil, fmt.Errorf("sqlserver: parse routine: %w", err)
	}
	if !script.Complete || script.Tree == nil || script.Tree.Class == "Block" || len(script.Diagnostics) > 0 {
		return nil, fmt.Errorf("sqlserver: parse routine: expected one completely parsed definition: %v", script.Diagnostics)
	}
	parsed, err := sqlglot.RoutineOf(script.Tree)
	if err != nil {
		return nil, fmt.Errorf("sqlserver: %w", err)
	}
	return &Routine{Kind: parsed.Kind, Schema: parsed.Schema, Name: parsed.Name, Text: definition, parsed: parsed}, nil
}

// AddTo adds the routine to s as a schema.Proc or schema.Func carrying its
// Definition. A routine qualified with another schema is an error.
func (r *Routine) AddTo(s *schema.Schema) error {
	if r.Schema != "" && !strings.EqualFold(r.Schema, s.Name) {
		return fmt.Errorf("sqlserver: routine %s.%s is declared in schema %q, not %q", r.Schema, r.Name, r.Schema, s.Name)
	}
	args, deps, err := r.args(s)
	if err != nil {
		return err
	}
	attrs := []schema.Attr{&Definition{Text: r.Text}}
	if r.Kind == "PROCEDURE" {
		s.AddProcs((&schema.Proc{Name: r.Name, Schema: s, Args: args, Lang: "T-SQL", Attrs: attrs}).AddDeps(deps...))
		return nil
	}
	ret, err := r.returns()
	if err != nil {
		return err
	}
	s.AddFuncs((&schema.Func{Name: r.Name, Schema: s, Args: args, Ret: ret, Lang: "T-SQL", Attrs: attrs}).AddDeps(deps...))
	return nil
}

// args reads the parameters; one typed by a table type declared in s becomes
// a TableTypeRef and a dependency, so the type is created first.
func (r *Routine) args(s *schema.Schema) ([]*schema.FuncArg, []schema.Object, error) {
	args := make([]*schema.FuncArg, 0, len(r.parsed.Params))
	var deps []schema.Object
	for _, param := range r.parsed.Params {
		t, err := expressionType(param.Type)
		if err != nil {
			var tableType *TableType
			if spec, specErr := sqlglot.DataTypeSpecOf(param.Type, Dialect); specErr == nil {
				tableType = FindTableType(s, spec.Name)
			}
			if tableType == nil {
				return nil, nil, fmt.Errorf("sqlserver: routine %s parameter %s: %w", r.Name, param.Name, err)
			}
			t, deps = &TableTypeRef{T: tableType}, append(deps, tableType)
		}
		mode := schema.FuncArgModeIn
		if param.Output {
			mode = schema.FuncArgModeInOut
		}
		args = append(args, &schema.FuncArg{Name: param.Name, Type: t, Mode: mode})
	}
	return args, deps, nil
}

// returns is a scalar function's return type, nil for a table-valued one.
func (r *Routine) returns() (schema.Type, error) {
	if r.parsed.Returns == nil || r.parsed.Returns.Class != "DataType" {
		return nil, nil
	}
	t, err := expressionType(r.parsed.Returns)
	if err != nil {
		return nil, fmt.Errorf("sqlserver: routine %s return type: %w", r.Name, err)
	}
	return t, nil
}

// kindNames maps sqlglot's type kinds to SQL Server type names.
var kindNames = map[string]string{
	"BIT": "bit", "UTINYINT": "tinyint", "SMALLINT": "smallint", "INT": "int", "BIGINT": "bigint",
	"DECIMAL": "decimal", "MONEY": "money", "SMALLMONEY": "smallmoney", "FLOAT": "float",
	"CHAR": "char", "VARCHAR": "varchar", "NCHAR": "nchar", "NVARCHAR": "nvarchar", "TEXT": "text",
	"BINARY": "binary", "VARBINARY": "varbinary", "IMAGE": "image",
	"DATE": "date", "TIME": "time", "DATETIME": "datetime", "DATETIME2": "datetime2",
	"SMALLDATETIME": "smalldatetime", "TIMESTAMPTZ": "datetimeoffset", "UUID": "uniqueidentifier",
	"XML": "xml", "VARIANT": "sql_variant", "ROWVERSION": "rowversion",
	"REAL": "real", "NTEXT": "ntext",
}

func expressionType(e *sqlglot.Expression) (schema.Type, error) {
	spec, err := sqlglot.DataTypeSpecOf(e, Dialect)
	if err != nil {
		return nil, err
	}
	// sysname is SQL Server's built-in alias for nvarchar(128).
	if strings.EqualFold(spec.Name, "sysname") {
		return specSize("nvarchar", sqlglot.DataTypeSpec{Length: new(128)}).schemaType()
	}
	name, ok := kindNames[spec.Kind]
	if !ok {
		return nil, fmt.Errorf("type %q is not supported", spec.Name)
	}
	return specSize(name, spec).schemaType()
}

// CreateOrAlter rewrites a CREATE PROCEDURE|FUNCTION definition to CREATE OR
// ALTER, splicing the two words in after the CREATE keyword so every other
// byte of the definition -- comments, layout, literals -- is kept.
func CreateOrAlter(definition string) (string, error) {
	tokens, err := sqlglot.Tokenize(definition, Dialect)
	if err != nil {
		return "", fmt.Errorf("sqlserver: tokenize routine definition: %w", err)
	}
	if len(tokens) == 0 || !strings.EqualFold(tokens[0].Text, "CREATE") && !strings.EqualFold(tokens[0].Text, "ALTER") {
		return "", fmt.Errorf("sqlserver: expected a CREATE or ALTER module definition")
	}
	runes := []rune(definition)
	end := tokens[0].End + 1
	text := definition
	if strings.EqualFold(tokens[0].Text, "ALTER") {
		text = string(runes[:tokens[0].Start]) + "CREATE OR ALTER" + string(runes[end:])
	} else if len(tokens) < 3 || !strings.EqualFold(tokens[1].Text, "OR") || !strings.EqualFold(tokens[2].Text, "ALTER") {
		text = string(runes[:end]) + " OR ALTER" + string(runes[end:])
	}
	parsed, err := sqlglot.ParseScript(text, Dialect)
	if err != nil {
		return "", err
	}
	if !parsed.Complete || parsed.Tree == nil || parsed.Tree.Class != "Create" {
		return "", fmt.Errorf("sqlserver: expected one completely parsed module definition: %v", parsed.Diagnostics)
	}
	switch parsed.Tree.Args["kind"] {
	case "PROCEDURE", "PROC", "FUNCTION", "VIEW", "TRIGGER":
		return text, nil
	default:
		return "", fmt.Errorf("sqlserver: unsupported module definition kind %v", parsed.Tree.Args["kind"])
	}
}

// QualifyDefinition prefixes an unqualified module name -- and a trigger's
// unqualified ON target -- with [schema]. SQL Server creates an unqualified
// module in the executing login's default schema, so a replayed definition
// must name the schema the catalog placed it in. Every other byte is kept.
func QualifyDefinition(definition, schema string) (string, error) {
	tokens, err := sqlglot.Tokenize(definition, Dialect)
	if err != nil {
		return "", fmt.Errorf("sqlserver: tokenize module definition: %w", err)
	}
	kind := slices.IndexFunc(tokens, func(t sqlglot.Token) bool {
		return slices.ContainsFunc([]string{"PROC", "PROCEDURE", "FUNCTION", "VIEW", "TRIGGER"}, func(k string) bool { return strings.EqualFold(t.Text, k) })
	})
	if kind < 1 || kind+1 >= len(tokens) {
		return "", fmt.Errorf("sqlserver: expected a CREATE or ALTER module definition")
	}
	var inserts []int
	if unqualified(tokens, kind+1) {
		inserts = append(inserts, tokens[kind+1].Start)
	}
	if strings.EqualFold(tokens[kind].Text, "TRIGGER") {
		on := slices.IndexFunc(tokens[kind+1:], func(t sqlglot.Token) bool { return strings.EqualFold(t.Text, "ON") })
		if on >= 0 && unqualified(tokens, kind+2+on) {
			inserts = append(inserts, tokens[kind+2+on].Start)
		}
	}
	runes := []rune(definition)
	prefix := []rune("[" + strings.ReplaceAll(schema, "]", "]]") + "].")
	for i := len(inserts) - 1; i >= 0; i-- {
		at := inserts[i]
		runes = append(runes[:at], append(slices.Clone(prefix), runes[at:]...)...)
	}
	return string(runes), nil
}

// unqualified reports a one-part object name starting at tokens[i].
func unqualified(tokens []sqlglot.Token, i int) bool {
	return i < len(tokens) && (i+1 >= len(tokens) || tokens[i+1].Text != ".")
}

// canonicalDefinition is a definition's sqlglot canonical form: comments,
// whitespace, keyword case, brackets and OR ALTER normalised away.
func canonicalDefinition(name, definition string) (string, error) {
	text, err := CreateOrAlter(definition)
	if err != nil {
		return "", fmt.Errorf("sqlserver: routine %s: %w", name, err)
	}
	canonical, err := sqlglot.CanonicalSQL(text, Dialect)
	if err != nil {
		return "", fmt.Errorf("sqlserver: routine %s: %w", name, err)
	}
	return canonical, nil
}

// definitionOf returns the Definition attribute a routine carries.
func definitionOf(name string, attrs []schema.Attr) (string, error) {
	for _, attr := range attrs {
		if d, ok := attr.(*Definition); ok {
			return d.Text, nil
		}
	}
	return "", fmt.Errorf("sqlserver: routine %s has no T-SQL definition", name)
}
