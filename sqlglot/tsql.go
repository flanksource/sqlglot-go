package sqlglot

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Helpers for reading T-SQL modules -- stored procedures and functions -- as
// a migration tool sees them: scripts split into batches, a definition reduced
// to a canonical form that two spellings of the same routine share, and the
// routine's signature read off its tree.

// SplitBatches splits a script into its batches at each GO that stands alone
// on its line, as sqlcmd and SSMS do. A GO inside a string or a comment is
// text, not a separator, because the script is tokenized rather than scanned
// by line. Batches holding nothing but whitespace and comments are dropped.
//
// `GO <count>`, which runs the batch before it that many times, is refused:
// a definition run twice is not a definition. So is anything else after GO on
// its line.
func SplitBatches(script, dialect string) ([]string, error) {
	tk, err := NewTokenizer(dialect)
	if err != nil {
		return nil, err
	}
	tk.noCommandPayload = true
	tokens, err := tk.Tokenize(script)
	if err != nil {
		return nil, fmt.Errorf("split batches: %w", err)
	}
	src := []rune(script)
	var batches []string
	start, first := 0, 0
	for i, token := range tokens {
		if !separatesBatches(tokens, i) {
			continue
		}
		if next := i + 1; next < len(tokens) && tokens[next].Line == token.Line {
			return nil, fmt.Errorf("split batches: line %d: GO must stand alone on its line, found %q after it", token.Line, tokens[next].Text)
		}
		if i > first {
			batches = append(batches, strings.TrimSpace(string(src[start:token.Start])))
		}
		start, first = token.End+1, i+1
	}
	if len(tokens) > first {
		batches = append(batches, strings.TrimSpace(string(src[start:])))
	}
	return batches, nil
}

// separatesBatches reports whether token i is a GO that begins its line.
func separatesBatches(tokens []Token, i int) bool {
	token := tokens[i]
	if token.Type == TokIDENTIFIER || !strings.EqualFold(token.Text, "GO") {
		return false
	}
	return i == 0 || tokens[i-1].Line < token.Line
}

// CanonicalSQL is the definition written back from its tree, with the
// differences that do not change what a routine is taken out: comments,
// whitespace, keyword case and brackets round a name that reads the same
// without them all vanish; CREATE OR ALTER becomes CREATE, PROC becomes
// PROCEDURE, and a parameter's OUT becomes OUTPUT. String literals are kept
// exactly as written. A definition that does not parse is an error -- there
// is no textual fallback.
func CanonicalSQL(sql, dialect string) (string, error) {
	tree, err := ParseOne(sql, dialect)
	if err != nil {
		return "", fmt.Errorf("canonical sql: %w", err)
	}
	tree = tree.Copy()
	tree.Walk(func(e *Expression) bool {
		canonicalise(e, dialect)
		return true
	})
	out, err := Generate(tree, dialect)
	if err != nil {
		return "", fmt.Errorf("canonical sql: %w", err)
	}
	return out, nil
}

// canonicalise rewrites one node of a tree CanonicalSQL owns.
func canonicalise(e *Expression, dialect string) {
	switch e.Class {
	case "Create":
		e.Set("replace", false)
		if kind, _ := e.Args["kind"].(string); strings.EqualFold(kind, "PROC") {
			e.Set("kind", "PROCEDURE")
		}
	case "ColumnDef":
		switch output, _ := e.Args["output"].(string); strings.ToUpper(output) {
		case "OUT", "OUTPUT":
			e.Set("output", "OUTPUT")
		case "READONLY":
			e.Set("output", "READONLY")
		}
	case "Identifier":
		if quoted, _ := e.Args["quoted"].(bool); quoted && readsAsABareName(e.Name(), dialect) {
			e.Set("quoted", false)
		}
	}
}

// readsAsABareName reports whether a name written without quotes is read back
// as that same name: one word, and not one of the dialect's keywords.
func readsAsABareName(name, dialect string) bool {
	tokens, err := Tokenize(name, dialect)
	return err == nil && len(tokens) == 1 && tokens[0].Type == TokVAR && tokens[0].Text == name
}

// Routine is what a CREATE PROCEDURE or CREATE FUNCTION says about the
// routine it makes.
type Routine struct {
	Kind         string // PROCEDURE or FUNCTION; PROC reads as PROCEDURE
	Schema, Name string
	OrAlter      bool // CREATE OR ALTER (or OR REPLACE)
	Params       []Param
	// Returns is what a function returns -- a DataType, the Var TABLE, or a
	// Schema of the columns a named table returns -- and nil for a procedure.
	Returns *Expression
	// Body is a Block of statements, or a function's Return.
	Body *Expression
}

// Param is one parameter of a routine.
type Param struct {
	Name     string // with its @
	Type     *Expression
	Default  *Expression
	Output   bool // OUTPUT or OUT
	ReadOnly bool // a READONLY table-valued parameter
}

// RoutineOf reads the routine a CREATE PROCEDURE or CREATE FUNCTION tree
// makes. Any other tree is an error.
func RoutineOf(e *Expression) (Routine, error) {
	if e == nil || e.Class != "Create" {
		return Routine{}, errors.New("routine: not a CREATE statement")
	}
	kind, _ := e.Args["kind"].(string)
	kind = strings.ToUpper(kind)
	if kind == "PROC" {
		kind = "PROCEDURE"
	}
	if kind != "PROCEDURE" && kind != "FUNCTION" {
		return Routine{}, fmt.Errorf("routine: CREATE %s makes no routine", kind)
	}
	table, params := routineSignature(e)
	if table == nil || table.Class != "Table" {
		return Routine{}, errors.New("routine: the routine has no name")
	}
	routine := Routine{Kind: kind, Name: table.Name(), Returns: routineReturns(e)}
	if db, _ := table.Args["db"].(*Expression); db != nil {
		routine.Schema = db.Name()
	}
	routine.OrAlter, _ = e.Args["replace"].(bool)
	routine.Body, _ = e.Args["expression"].(*Expression)
	for _, param := range params {
		read, err := paramOf(param)
		if err != nil {
			return Routine{}, err
		}
		routine.Params = append(routine.Params, read)
	}
	return routine, nil
}

// routineSignature is the routine's name and its parameter list, which a
// StoredProcedure or a UserDefinedFunction wraps where there are any.
func routineSignature(e *Expression) (*Expression, []*Expression) {
	this, _ := e.Args["this"].(*Expression)
	if this == nil || (this.Class != "StoredProcedure" && this.Class != "UserDefinedFunction") {
		return this, nil
	}
	params, _ := this.Args["expressions"].([]*Expression)
	return this.This(), params
}

func routineReturns(e *Expression) *Expression {
	properties, _ := e.Args["properties"].(*Expression)
	if properties == nil {
		return nil
	}
	items, _ := properties.Args["expressions"].([]*Expression)
	for _, item := range items {
		if item.Class == "ReturnsProperty" {
			return item.This()
		}
	}
	return nil
}

func paramOf(def *Expression) (Param, error) {
	if def.Class != "ColumnDef" {
		return Param{}, fmt.Errorf("routine: a parameter with no name (%s)", def.Class)
	}
	name := def.This()
	param := Param{Name: name.Name()}
	if name.Class == "Parameter" {
		param.Name = "@" + name.This().Name()
	}
	param.Type, _ = def.Args["kind"].(*Expression)
	param.Default, _ = def.Args["default"].(*Expression)
	switch output, _ := def.Args["output"].(string); strings.ToUpper(output) {
	case "OUT", "OUTPUT":
		param.Output = true
	case "READONLY":
		param.ReadOnly = true
	}
	return param, nil
}

// ParseDataType reads a bare data type -- `nvarchar(max)`, `decimal(18,2)` --
// as a column definition would. Anything after the type is an error.
func ParseDataType(s, dialect string) (*Expression, error) {
	tk, err := NewTokenizer(dialect)
	if err != nil {
		return nil, err
	}
	tokens, err := tk.Tokenize(s)
	if err != nil {
		return nil, fmt.Errorf("data type: %w", err)
	}
	cfg := tk.Config()
	p := &parser{tokens: tokens, cfg: cfg, tables: cfg.Tables, dialect: dialect, sql: s, inColumnType: true}
	if p.curr() == nil {
		return nil, errors.New("data type: nothing to read")
	}
	dt, err := p.parseDataType()
	switch {
	case err != nil:
		return nil, fmt.Errorf("data type %q: %w", s, err)
	case dt == nil || dt.Class != "DataType":
		return nil, fmt.Errorf("data type %q: not a data type", s)
	case p.curr() != nil:
		return nil, fmt.Errorf("data type %q: %q after the type", s, p.curr().Text)
	}
	return dt, nil
}

// DataTypeSpec is a data type taken apart: its kind, and what its parameters
// say. A size the type was not written with is nil.
type DataTypeSpec struct {
	Kind string // the tree's kind: NVARCHAR, DECIMAL, UUID, USERDEFINED, ...
	Name string // as the dialect writes it: NUMERIC for DECIMAL, UNIQUEIDENTIFIER for UUID
	Max  bool   // (MAX)
	// Length is a string or binary type's size; Precision and Scale a
	// number's, and Scale alone a time's fractional seconds -- DATETIME2(7).
	Length, Precision, Scale *int
}

func (s DataTypeSpec) String() string {
	size := func(v *int) string {
		if v == nil {
			return "-"
		}
		return strconv.Itoa(*v)
	}
	return fmt.Sprintf("%s(%s max=%t length=%s precision=%s scale=%s)",
		s.Kind, s.Name, s.Max, size(s.Length), size(s.Precision), size(s.Scale))
}

// DataTypeSpecOf takes a DataType node apart, naming it as the dialect writes
// it.
func DataTypeSpecOf(e *Expression, dialect string) (DataTypeSpec, error) {
	if e == nil || e.Class != "DataType" {
		return DataTypeSpec{}, errors.New("data type: not a DataType")
	}
	bare := e.Copy()
	bare.Set("expressions", nil)
	name, err := Generate(bare, dialect)
	if err != nil {
		return DataTypeSpec{}, fmt.Errorf("data type: %w", err)
	}
	spec := DataTypeSpec{Kind: typeKind(e), Name: name}
	params, _ := e.Args["expressions"].([]*Expression)
	sizes := make([]*int, 0, len(params))
	for _, param := range params {
		value := param.This()
		if value != nil && value.Class == "Var" && strings.EqualFold(value.Name(), "MAX") && len(params) == 1 {
			spec.Max = true
			return spec, nil
		}
		size, err := strconv.Atoi(value.Name())
		if value == nil || value.Class != "Literal" || err != nil {
			return DataTypeSpec{}, fmt.Errorf("data type %s: a parameter that is not a size", name)
		}
		sizes = append(sizes, &size)
	}
	return spec.withSizes(sizes)
}

// withSizes says what a type's numeric parameters are, which depends on the
// type: precision and scale for a decimal, fractional seconds for a time,
// a length for everything else.
func (s DataTypeSpec) withSizes(sizes []*int) (DataTypeSpec, error) {
	if len(sizes) == 0 {
		return s, nil
	}
	switch s.Kind {
	case "DECIMAL", "NUMERIC", "BIGDECIMAL":
		if len(sizes) > 2 {
			break
		}
		s.Precision = sizes[0]
		if len(sizes) == 2 {
			s.Scale = sizes[1]
		}
		return s, nil
	case "FLOAT", "DOUBLE":
		if len(sizes) == 1 {
			s.Precision = sizes[0]
			return s, nil
		}
	case "DATETIME2", "TIME", "TIMESTAMPTZ", "DATETIMEOFFSET":
		if len(sizes) == 1 {
			s.Scale = sizes[0]
			return s, nil
		}
	default:
		if len(sizes) == 1 {
			s.Length = sizes[0]
			return s, nil
		}
	}
	return DataTypeSpec{}, fmt.Errorf("data type %s: %d sizes", s.Name, len(sizes))
}
