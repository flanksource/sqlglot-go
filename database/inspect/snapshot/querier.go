package snapshot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Querier is implemented by a database, pinned connection or transaction.
type Querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// requireViewDefinition fails unless the connection sees every definition, so
// hidden metadata cannot look like absent objects.
func requireViewDefinition(ctx context.Context, db Querier) error {
	var permission int
	rows, err := db.QueryContext(ctx, "SELECT COALESCE(HAS_PERMS_BY_NAME(DB_NAME(), 'DATABASE', 'VIEW DEFINITION'), 0)")
	if err != nil {
		return fmt.Errorf("inspect migration permissions: %w", err)
	}
	if rows.Next() {
		err = rows.Scan(&permission)
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("inspect migration permissions: %w", err)
	}
	if permission != 1 {
		return fmt.Errorf("complete migration inspection requires database VIEW DEFINITION permission")
	}
	return nil
}
