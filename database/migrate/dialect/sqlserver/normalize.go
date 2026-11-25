package sqlserver

import (
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate"
)

// quote and qualified spell identifiers the way the T-SQL the dialect renders does.
func quote(identifier string) string { return "[" + strings.ReplaceAll(identifier, "]", "]]") + "]" }

func qualified(n migrate.Name) string { return quote(n.Schema) + "." + quote(n.Name) }

func quoteAll(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = quote(n)
	}
	return strings.Join(quoted, ", ")
}

// normalizeFillFactor maps the two spellings of the server default (0 and 100)
// to 0, so an index re-created without a fill factor reads back the same.
func normalizeFillFactor(percent int) int {
	if percent == 100 {
		return 0
	}
	return percent
}

// normalizeAction turns a catalog referential action (NO_ACTION) into the
// spelling the planner renders (NO ACTION), as the inspector does.
func normalizeAction(desc string) string { return strings.ReplaceAll(desc, "_", " ") }

// normalizeExpression strips the whitespace around an expression the catalog
// returned; the server already stores its own canonical text.
func normalizeExpression(text string) string { return strings.TrimSpace(text) }

// sortedColumns keeps a set of column names in a fixed order.
func sortedColumns(names []string) []string {
	out := slices.Clone(names)
	slices.Sort(out)
	return out
}

// compressionOf reads an index's data compression from its partitions (the
// least and greatest description, and how many there are): the one ROW or PAGE
// (or none) they all share. Mixed, per-partition or columnstore compression is
// returned as a reason the index cannot be re-created faithfully.
func compressionOf(least, most string, partitions int) (compression, refusal string) {
	if least != most {
		return "", fmt.Sprintf("has mixed per-partition data compression (%s to %s)", least, most)
	}
	if partitions > 1 {
		return "", fmt.Sprintf("has %d partitions", partitions)
	}
	switch least {
	case "", "NONE":
		return "", ""
	case "ROW", "PAGE":
		return least, ""
	}
	return "", "uses " + least + " data compression"
}

// filegroupOf returns the filegroup an index must be re-created on, empty for
// the database's default filegroup. A partition scheme or other data space is a refusal.
func filegroupOf(spaceType, name string, isDefault bool) (filegroup, refusal string) {
	switch spaceType {
	case "":
		return "", ""
	case "FG":
		if isDefault {
			return "", ""
		}
		return name, ""
	case "PS":
		return "", "is partitioned by " + name
	}
	return "", fmt.Sprintf("is on %s data space %s", spaceType, name)
}
