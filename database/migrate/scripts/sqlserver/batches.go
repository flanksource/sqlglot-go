package sqlserver

import (
	"fmt"
	"strings"

	"github.com/flanksource/sqlglot-go/sqlglot"
)

// Batches splits plain T-SQL at GO and rejects unsupported sqlcmd directives.
func Batches(name, content string) ([]string, error) {
	batches, problem := splitBatches(content)
	if problem != "" {
		return nil, batchError(name, problem)
	}
	return batches, nil
}

// splitBatches is Batches without the script name: problem is the text that
// follows the name in the error, so it can be cached per body.
func splitBatches(content string) ([]string, string) {
	for i, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, ":") || strings.HasPrefix(trimmed, "!!") {
			return nil, fmt.Sprintf(" line %d: sqlcmd command %q is not supported; scripts are plain T-SQL batches separated by GO", i+1, trimmed)
		}
		if strings.Contains(line, "$(") {
			return nil, fmt.Sprintf(" line %d: sqlcmd scripting variables ($(...)) are not supported; sqlcmd would substitute them", i+1)
		}
	}
	batches, err := sqlglot.SplitBatches(content, "tsql")
	if err != nil {
		return nil, ": " + err.Error()
	}
	if len(batches) == 0 {
		return nil, " has no statements"
	}
	return batches, ""
}

func batchError(name, problem string) error {
	return fmt.Errorf("SQL script %s%s", name, problem)
}
