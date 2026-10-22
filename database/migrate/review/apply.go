package review

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"ariga.io/atlas/sql/schema"
)

func Apply(ctx context.Context, db *sql.DB, request Request) (_ Result, returnErr error) {
	result := Result{Hash: request.Hash, Outcome: Failed}
	if err := validate(request); err != nil {
		return result, err
	}
	if err := validateScope(request.Artifact.Baseline); err != nil {
		return result, err
	}
	if db == nil {
		return result, fmt.Errorf("database is nil")
	}
	if request.LockTimeout == 0 {
		request.LockTimeout = 5 * time.Second
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return result, err
	}
	defer func() { returnErr = errors.Join(returnErr, conn.Close()) }()
	if request.Artifact.Baseline.Dialect == "sqlite" {
		return applySQLite(ctx, conn, request)
	}
	return applyPostgres(ctx, conn, request)
}

func applyPostgres(ctx context.Context, conn *sql.Conn, request Request) (result Result, err error) {
	result = Result{Hash: request.Hash, Outcome: Failed}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() {
		if e := tx.Rollback(); e != nil && !errors.Is(e, sql.ErrTxDone) {
			err = errors.Join(err, e)
		}
	}()
	if _, err = tx.ExecContext(ctx, fmt.Sprintf("SET LOCAL lock_timeout = '%dms'", max(1, request.LockTimeout.Milliseconds()))); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(7561364747649)"); err != nil {
		return result, fmt.Errorf("acquire migration lock: %w", err)
	}
	result, err = execute(ctx, tx, request)
	if err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		result.Outcome = CommitUnknown
		return result, fmt.Errorf("commit outcome unknown: %w", err)
	}
	return result, nil
}

func execute(ctx context.Context, q schema.ExecQuerier, request Request) (Result, error) {
	result := Result{Hash: request.Hash, Outcome: Failed}
	a := request.Artifact
	b, err := open(q, a.Baseline.Dialect)
	if err != nil {
		return result, err
	}
	baseline, err := b.decode(a.Baseline)
	if err != nil {
		return result, err
	}
	desired, err := b.decode(a.Desired)
	if err != nil {
		return result, err
	}
	current, err := inspect(ctx, q, b, a.Baseline)
	if err != nil {
		return result, err
	}
	remaining, err := b.diff(current, desired)
	if err != nil {
		return result, err
	}
	if len(remaining) == 0 {
		result.Outcome = AlreadyApplied
		return result, nil
	}
	drift, err := b.diff(current, baseline)
	if err != nil {
		return result, err
	}
	if len(drift) > 0 {
		return result, fmt.Errorf("schema drift: current schema matches neither reviewed baseline nor desired state")
	}
	steps, err := b.plan(ctx, baseline, desired)
	if err != nil {
		return result, err
	}
	if !sameSteps(steps, a.Steps) {
		return result, fmt.Errorf("saved SQL differs from the current backend planner; review a fresh plan")
	}
	for _, step := range a.Steps {
		if _, err := q.ExecContext(ctx, step.SQL); err != nil {
			return result, fmt.Errorf("execute step %s: %w", step.ID, err)
		}
		result.Steps++
	}
	current, err = inspect(ctx, q, b, a.Desired)
	if err != nil {
		return result, err
	}
	remaining, err = b.diff(current, desired)
	if err != nil {
		return result, err
	}
	if len(remaining) > 0 {
		return result, fmt.Errorf("postcondition failed: %d schema differences remain", len(remaining))
	}
	result.Outcome = Applied
	return result, nil
}
