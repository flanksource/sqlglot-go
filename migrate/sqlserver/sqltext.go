package sqlserver

import (
	"strings"

	"github.com/flanksource/sqlglot-go/migrate"
)

func bit(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func quote(identifier string) string { return "[" + strings.ReplaceAll(identifier, "]", "]]") + "]" }

func qualified(name migrate.Name) string { return quote(name.Schema) + "." + quote(name.Name) }

func literal(text string) string { return "N'" + strings.ReplaceAll(text, "'", "''") + "'" }
