package sqlserver

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	mssql "github.com/microsoft/go-mssqldb"
)

// session is one apply: a dedicated connection holding the
// scope's migration lock for its whole run.
type session struct {
	conn     *sql.Conn
	opts     Options
	lockName string
	closed   bool
}

// open locks the scope of prepared options on a dedicated connection.
func open(ctx context.Context, opts Options) (*session, error) {
	conn, err := opts.DB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	lockName := "sqlglot/scripts:" + scripts.Digest(opts.Scope)
	var code int
	err = conn.QueryRowContext(ctx, `DECLARE @r int; EXEC @r=sys.sp_getapplock @Resource=@p1,@LockMode='Exclusive',@LockOwner='Session',@LockTimeout=60000; SELECT @r`, lockName).Scan(&code)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("acquire migration lock: %w", err), discardConnection(conn))
	}
	if code < 0 {
		return nil, errors.Join(fmt.Errorf("acquire migration lock: SQL Server returned code %d", code), discardConnection(conn))
	}
	return &session{conn: conn, opts: opts, lockName: lockName}, nil
}

func (s *session) Close() error {
	if s.closed {
		return fmt.Errorf("migration session already closed")
	}
	s.closed = true
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := s.conn.ExecContext(ctx, `EXEC sys.sp_releaseapplock @Resource=@p1,@LockOwner='Session'`, s.lockName)
	// Arbitrary script SET options must never reach another pooled borrower.
	return errors.Join(err, discardConnection(s.conn))
}

func discardConnection(conn *sql.Conn) error {
	discardErr := conn.Raw(func(any) error { return driver.ErrBadConn })
	if errors.Is(discardErr, driver.ErrBadConn) || errors.Is(discardErr, sql.ErrConnDone) {
		discardErr = nil
	}
	closeErr := conn.Close()
	if errors.Is(closeErr, sql.ErrConnDone) {
		closeErr = nil
	}
	return errors.Join(discardErr, closeErr)
}

// verified re-plans file's bundle on the session connection and returns that
// fresh plan, refusing a file for another target or one the target moved
// away from.
func (s *session) verified(ctx context.Context, file PlanFile) (PlanFile, error) {
	fresh, err := plan(ctx, s.conn, s.opts)
	if err != nil {
		return fresh, err
	}
	if fresh.Target != file.Target {
		return fresh, fmt.Errorf("plan targets %s, but the connection is %s", file.Target, fresh.Target)
	}
	if moved := file.moved(fresh); len(moved) > 0 {
		return fresh, &StalePlanError{Moved: moved}
	}
	return fresh, nil
}

// options are the prepared options of file's bundle on db.
func (file PlanFile) options(ctx context.Context, db *sql.DB, cfg applyConfig) (Options, error) {
	if err := file.Verify(); err != nil {
		return Options{}, err
	}
	return prepare(ctx, Options{DB: db, Scope: file.Target.Scope, absorbed: file.Absorbed, Scripts: file.Bundle.Scripts, Functions: file.Bundle.Functions,
		Schema: file.Bundle.Schema, Analysis: cfg.analysis, AnalysisTTL: cfg.analysisTTL})
}

func retryable(err error) bool {
	var serverErr mssql.Error
	return errors.As(err, &serverErr) && (serverErr.Number == 1205 || serverErr.Number == 1222)
}
