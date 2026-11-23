package sqlserver

import (
	"errors"
	"fmt"
	"strings"

	mssql "github.com/microsoft/go-mssqldb"
)

type scriptBatchError struct {
	script string
	batch  int
	err    error
}

func (e *scriptBatchError) Error() string {
	return fmt.Sprintf("script %s batch %d: %v", e.script, e.batch, e.err)
}

func (e *scriptBatchError) Unwrap() error { return e.err }

func isPrimaryKeyViolation(err error) bool {
	var batch *scriptBatchError
	if !errors.As(err, &batch) {
		return false
	}
	cause := batch.err
	for errors.Unwrap(cause) != nil {
		cause = errors.Unwrap(cause)
	}
	var server mssql.Error
	if _, joined := cause.(interface{ Unwrap() []error }); joined || !errors.As(cause, &server) {
		return false
	}
	messages := server.All
	if len(messages) == 0 {
		messages = []mssql.Error{server}
	}
	found := false
	for _, message := range messages {
		if message.Number == 3621 { // The statement was terminated.
			continue
		}
		text := strings.ToLower(message.Message)
		if message.Number != 2627 || !strings.HasPrefix(text, "violation of primary key constraint") ||
			strings.Contains(text, "dbo.sqlglot_migration") || message.Class >= 20 {
			return false
		}
		found = true
	}
	return found
}
