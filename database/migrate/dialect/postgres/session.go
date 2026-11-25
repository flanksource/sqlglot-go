package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/sqlglot-go/migrate/engine"
)

const (
	// migrationLock is the advisory lock every migration unit holds, so two
	// units never plan or apply against the same schema at once.
	migrationLock = 7561364747649
	// defaultLockTimeout bounds lock waits when the caller sets none.
	defaultLockTimeout = 5 * time.Second
	cleanupTimeout     = 5 * time.Second
)

func lockTimeoutSQL(timeout time.Duration, local bool) string {
	if timeout == 0 {
		timeout = defaultLockTimeout
	}
	scope := ""
	if local {
		scope = "LOCAL "
	}
	return fmt.Sprintf("SET %slock_timeout = '%dms'", scope, max(1, timeout.Milliseconds()))
}

// Open starts a unit. A transactional unit is one transaction whose lock
// timeout and advisory lock end with it. A non-transactional unit has no such
// scope, so it takes a session-level advisory lock and sets the connection's
// lock timeout, and Finish releases the lock and puts the timeout back.
func (backend) Open(ctx context.Context, conn *sql.Conn, opts engine.SessionOptions) (engine.UnitSession, error) {
	if opts.Transactional {
		return engine.TxSession(ctx, conn, func(ctx context.Context, q engine.Querier) error {
			if _, err := q.ExecContext(ctx, lockTimeoutSQL(opts.LockTimeout, true)); err != nil {
				return fmt.Errorf("set lock timeout: %w", err)
			}
			if _, err := q.ExecContext(ctx, fmt.Sprintf("SELECT pg_advisory_xact_lock(%d)", migrationLock)); err != nil {
				return fmt.Errorf("acquire migration lock: %w", err)
			}
			return nil
		})
	}
	return openConnection(ctx, conn, opts)
}

func openConnection(ctx context.Context, conn *sql.Conn, opts engine.SessionOptions) (engine.UnitSession, error) {
	var previous string
	if err := conn.QueryRowContext(ctx, "SELECT current_setting('lock_timeout')").Scan(&previous); err != nil {
		return engine.UnitSession{}, fmt.Errorf("read lock timeout: %w", err)
	}
	restoreTimeout := func(ctx context.Context) error {
		_, err := conn.ExecContext(ctx, "SELECT set_config('lock_timeout', $1, false)", previous)
		return err
	}
	if _, err := conn.ExecContext(ctx, lockTimeoutSQL(opts.LockTimeout, false)); err != nil {
		return engine.UnitSession{}, fmt.Errorf("set lock timeout: %w", err)
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("SELECT pg_advisory_lock(%d)", migrationLock)); err != nil {
		return engine.UnitSession{}, errors.Join(fmt.Errorf("acquire migration lock: %w", err), discardOnFailure(ctx, conn, restoreTimeout))
	}
	return engine.UnitSession{Q: conn, Finish: func(ctx context.Context, _ bool) error {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		return discardOnFailure(cleanup, conn, func(ctx context.Context) error {
			var released bool
			if err := conn.QueryRowContext(ctx, fmt.Sprintf("SELECT pg_advisory_unlock(%d)", migrationLock)).Scan(&released); err != nil {
				return fmt.Errorf("release migration lock: %w", err)
			}
			if !released {
				return errors.New("release migration lock: the session did not hold it")
			}
			return restoreTimeout(ctx)
		})
	}}, nil
}

// discardOnFailure runs cleanup; when it fails the connection still carries a
// lock or a changed setting, so it is marked bad and the pool closes it
// instead of handing it to the next caller.
func discardOnFailure(ctx context.Context, conn *sql.Conn, cleanup func(context.Context) error) error {
	err := cleanup(ctx)
	if err == nil {
		return nil
	}
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	return err
}

func (backend) Exec(ctx context.Context, q engine.Querier, s engine.Statement) error {
	_, err := q.ExecContext(ctx, s.SQL)
	return err
}
