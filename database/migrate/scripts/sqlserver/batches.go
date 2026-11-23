package sqlserver

import (
	"fmt"

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

// splitBatches keeps the error independent of the source path for caching.
func splitBatches(content string) ([]string, string) {
	batches, err := sqlglot.SplitSQLServerScript(content)
	if err != nil {
		return nil, err.Error()
	}
	return batches, ""
}

func batchError(name, problem string) error {
	return fmt.Errorf("SQL script %s: %s", name, problem)
}
