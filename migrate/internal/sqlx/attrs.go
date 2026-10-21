// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package sqlx

import (
	"ariga.io/atlas/sql/schema"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// CommentChange reports if the element comment was changed.
func CommentChange(from, to []schema.Attr) schema.ChangeKind {
	var c1, c2 schema.Comment
	if Has(from, &c1) != Has(to, &c2) || c1.Text != c2.Text {
		return schema.ChangeComment
	}
	return schema.NoChange
}

// Charset reports if the attribute contains the "charset" attribute,
// and it needs to be defined explicitly on the schema. This is true, in
// case the element charset is different from its parent charset.
func Charset(attr, parent []schema.Attr) (string, bool) {
	var c, p schema.Charset
	if Has(attr, &c) && (parent == nil || Has(parent, &p) && c.V != p.V) {
		return c.V, true
	}
	return "", false
}

// Collate reports if the attribute contains the "collation"/"collate" attribute,
// and it needs to be defined explicitly on the schema. This is true, in
// case the element collation is different from its parent collation.
func Collate(attr, parent []schema.Attr) (string, bool) {
	var c, p schema.Collation
	if Has(attr, &c) && (parent == nil || Has(parent, &p) && c.V != p.V) {
		return c.V, true
	}
	return "", false
}

var (
	attrsType   = reflect.TypeOf(([]schema.Attr)(nil))
	clausesType = reflect.TypeOf(([]schema.Clause)(nil))
	exprsType   = reflect.TypeOf(([]schema.Expr)(nil))
)

// AttrOr returns the first attribute of the given type,
// or the given default value.
func AttrOr[T schema.Attr](attrs []schema.Attr, t T) T {
	for _, attr := range attrs {
		if a, ok := attr.(T); ok {
			return a
		}
	}
	return t
}

// Has finds the first element in the elements list that
// matches target, and if so, sets target to that attribute
// value and returns true.
func Has(elements, target any) bool {
	ev := reflect.ValueOf(elements)
	if t := ev.Type(); t != attrsType && t != clausesType && t != exprsType {
		panic(fmt.Sprintf("unexpected elements type: %T", elements))
	}
	tv := reflect.ValueOf(target)
	if tv.Kind() != reflect.Pointer || tv.IsNil() {
		panic("target must be a non-nil pointer")
	}
	for i := 0; i < ev.Len(); i++ {
		idx := ev.Index(i)
		if idx.IsNil() {
			continue
		}
		if e := idx.Elem(); e.Type().AssignableTo(tv.Type()) {
			tv.Elem().Set(e.Elem())
			return true
		}
	}
	return false
}

// UnsupportedTypeError describes an unsupported type error.
type UnsupportedTypeError struct {
	schema.Type
}

func (e UnsupportedTypeError) Error() string {
	return fmt.Sprintf("unsupported type %T", e.Type)
}

// CommentDiff computes the comment diff between the 2 attribute list.
// Note that, the implementation relies on the fact that both PostgreSQL
// and MySQL treat empty comment as "no comment" and a way to clear comments.
func CommentDiff(from, to []schema.Attr) schema.Change {
	var fromC, toC schema.Comment
	switch fromHas, toHas := Has(from, &fromC), Has(to, &toC); {
	case !fromHas && !toHas:
	case !fromHas && toC.Text != "":
		return &schema.AddAttr{
			A: &toC,
		}
	case !toHas:
		// In MySQL, there is no way to DROP a comment. Instead, setting it to empty ('')
		// will remove it from INFORMATION_SCHEMA. We use the same approach in PostgreSQL,
		// because comments can be dropped either by setting them to NULL or empty string.
		// See: postgres/backend/commands/comment.c#CreateComments.
		return &schema.ModifyAttr{
			From: &fromC,
			To:   &toC,
		}
	default:
		v1, err1 := Unquote(fromC.Text)
		v2, err2 := Unquote(toC.Text)
		if err1 == nil && err2 == nil && v1 != v2 {
			return &schema.ModifyAttr{
				From: &fromC,
				To:   &toC,
			}
		}
	}
	return nil
}

// CheckDiffMode is like CheckDiff, but compares also expressions
// if the schema.DiffMode is equal to schema.DiffModeNormalized.
func CheckDiffMode(from, to *schema.Table, mode schema.DiffMode, compare ...func(c1, c2 *schema.Check) bool) []schema.Change {
	if !mode.Is(schema.DiffModeNormalized) {
		return checksSimilarDiff(from, to, compare...)
	}
	return ChecksDiff(from, to, func(c1, c2 *schema.Check) bool {
		if len(compare) == 1 && !compare[0](c1, c2) {
			return false
		}
		return c1.Expr == c2.Expr || MayWrap(c1.Expr) == MayWrap(c2.Expr)
	})
}

// ChecksDiff computes the change diff between the 2 tables.
func ChecksDiff(from, to *schema.Table, compare func(c1, c2 *schema.Check) bool) []schema.Change {
	var (
		changes    []schema.Change
		fromC, toC = checks(from.Attrs), checks(to.Attrs)
		compareTo  = func(c1 *schema.Check) func(c2 *schema.Check) bool {
			return func(c2 *schema.Check) bool {
				if c1.Name != "" && c2.Name != "" {
					// Only compare by name if both have a name.
					return c1.Name == c2.Name
				}
				return compare(c1, c2)
			}
		}
	)
	for _, c1 := range fromC {
		switch idx := slices.IndexFunc(toC, compareTo(c1)); {
		case idx == -1:
			changes = append(changes, &schema.DropCheck{
				C: c1,
			})
		case !compare(c1, toC[idx]):
			changes = append(changes, &schema.ModifyCheck{
				From: c1,
				To:   toC[idx],
			})
		}
	}
	for _, c1 := range toC {
		if !slices.ContainsFunc(fromC, compareTo(c1)) {
			changes = append(changes, &schema.AddCheck{
				C: c1,
			})
		}
	}
	return changes
}

// checksSimilarDiff computes the change diff between the 2 tables.
// Unlike ChecksDiff, it does not compare the constraint name, but
// determines if there is any similar constraint by its expression.
// This is an old implementation that is not used anymore by the CLI.
func checksSimilarDiff(from, to *schema.Table, compare ...func(c1, c2 *schema.Check) bool) []schema.Change {
	var changes []schema.Change
	// Drop or modify checks.
	for _, c1 := range checks(from.Attrs) {
		switch c2, ok := similarCheck(to.Attrs, c1); {
		case !ok:
			changes = append(changes, &schema.DropCheck{
				C: c1,
			})
		case len(compare) == 1 && !compare[0](c1, c2):
			changes = append(changes, &schema.ModifyCheck{
				From: c1,
				To:   c2,
			})
		}
	}
	// Add checks.
	for _, c1 := range checks(to.Attrs) {
		if _, ok := similarCheck(from.Attrs, c1); !ok {
			changes = append(changes, &schema.AddCheck{
				C: c1,
			})
		}
	}
	return changes
}

// checks extracts all constraints from table attributes.
func checks(attr []schema.Attr) (checks []*schema.Check) {
	for i := range attr {
		if c, ok := attr[i].(*schema.Check); ok {
			checks = append(checks, c)
		}
	}
	return checks
}

// similarCheck returns a CHECK by its constraints name or expression.
func similarCheck(attrs []schema.Attr, c *schema.Check) (*schema.Check, bool) {
	var byName, byExpr *schema.Check
	for i := 0; i < len(attrs) && (byName == nil || byExpr == nil); i++ {
		check, ok := attrs[i].(*schema.Check)
		if !ok {
			continue
		}
		if check.Name != "" && check.Name == c.Name {
			byName = check
		}
		if check.Expr == c.Expr || MayWrap(check.Expr) == MayWrap(c.Expr) {
			byExpr = check
		}
	}
	// Give precedence to constraint name.
	if byName != nil {
		return byName, true
	}
	if byExpr != nil {
		return byExpr, true
	}
	return nil, false
}

// Unquote single or double quotes.
func Unquote(s string) (string, error) {
	switch {
	case IsQuoted(s, '"'):
		return strconv.Unquote(s)
	case IsQuoted(s, '\''):
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'"), nil
	default:
		return s, nil
	}
}

// SingleQuote quotes the given string with single quote.
func SingleQuote(s string) (string, error) {
	switch {
	case IsQuoted(s, '\''):
		return s, nil
	case IsQuoted(s, '"'):
		v, err := strconv.Unquote(s)
		if err != nil {
			return "", err
		}
		s = v
		fallthrough
	default:
		return "'" + strings.ReplaceAll(s, "'", "''") + "'", nil
	}
}

// TrimViewExtra trims the extra unnecessary
// characters from the view definition.
func TrimViewExtra(s string) string {
	return strings.Trim(s, " \r\n\t;")
}

// BodyDefChanged reports if the body definition of a function, procedure, view, or
// trigger has changed. There is a small task here that normalizes the indentation,
// which might be added during inspection or by the user.
func BodyDefChanged(from, to string) bool {
	if from == to {
		return false // Exact match.
	}
	if from, to = TrimViewExtra(from), TrimViewExtra(to); from == to {
		return false // Match after trimming.
	}
	noident := func(v string) string {
		var b strings.Builder
		for i, s := range strings.Split(v, "\n") {
			if s = TrimViewExtra(s); s != "" && i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(s)
		}
		return b.String()
	}
	return noident(from) != noident(to)
}

// findView finds the view by its name and its type.
func findView(s *schema.Schema, v1 *schema.View) (*schema.View, bool) {
	if v1.Materialized() {
		return s.Materialized(v1.Name)
	}
	return s.View(v1.Name)
}
