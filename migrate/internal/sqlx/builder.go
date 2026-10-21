// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license.

package sqlx

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"ariga.io/atlas/sql/schema"
)

type Builder struct {
	buf          []byte
	QuoteOpening byte    // quoting identifiers
	QuoteClosing byte    // quoting identifiers
	Schema       *string // schema qualifier
	Indent       string  // indentation string
	level        int     // current indentation level
}

func (b *Builder) Byte(value byte) { b.buf = append(b.buf, value) }

func (b *Builder) Text(value string) { b.buf = append(b.buf, value...) }

func (b *Builder) Len() int { return len(b.buf) }

// P writes a list of phrases to the builder separated and
// suffixed with whitespace.
func (b *Builder) P(phrases ...string) *Builder {
	for _, p := range phrases {
		if p == "" {
			continue
		}
		if b.Len() > 0 && !slices.Contains([]byte{' ', '(', '\n'}, b.lastByte()) {
			b.Byte(' ')
		}
		b.Text(p)
		if p[len(p)-1] != ' ' {
			b.Byte(' ')
		}
	}
	return b
}

// Int64 writes the given value to the builder in base 10.
func (b *Builder) Int64(v int64) *Builder {
	return b.P(strconv.FormatInt(v, 10))
}

// Ident writes the given string quoted as an SQL identifier.
func (b *Builder) Ident(s string) *Builder {
	if s != "" {
		b.Byte(b.QuoteOpening)
		b.Text(strings.ReplaceAll(s, string(b.QuoteClosing), strings.Repeat(string(b.QuoteClosing), 2)))
		b.Byte(b.QuoteClosing)
		b.Byte(' ')
	}
	return b
}

// View writes the view identifier to the builder, prefixed
// with the schema name if exists.
func (b *Builder) View(v *schema.View) *Builder {
	return b.mayQualify(v.Schema, v.Name)
}

// Table writes the table identifier to the builder, prefixed
// with the schema name if exists.
func (b *Builder) Table(t *schema.Table) *Builder {
	return b.mayQualify(t.Schema, t.Name)
}

// RefTable writes the referenced/parent table identifier to the builder.
// Unlike the Table method, RefTable prefix the table with its schema qualifier
// if the "Schema" is set to nil, or if the schema is set to empty (schema-scope),
// and the parent table is in a different schema. For example:
//
//	CREATE TABLE child (
//		id INT PRIMARY KEY,
//		parent_id1 INT REFERENCES other1.parent(id)
//		parent_id2 INT REFERENCES other2.parent(id)
//	);
//
// This case is possible only if the child table (and its schema) was loaded with
// baseline schema (contains other1, other2) or with external references to resources
// not managed by this scope.
func (b *Builder) RefTable(childT, parentT *schema.Table) *Builder {
	// Default schema-scope config (no schema qualifier), both schemas are known, and parent table resides in a different schema.
	if b.Schema != nil && *b.Schema == "" && V(childT.Schema).Name != "" && V(parentT.Schema).Name != "" && !SameSchema(childT.Schema, parentT.Schema) {
		b.Ident(parentT.Schema.Name)
		b.rewriteLastByte('.')
		b.Ident(parentT.Name)
		return b
	}
	return b.Table(parentT)
}

// TableColumn writes the table's resource identifier to the builder, prefixed
// with the schema name if exists.
func (b *Builder) TableColumn(t *schema.Table, c *schema.Column) *Builder {
	return b.mayQualify(t.Schema, t.Name, c.Name)
}

// Func writes the function identifier to the builder, prefixed
// with the schema name if exists.
func (b *Builder) Func(f *schema.Func) *Builder {
	return b.mayQualify(f.Schema, f.Name)
}

// FuncCall writes the function identifier to the builder as a function call,
func (b *Builder) FuncCall(f *schema.Func, args ...string) *Builder {
	b.Func(f).rewriteLastByte('(')
	b.MapComma(args, func(i int, b *Builder) {
		b.Text(args[i])
	})
	b.Byte(')')
	return b
}

// Proc writes the procedure identifier to the builder, prefixed
// with the schema name if exists.
func (b *Builder) Proc(p *schema.Proc) *Builder {
	return b.mayQualify(p.Schema, p.Name)
}

// ProcCall writes the procedure identifier to the builder as a procedure call,
func (b *Builder) ProcCall(p *schema.Proc, args ...string) *Builder {
	b.Proc(p).rewriteLastByte('(')
	b.MapComma(args, func(i int, b *Builder) {
		b.Text(args[i])
	})
	b.Byte(')')
	return b
}

// TableResource writes the table resource identifier to the builder, prefixed
// with the schema name if exists.
func (b *Builder) TableResource(t *schema.Table, r any) *Builder {
	switch c := r.(type) {
	case *schema.Column:
		return b.TableColumn(t, c)
	case *schema.Index:
		return b.mayQualify(t.Schema, t.Name, c.Name)
	default:
		panic(fmt.Sprintf("unexpected table resource: %T", r))
	}
}

// ViewResource writes the view resource identifier to the builder, prefixed
// with the schema name if exists.
func (b *Builder) ViewResource(v *schema.View, r any) *Builder {
	switch c := r.(type) {
	case *schema.Column:
		return b.mayQualify(v.Schema, v.Name, c.Name)
	case *schema.Index:
		return b.mayQualify(v.Schema, v.Name, c.Name)
	default:
		panic(fmt.Sprintf("unexpected view resource: %T", r))
	}
}

// SchemaResource writes the schema resource identifier to the builder, prefixed
// with the schema name if exists.
func (b *Builder) SchemaResource(s *schema.Schema, name string) *Builder {
	return b.mayQualify(s, name)
}

func (b *Builder) mayQualify(s *schema.Schema, top string, children ...string) *Builder {
	switch {
	// Custom qualifier.
	case b.Schema != nil:
		// Empty means skip prefix.
		if *b.Schema != "" {
			b.Ident(*b.Schema)
			b.rewriteLastByte('.')
		}
	// Default schema qualifier.
	case s != nil && s.Name != "":
		b.Ident(s.Name)
		b.rewriteLastByte('.')
	}
	b.Ident(top)
	for _, ident := range children {
		b.rewriteLastByte('.')
		b.Ident(ident)
	}
	return b
}

// IndentIn adds one indentation in.
func (b *Builder) IndentIn() *Builder {
	b.level++
	return b
}

// IndentOut removed one indentation level.
func (b *Builder) IndentOut() *Builder {
	b.level--
	return b
}

// NL adds line break and prefix the new line with
// indentation in case indentation is enabled.
func (b *Builder) NL() *Builder {
	if b.Indent != "" {
		if b.lastByte() == ' ' {
			b.rewriteLastByte('\n')
		} else {
			b.Byte('\n')
		}
		b.Text(strings.Repeat(b.Indent, b.level))
	}
	return b
}

// Comma writes a comma in case the buffer is not empty, or
// replaces the last char if it is a whitespace.
func (b *Builder) Comma() *Builder {
	switch {
	case b.Len() == 0:
	case b.lastByte() == ' ':
		b.rewriteLastByte(',')
		b.Byte(' ')
	default:
		b.Text(", ")
	}
	return b
}

// MapComma maps the slice x using the function f and joins the result with
// a comma separating between the written elements.
func (b *Builder) MapComma(x any, f func(i int, b *Builder)) *Builder {
	s := reflect.ValueOf(x)
	for i := 0; i < s.Len(); i++ {
		if i > 0 {
			b.Comma()
		}
		f(i, b)
	}
	return b
}

// Quote wraps the given function with a single quote and a prefix
func (b *Builder) Quote(prefix string, fn func(b *Builder)) *Builder {
	b.Text(prefix)
	b.Byte('\'')
	fn(b)
	if b.lastByte() != ' ' {
		b.Byte('\'')
	} else {
		b.rewriteLastByte('\'')
	}
	return b
}

// MapIndent is like MapComma, but writes a new line before each element.
func (b *Builder) MapIndent(x any, f func(i int, b *Builder)) *Builder {
	return b.MapComma(x, func(i int, b *Builder) {
		f(i, b.NL())
	})
}

// MapCommaErr is like MapComma, but returns an error if f returns an error.
func (b *Builder) MapCommaErr(x any, f func(i int, b *Builder) error) error {
	s := reflect.ValueOf(x)
	for i := 0; i < s.Len(); i++ {
		if i > 0 {
			b.Comma()
		}
		if err := f(i, b); err != nil {
			return err
		}
	}
	return nil
}

// MapIndentErr is like MapCommaErr, but writes a new line before each element.
func (b *Builder) MapIndentErr(x any, f func(i int, b *Builder) error) error {
	return b.MapCommaErr(x, func(i int, b *Builder) error {
		return f(i, b.NL())
	})
}

// Wrap wraps the written string with parentheses.
func (b *Builder) Wrap(f func(b *Builder)) *Builder {
	b.Byte('(')
	f(b)
	if b.lastByte() != ' ' {
		b.Byte(')')
	} else {
		b.rewriteLastByte(')')
	}
	return b
}

// WrapErr wraps the written string with parentheses
func (b *Builder) WrapErr(f func(b *Builder) error) error {
	var err error
	b.Wrap(func(b *Builder) { err = f(b) })
	return err
}

// WrapIndent is like Wrap but with extra level of indentation.
func (b *Builder) WrapIndent(f func(b *Builder)) *Builder {
	return b.Wrap(func(b *Builder) {
		b.IndentIn()
		f(b)
		b.IndentOut()
		b.NL()
	})
}

// WrapIndentErr is like WrapErr but with extra level of indentation.
func (b *Builder) WrapIndentErr(f func(b *Builder) error) error {
	var err error
	b.Wrap(func(b *Builder) {
		b.IndentIn()
		err = f(b)
		b.IndentOut()
		b.NL()
	})
	return err
}

// Clone returns a duplicate of the builder.
func (b *Builder) Clone() *Builder {
	clone := *b
	clone.buf = slices.Clone(b.buf)
	return &clone
}

// String returns the statement without padding.
func (b *Builder) String() string {
	return strings.TrimSpace(string(b.buf))
}

func (b *Builder) lastByte() byte {
	if b.Len() == 0 {
		return 0
	}
	return b.buf[len(b.buf)-1]
}

func (b *Builder) rewriteLastByte(c byte) {
	if b.Len() == 0 {
		return
	}
	b.buf[len(b.buf)-1] = c
}

// IsQuoted reports if the given string is quoted with one of the given quotes (e.g. ', ", `).
func IsQuoted(s string, q ...byte) bool {
	last := len(s) - 1
	if last < 1 {
		return false
	}
Top:
	for _, quote := range q {
		if s[0] != quote || s[last] != quote {
			continue
		}
		for i := 1; i < last-1; i++ {
			switch c := s[i]; {
			case c == '\\', c == quote && s[i+1] == quote:
				i++
			// Accept only escaped quotes and reject otherwise.
			case c == quote:
				continue Top
			}
		}
		return true
	}
	return false
}

// IsLiteralBool reports if the given string is a valid literal bool.
func IsLiteralBool(s string) bool {
	_, err := strconv.ParseBool(s)
	return err == nil
}

// IsLiteralNumber reports if the given string is a literal number.
func IsLiteralNumber(s string) bool {
	// Hex digits.
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		// Some databases allow odd length hex string.
		_, err := strconv.ParseUint(s[2:], 16, 64)
		return err == nil
	}
	// Digits with optional exponent.
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// DefaultValue returns the string represents the DEFAULT of a column.
func DefaultValue(c *schema.Column) (string, bool) {
	switch x := schema.UnderlyingExpr(c.Default).(type) {
	case nil:
		return "", false
	case *schema.Literal:
		return x.V, true
	case *schema.RawExpr:
		return x.X, true
	default:
		panic(fmt.Sprintf("unexpected default value type: %T", x))
	}
}

// MayWrap ensures the given string is wrapped with parentheses.
// Used by the different drivers to turn strings valid expressions.
func MayWrap(s string) string {
	n := len(s) - 1
	if len(s) < 2 || s[0] != '(' || s[n] != ')' || !balanced(s[1:n]) {
		return "(" + s + ")"
	}
	return s
}

func balanced(expr string) bool {
	return ExprLastIndex(expr) == len(expr)-1
}

// ExprLastIndex scans the first expression in the given string until
// its end and returns its last index.
func ExprLastIndex(expr string) int {
	var l, r int
	for i := 0; i < len(expr); i++ {
	Top:
		switch expr[i] {
		case '(':
			l++
		case ')':
			r++
		// String or identifier.
		case '\'', '"', '`':
			for j := i + 1; j < len(expr); j++ {
				switch expr[j] {
				case '\\':
					j++
				case expr[i]:
					i = j
					break Top
				}
			}
			// Unexpected EOS.
			return -1
		}
		// Balanced parens and we reached EOS or a terminator.
		if l == r && (i == len(expr)-1 || expr[i+1] == ',') {
			return i
		} else if r > l {
			return -1
		}
	}
	return -1
}

// ReverseChanges reverses the order of the changes.
func ReverseChanges(c []schema.Change) {
	for i, n := 0, len(c); i < n/2; i++ {
		c[i], c[n-i-1] = c[n-i-1], c[i]
	}
}

// P returns a pointer to v.
func P[T any](v T) *T {
	return &v
}

// V returns the value p is pointing to.
// If p is nil, the zero value is returned.
func V[T any](p *T) (v T) {
	if p != nil {
		v = *p
	}
	return
}

// IsUint reports whether the string represents an unsigned integer.
func IsUint(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func ValuesEqual(v1, v2 []string) bool {
	if len(v1) != len(v2) {
		return false
	}
	for i := range v1 {
		if v1[i] != v2[i] {
			return false
		}
	}
	return true
}
