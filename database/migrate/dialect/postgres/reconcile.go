package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// Reconcile spells a desired column default like the live one when the
// database says the two expressions are equal. Atlas's own comparison trims
// casts and quotes; when the texts still differ and either side is an
// expression, the connected Atlas driver asks the database (SELECT a = b), and
// so does this: offline, such a pair is always reported as a changed default.
// A pair the database finds different is left alone and stays a change.
func (backend) Reconcile(ctx context.Context, q engine.Querier, cur, des *schema.Schema, keep func(string) bool) (bool, error) {
	changed := false
	for _, dt := range des.Tables {
		ct, ok := cur.Table(dt.Name)
		if !keep(dt.Name) || !ok {
			continue
		}
		for _, dc := range dt.Columns {
			cc, ok := ct.Column(dc.Name)
			if !ok {
				continue
			}
			equal, err := equalDefaults(ctx, q, cc, dc)
			if err != nil {
				return false, fmt.Errorf("compare default of %s.%s: %w", dt.Name, dc.Name, err)
			}
			if equal {
				dc.Default, changed = cc.Default, true
			}
		}
		checked, err := reconcileChecks(ctx, q, ct, dt)
		if err != nil {
			return false, fmt.Errorf("compare checks of %s: %w", dt.Name, err)
		}
		changed = changed || checked
	}
	return changed, nil
}

// equalDefaults reports whether the database finds two differently spelled
// defaults equal. Identical spellings, as Atlas compares them, need no rewrite.
func equalDefaults(ctx context.Context, q engine.Querier, live, desired *schema.Column) (bool, error) {
	from, fromRaw, ok1 := defaultText(live)
	to, toRaw, ok2 := defaultText(desired)
	if !ok1 || !ok2 || trimCast(from) == trimCast(to) || quote(from) == quote(to) || (!fromRaw && !toRaw) {
		return false, nil
	}
	var equal sql.NullBool
	if err := q.QueryRowContext(ctx, fmt.Sprintf("SELECT %s = %s", asSQL(from, fromRaw), asSQL(to, toRaw))).Scan(&equal); err != nil {
		return false, err
	}
	return equal.Valid && equal.Bool, nil
}

func defaultText(c *schema.Column) (text string, raw, ok bool) {
	switch x := schema.UnderlyingExpr(c.Default).(type) {
	case *schema.Literal:
		return x.V, false, true
	case *schema.RawExpr:
		return x.X, true, true
	}
	return "", false, false
}

func asSQL(text string, raw bool) string {
	if raw {
		return text
	}
	return quote(text)
}

func quote(s string) string {
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func trimCast(s string) string {
	i := strings.LastIndex(s, "::")
	if i == -1 {
		return s
	}
	for _, r := range s[i+2:] {
		if r != ' ' && !unicode.IsLetter(r) {
			return s
		}
	}
	return s[:i]
}
