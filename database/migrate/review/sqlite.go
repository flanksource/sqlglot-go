package review

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"
)

func applySQLite(ctx context.Context, conn *sql.Conn, request Request) (result Result, err error) {
	result = Result{Hash: request.Hash, Outcome: Failed}
	var foreignKeys, busyTimeout int
	if err = conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return result, err
	}
	if err = conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		return result, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, foreignErr := conn.ExecContext(cleanup, fmt.Sprintf("PRAGMA foreign_keys = %d", foreignKeys))
		_, busyErr := conn.ExecContext(cleanup, fmt.Sprintf("PRAGMA busy_timeout = %d", busyTimeout))
		if foreignErr != nil || busyErr != nil {
			err = errors.Join(err, fmt.Errorf("restore SQLite pragmas: %w", errors.Join(foreignErr, busyErr)))
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if _, err = conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", max(1, request.LockTimeout.Milliseconds()))); err != nil {
		return result, err
	}
	if _, err = conn.ExecContext(ctx, "PRAGMA foreign_keys = off"); err != nil {
		return result, err
	}
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return result, fmt.Errorf("acquire SQLite migration write lock: %w", err)
	}
	finished := false
	defer func() {
		if finished {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, rollbackErr := conn.ExecContext(cleanup, "ROLLBACK")
		if rollbackErr != nil {
			err = errors.Join(err, fmt.Errorf("rollback SQLite migration: %w", rollbackErr))
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if err = foreignKeyCheck(ctx, conn); err != nil {
		return result, err
	}
	result, err = execute(ctx, conn, request)
	if err != nil {
		return result, err
	}
	if err = foreignKeyCheck(ctx, conn); err != nil {
		result.Outcome = Failed
		return result, err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		result.Outcome = CommitUnknown
		return result, fmt.Errorf("commit outcome unknown: %w", err)
	}
	finished = true
	return result, nil
}

func foreignKeyCheck(ctx context.Context, conn *sql.Conn) (returnErr error) {
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	if rows.Next() {
		return fmt.Errorf("SQLite foreign key check failed")
	}
	return rows.Err()
}
