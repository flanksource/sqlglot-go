package sqlserver

import (
	"fmt"
	"strings"

	"github.com/flanksource/sqlglot-go/sqlglot"
)

// Batches splits plain T-SQL at GO and rejects unsupported sqlcmd directives.
func Batches(name, content string) ([]string, error) {
	for i, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, ":") || strings.HasPrefix(trimmed, "!!") {
			return nil, fmt.Errorf("SQL script %s line %d: sqlcmd command %q is not supported; scripts are plain T-SQL batches separated by GO", name, i+1, trimmed)
		}
		if strings.Contains(line, "$(") {
			return nil, fmt.Errorf("SQL script %s line %d: sqlcmd scripting variables ($(...)) are not supported; sqlcmd would substitute them", name, i+1)
		}
	}
	batches, err := sqlglot.SplitBatches(content, "tsql")
	if err != nil {
		return nil, fmt.Errorf("SQL script %s: %w", name, err)
	}
	if len(batches) == 0 {
		return nil, fmt.Errorf("SQL script %s has no statements", name)
	}
	return batches, nil
}
