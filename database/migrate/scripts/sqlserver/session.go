package sqlserver

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	mssql "github.com/microsoft/go-mssqldb"
)

type Session struct {
	conn       *sql.Conn
	opts       Options
	bundleHash string
	lockName   string
	closed     bool
}

func Open(ctx context.Context, opts Options) (*Session, error) {
	opts, hash, err := prepare(opts)
	if err != nil {
		return nil, err
	}
	conn, err := opts.DB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	lockName := "sqlglot/scripts:" + scripts.Digest(opts.Scope)
	var code int
	err = conn.QueryRowContext(ctx, `DECLARE @r int; EXEC @r=sys.sp_getapplock @Resource=@p1,@LockMode='Exclusive',@LockOwner='Session',@LockTimeout=60000; SELECT @r`, lockName).Scan(&code)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("acquire migration lock: %w", err), conn.Close())
	}
	if code < 0 {
		return nil, errors.Join(fmt.Errorf("acquire migration lock: SQL Server returned code %d", code), conn.Close())
	}
	return &Session{conn: conn, opts: opts, bundleHash: hash, lockName: lockName}, nil
}

func (s *Session) Close() error {
	if s.closed {
		return fmt.Errorf("migration session already closed")
	}
	s.closed = true
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := s.conn.ExecContext(ctx, `EXEC sys.sp_releaseapplock @Resource=@p1,@LockOwner='Session'`, s.lockName)
	// Arbitrary script SET options must never reach another pooled borrower.
	discardErr := s.conn.Raw(func(any) error { return driver.ErrBadConn })
	if errors.Is(discardErr, driver.ErrBadConn) || errors.Is(discardErr, sql.ErrConnDone) {
		discardErr = nil
	}
	closeErr := s.conn.Close()
	if errors.Is(closeErr, sql.ErrConnDone) {
		closeErr = nil
	}
	return errors.Join(err, discardErr, closeErr)
}

func (s *Session) Inspect(ctx context.Context, phase scripts.Phase) (Preview, error) {
	if s.closed {
		return Preview{}, fmt.Errorf("migration session closed")
	}
	return inspect(ctx, s.conn, s.opts, phase, s.bundleHash)
}

type ApplyOptions struct {
	Preview                     Preview
	AcknowledgeNonTransactional bool
}

type Result struct {
	Outcome      string   `json:"outcome"`
	Applied      []string `json:"applied"`
	Continuation *Preview `json:"continuation,omitempty"`
}

func (s *Session) Apply(ctx context.Context, opts ApplyOptions) (Result, error) {
	result := Result{Outcome: "already-applied", Applied: []string{}}
	current, err := s.Inspect(ctx, opts.Preview.Plan.Phase)
	if err != nil {
		return result, err
	}
	if current.Plan.Hash != opts.Preview.Plan.Hash || current.BundleHash != opts.Preview.BundleHash {
		if alreadyApplied(opts.Preview, current) {
			return result, nil
		}
		result.Outcome, result.Continuation = "continuation-required", &current
		return result, nil
	}
	for _, item := range current.Plan.Scripts {
		if item.Selected && item.Script.NonTransactional && !opts.AcknowledgeNonTransactional {
			return result, fmt.Errorf("script %s requires nontransactional acknowledgement", item.Script.ID)
		}
	}
	selected := false
	for _, item := range current.Plan.Scripts {
		selected = selected || item.Selected
	}
	if selected {
		if err := ensureStore(ctx, s.conn); err != nil {
			return result, err
		}
	}
	for _, approved := range current.Plan.Scripts {
		if !approved.Selected {
			continue
		}
		fresh, err := s.Inspect(ctx, current.Plan.Phase)
		if err != nil {
			return result, err
		}
		var item scripts.Pending
		for _, candidate := range fresh.Plan.Scripts {
			if candidate.Script.ID == approved.Script.ID {
				item = candidate
			}
		}
		if item.State.Hash != approved.State.Hash || len(item.Blocked) > 0 {
			result.Outcome, result.Continuation = "continuation-required", &fresh
			return result, nil
		}
		if err := s.execute(ctx, item, current.Plan.Hash); err != nil {
			result.Outcome = "failed"
			var execution *ExecutionError
			if errors.As(err, &execution) {
				result.Outcome = execution.Outcome
				if execution.Outcome == "applied" {
					result.Applied = append(result.Applied, item.Script.ID)
				}
			}
			return result, err
		}
		result.Applied = append(result.Applied, item.Script.ID)
		result.Outcome = "applied"
	}
	remaining, err := s.Inspect(ctx, current.Plan.Phase)
	if err != nil {
		return result, err
	}
	for _, item := range remaining.Plan.Scripts {
		if (item.Selected || len(item.Blocked) > 0) && !slices.Contains(result.Applied, item.Script.ID) {
			result.Outcome, result.Continuation = "continuation-required", &remaining
			break
		}
	}
	return result, nil
}

func alreadyApplied(approved, current Preview) bool {
	if approved.BundleHash != current.BundleHash || approved.Plan.Target != current.Plan.Target || len(approved.Plan.Scripts) != len(current.Plan.Scripts) {
		return false
	}
	for i, item := range current.Plan.Scripts {
		before := approved.Plan.Scripts[i]
		if item.Script.ID != before.Script.ID || item.State.Hash != before.State.Hash || item.Selected || len(item.Blocked) > 0 {
			return false
		}
	}
	return true
}

func retryable(err error) bool {
	var serverErr mssql.Error
	return errors.As(err, &serverErr) && (serverErr.Number == 1205 || serverErr.Number == 1222)
}
