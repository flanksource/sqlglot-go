package sqlserver

import (
	"fmt"
	"regexp"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate/internal/sqlx"
)

var collationLiteral = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

func writeCollation(b *sqlx.Builder, name string) error {
	if !collationLiteral.MatchString(name) {
		return fmt.Errorf("sqlserver: invalid collation literal %q", name)
	}
	b.P("COLLATE", name)
	return nil
}

func supportsCollation(t schema.Type) bool {
	_, ok := t.(*schema.StringType)
	return ok
}
