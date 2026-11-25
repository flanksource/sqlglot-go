package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/sqlglot-go/migrate/engine"
)

const (
	// defaultLockTimeout bounds the wait for the write lock when the caller sets none.
	defaultLockTimeout = 5 * time.Second
	cleanupTimeout     = 5 * time.Second
)

// pragmas are the connection settings a unit changes and puts back.
type pragmas struct{ foreignKeys, busyTimeout int }

// Open starts a unit on the connection it owns. Before the transaction it sets
// the busy timeout from the lock timeout and turns foreign key enforcement
// off: a table rebuild drops the old table, which with enforcement on first
// deletes its rows and runs their ON DELETE actions on child tables, and the
// pragma cannot change inside a transaction. The engine does not say whether
// the unit rebuilds a table, so enforcement is off for every unit and the
// foreign keys of the tables it replaced are checked before it commits.
// BEGIN IMMEDIATE then takes the write lock. Finish puts both pragmas back on
// every path.
func (backend) Open(ctx context.Context, conn *sql.Conn, opts engine.SessionOptions) (engine.UnitSession, error) {
	saved, err := readPragmas(ctx, conn)
	if err != nil {
		return engine.UnitSession{}, err
	}
	timeout := opts.LockTimeout
	if timeout == 0 {
		timeout = defaultLockTimeout
	}
	for _, s := range []string{fmt.Sprintf("PRAGMA busy_timeout = %d", max(1, timeout.Milliseconds())), "PRAGMA foreign_keys = off"} {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			return engine.UnitSession{}, errors.Join(fmt.Errorf("%s: %w", s, err), saved.restore(ctx, conn))
		}
	}
	u := &unit{conn: conn, saved: saved, transactional: opts.Transactional}
	if !opts.Transactional {
		return u.session(), nil
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return engine.UnitSession{}, errors.Join(fmt.Errorf("acquire SQLite migration write lock: %w", err), saved.restore(ctx, conn))
	}
	if u.before, err = rootPages(ctx, conn); err != nil {
		return engine.UnitSession{}, errors.Join(err, u.finish(ctx, false))
	}
	return u.session(), nil
}

type unit struct {
	conn          *sql.Conn
	saved         pragmas
	transactional bool
	before        map[string]int
}

func (u *unit) session() engine.UnitSession {
	return engine.UnitSession{Q: u.conn, Finish: u.finish}
}

// finish commits (after the foreign key check) or rolls back, then restores
// the pragmas. A connection that could not be put back is discarded, so the
// pool never hands it on with this unit's settings.
func (u *unit) finish(ctx context.Context, commit bool) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	var verdict, broken error
	if u.transactional {
		verdict, broken = u.endTransaction(cleanup, commit)
	}
	broken = errors.Join(broken, u.saved.restore(cleanup, u.conn))
	if broken != nil {
		_ = u.conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	return errors.Join(verdict, broken)
}

// endTransaction returns why the unit could not commit (verdict) apart from a
// failure to leave the connection clean (broken).
func (u *unit) endTransaction(ctx context.Context, commit bool) (verdict, broken error) {
	if !commit {
		return nil, u.rollback(ctx)
	}
	if err := u.checkReplaced(ctx); err != nil {
		return err, u.rollback(ctx)
	}
	if _, err := u.conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit migration transaction: %w", err), u.rollback(ctx)
	}
	return nil, nil
}

func (u *unit) rollback(ctx context.Context) error {
	if _, err := u.conn.ExecContext(ctx, "ROLLBACK"); err != nil && !isNoTransaction(err) {
		return fmt.Errorf("rollback migration transaction: %w", err)
	}
	return nil
}

func isNoTransaction(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "no transaction is active"))
}

// checkReplaced checks the foreign keys of every table whose b-tree was
// replaced (a rebuild creates a new one) or created in the unit, so a rebuild
// that leaves a dangling reference fails the unit instead of committing it.
func (u *unit) checkReplaced(ctx context.Context) error {
	after, err := rootPages(ctx, u.conn)
	if err != nil {
		return err
	}
	for table, page := range after {
		if before, ok := u.before[table]; ok && before == page {
			continue
		}
		if err := checkForeignKeys(ctx, u.conn, table); err != nil {
			return err
		}
	}
	return nil
}

func (backend) Exec(ctx context.Context, q engine.Querier, s engine.Statement) error {
	_, err := q.ExecContext(ctx, s.SQL)
	return err
}

func readPragmas(ctx context.Context, conn *sql.Conn) (pragmas, error) {
	var p pragmas
	if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&p.foreignKeys); err != nil {
		return p, fmt.Errorf("read PRAGMA foreign_keys: %w", err)
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&p.busyTimeout); err != nil {
		return p, fmt.Errorf("read PRAGMA busy_timeout: %w", err)
	}
	return p, nil
}

func (p pragmas) restore(ctx context.Context, conn *sql.Conn) error {
	_, ferr := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA foreign_keys = %d", p.foreignKeys))
	_, berr := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", p.busyTimeout))
	if err := errors.Join(ferr, berr); err != nil {
		return fmt.Errorf("restore SQLite pragmas: %w", err)
	}
	return nil
}

func rootPages(ctx context.Context, conn *sql.Conn) (_ map[string]int, returnErr error) {
	rows, err := conn.QueryContext(ctx, "SELECT name, rootpage FROM sqlite_schema WHERE type = 'table'")
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	pages := map[string]int{}
	for rows.Next() {
		var name string
		var page int
		if err := rows.Scan(&name, &page); err != nil {
			return nil, err
		}
		pages[name] = page
	}
	return pages, rows.Err()
}

func checkForeignKeys(ctx context.Context, conn *sql.Conn, table string) (returnErr error) {
	rows, err := conn.QueryContext(ctx, fmt.Sprintf("PRAGMA foreign_key_check(%s)", quoteLiteral(table)))
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	if rows.Next() {
		return fmt.Errorf("foreign key check failed: table %q has rows that reference rows that do not exist", table)
	}
	return rows.Err()
}

func quoteLiteral(s string) string {
	out := "'"
	for _, r := range s {
		if r == '\'' {
			out += "'"
		}
		out += string(r)
	}
	return out + "'"
}
