package sqlserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/flanksource/sqlglot-go/migrate/engine"
	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
)

// executeBatch nests each statement in sp_executesql so module DDL is first in
// its batch and the session options are set for it.
const executeBatch = `EXEC sys.sp_executesql N'` + planner.SessionOptions + ` EXEC sys.sp_executesql @ddl;', N'@ddl nvarchar(max)', @ddl=@p1`

const (
	xactAbortOption = 16384
	lockOwnerTx     = "Transaction"
	lockOwnerConn   = "Session"
)

// Open starts one unit on its connection: the transaction when the unit has
// one, the application lock, SET LOCK_TIMEOUT and SET XACT_ABORT ON. A zero lock
// timeout means no waiting, for the application lock and for blocked statements.
// Finish ends the transaction, releases the lock and puts the connection's
// settings back, so the pooled connection is clean.
func (Dialect) Open(ctx context.Context, conn *sql.Conn, opts engine.SessionOptions) (engine.UnitSession, error) {
	if opts.LockTimeout < 0 || opts.LockTimeout > math.MaxInt32*time.Millisecond {
		return engine.UnitSession{}, fmt.Errorf("sqlserver: lock timeout must be between zero and %s, got %s", math.MaxInt32*time.Millisecond, opts.LockTimeout)
	}
	saved, err := saveSettings(ctx, conn)
	if err != nil {
		return engine.UnitSession{}, err
	}
	var s engine.UnitSession
	if opts.Transactional {
		s, err = engine.TxSession(ctx, conn, func(ctx context.Context, q engine.Querier) error { return lockAndSet(ctx, q, opts, lockOwnerTx) })
	} else {
		s, err = openConnection(ctx, conn, opts)
	}
	if err != nil {
		return engine.UnitSession{}, errors.Join(err, saved.restore(ctx, conn))
	}
	inner := s.Finish
	s.Finish = func(ctx context.Context, commit bool) error {
		return errors.Join(inner(ctx, commit), saved.restore(ctx, conn))
	}
	return s, nil
}

// openConnection is a session for a unit that cannot share a transaction: the
// lock is owned by the connection and released by Finish.
func openConnection(ctx context.Context, conn *sql.Conn, opts engine.SessionOptions) (engine.UnitSession, error) {
	if err := lockAndSet(ctx, conn, opts, lockOwnerConn); err != nil {
		return engine.UnitSession{}, err
	}
	return engine.UnitSession{Q: conn, Finish: func(ctx context.Context, _ bool) error {
		_, err := conn.ExecContext(ctx, `EXEC sys.sp_releaseapplock @Resource=N'`+planner.LockResource+`', @LockOwner='`+lockOwnerConn+`'`)
		if err != nil {
			return fmt.Errorf("release migration lock: %w", err)
		}
		return nil
	}}, nil
}

func lockAndSet(ctx context.Context, q engine.Querier, opts engine.SessionOptions, owner string) error {
	var status int
	if err := q.QueryRowContext(ctx, `DECLARE @status int;
		EXEC @status = sys.sp_getapplock @Resource=N'`+planner.LockResource+`',
		@LockMode='Exclusive', @LockOwner='`+owner+`', @LockTimeout=@p1;
		SELECT @status`, opts.LockTimeout.Milliseconds()).Scan(&status); err != nil {
		return fmt.Errorf("lock migration: %w", err)
	}
	if status < 0 {
		return fmt.Errorf("lock migration: sp_getapplock returned %d (timed out, cancelled, deadlock or error)", status)
	}
	if _, err := q.ExecContext(ctx, fmt.Sprintf("SET LOCK_TIMEOUT %d; SET XACT_ABORT ON;", opts.LockTimeout.Milliseconds())); err != nil {
		return fmt.Errorf("set migration session options: %w", err)
	}
	return nil
}

// settings are the connection settings Open changes, as they were.
type settings struct {
	lockTimeout int64
	xactAbort   bool
}

func saveSettings(ctx context.Context, conn *sql.Conn) (settings, error) {
	var s settings
	var options int
	if err := conn.QueryRowContext(ctx, "SELECT CAST(@@LOCK_TIMEOUT AS bigint), @@OPTIONS").Scan(&s.lockTimeout, &options); err != nil {
		return s, fmt.Errorf("read connection settings: %w", err)
	}
	s.xactAbort = options&xactAbortOption != 0
	return s, nil
}

func (s settings) restore(ctx context.Context, conn *sql.Conn) error {
	abort := "OFF"
	if s.xactAbort {
		abort = "ON"
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("SET LOCK_TIMEOUT %d; SET XACT_ABORT %s;", s.lockTimeout, abort)); err != nil {
		return fmt.Errorf("restore connection settings: %w", err)
	}
	return nil
}

// Exec runs one statement of a plan in a nested batch.
func (Dialect) Exec(ctx context.Context, q engine.Querier, s engine.Statement) error {
	_, err := q.ExecContext(ctx, executeBatch, s.SQL)
	return err
}
