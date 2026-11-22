package sqlserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
)

// inputCheck is what a script's run observes of its inputs. Apply verified
// every input once, at its start, and re-observes an input after each
// upstream writer the plan predicts, so a script with known effects observes
// only after, the inputs its own writes reach, once its body ran: a script
// must not change its own inputs. full observes every input before the body
// (a change there is drift, which makes the plan stale) and after it.
type inputCheck struct {
	full  bool
	after []string
}

// before observes every input of a full check; any other check holds item to
// the snapshot values apply settled it with, without observing.
func (c inputCheck) before(ctx context.Context, opts Options, q queryer, item scripts.Pending) error {
	if !c.full {
		return nil
	}
	if err := validateInputs(item, opts.observe(ctx, newObservationEpoch(q), item.Script)); err != nil {
		return &inputDrift{err}
	}
	return nil
}

// afterRun observes what c checks once item's body ran on q.
func (c inputCheck) afterRun(ctx context.Context, opts Options, q queryer, item scripts.Pending) error {
	if c.full {
		return validateInputs(item, opts.observe(ctx, newObservationEpoch(q), item.Script))
	}
	if len(c.after) == 0 {
		return nil
	}
	observed, err := opts.observeKeys(ctx, q, c.after)
	if err != nil {
		return err
	}
	for _, key := range c.after {
		if observed[key].Error != "" {
			return fmt.Errorf("script %s dependency %s: %s", item.Script.ID, key, observed[key].Error)
		}
		if observed[key].Hash != item.State.Inputs[key] {
			return fmt.Errorf("script %s dependency drift: observed inputs changed (%s)", item.Script.ID, key)
		}
	}
	return nil
}

func (s *session) execute(ctx context.Context, item scripts.Pending, planHash string, check inputCheck) error {
	if item.Script.NonTransactional {
		return s.nonTransactional(ctx, item, planHash, check)
	}
	uncertain, previous, err := s.inProgressState(ctx, item, "commit-unknown")
	if err != nil {
		return err
	}
	if err := recordState(ctx, s.conn, s.opts.Scope, item.Script.ID, uncertain); err != nil {
		return err
	}
	for attempt := 0; attempt < 6; attempt++ {
		err := s.transaction(ctx, item, check)
		var drift *inputDrift
		if errors.As(err, &drift) {
			after, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			return errors.Join(err, restoreState(after, s.conn, s.opts.Scope, item.Script.ID, previous))
		}
		if err == nil {
			if err := s.attempt(ctx, item.Script.ID, planHash, "applied", ""); err != nil {
				return &ExecutionError{Outcome: "applied", Err: err}
			}
			return nil
		}
		var outcome *ExecutionError
		if errors.As(err, &outcome) {
			after, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			return errors.Join(err, s.attempt(after, item.Script.ID, planHash, outcome.Outcome, err.Error()))
		}
		if retryable(err) && attempt < 5 {
			select {
			case <-ctx.Done():
				err = errors.Join(err, context.Cause(ctx))
			case <-time.After(time.Duration(attempt+1) * 250 * time.Millisecond):
				continue
			}
		}
		failed := item.State
		failed.Outcome = "failed"
		failed.LastSuccess = uncertain.LastSuccess
		after, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if recordErr := recordState(after, s.conn, s.opts.Scope, item.Script.ID, failed); recordErr != nil {
			return &ExecutionError{Outcome: "commit-unknown", Err: errors.Join(err, fmt.Errorf("record rollback for %s: %w", item.Script.ID, recordErr))}
		}
		return errors.Join(err, s.attempt(after, item.Script.ID, planHash, "failed", err.Error()))
	}
	return fmt.Errorf("migration retries exhausted")
}

type ExecutionError struct {
	Outcome string
	Err     error
}

// inProgressState is item.State marked outcome, carrying the last success
// forward, and the record it replaces.
func (s *session) inProgressState(ctx context.Context, item scripts.Pending, outcome string) (scripts.State, scripts.State, error) {
	recorded, err := readStates(ctx, s.conn, s.opts.ledger())
	state := item.State
	state.Outcome = outcome
	previous := recorded[item.Script.ID]
	state.LastSuccess = previous.LastSuccess
	if previous.LastSuccess == nil && previous.Hash != "" &&
		(previous.Outcome == "applied" || previous.Version == 1) {
		state.LastSuccess = &previous
	}
	return state, previous, err
}

// inputDrift is a failed pre-run input validation: an input changed, or could
// not be observed, with no predicted upstream write. The script did not run,
// and apply refuses the plan as stale.
type inputDrift struct{ err error }

func (e *inputDrift) Error() string { return e.err.Error() }
func (e *inputDrift) Unwrap() error { return e.err }

func (e *ExecutionError) Error() string { return "script " + e.Outcome + ": " + e.Err.Error() }
func (e *ExecutionError) Unwrap() error { return e.Err }

func (s *session) transaction(ctx context.Context, item scripts.Pending, check inputCheck) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	// Finish rollback synchronously before writing the outcome on this connection.
	tx, err := s.conn.BeginTx(context.WithoutCancel(ctx), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = &ExecutionError{Outcome: "commit-unknown", Err: errors.Join(err, rollbackErr)}
		}
	}()
	if _, err = tx.ExecContext(ctx, "SET LOCK_TIMEOUT 15000"); err != nil {
		return err
	}
	if err = check.before(ctx, s.opts, tx, item); err != nil {
		return err
	}
	if err = runBatches(ctx, tx, item.Script, func(ctx context.Context) error {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT @@TRANCOUNT").Scan(&count); err != nil {
			return &ExecutionError{Outcome: "commit-unknown", Err: fmt.Errorf("check script transaction: %w", err)}
		}
		if count != 1 {
			return &ExecutionError{Outcome: "commit-unknown", Err: fmt.Errorf("script changed the migration transaction count to %d", count)}
		}
		return nil
	}); err != nil {
		return err
	}
	if err = check.afterRun(ctx, s.opts, tx, item); err != nil {
		return err
	}
	committed := item.State
	committed.Outcome = "applied"
	if err = recordState(ctx, tx, s.opts.Scope, item.Script.ID, committed); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return &ExecutionError{Outcome: "commit-unknown", Err: fmt.Errorf("script %s commit: %w", item.Script.ID, err)}
	}
	return nil
}

func validateInputs(item scripts.Pending, observed map[string]scripts.Observation) error {
	want, actual := map[string]string{}, map[string]string{}
	for key, value := range item.State.Inputs {
		if !strings.HasPrefix(key, "script:") {
			want[key] = value
		}
	}
	for key, value := range observed {
		if value.Error != "" {
			return fmt.Errorf("script %s dependency %s: %s", item.Script.ID, key, value.Error)
		}
		actual[key] = value.Hash
	}
	if scripts.InputHash(want) != scripts.InputHash(actual) {
		return fmt.Errorf("script %s dependency drift: observed inputs changed", item.Script.ID)
	}
	return nil
}

func runBatches(ctx context.Context, db sessionDB, s scripts.Script, check func(context.Context) error) error {
	batches, err := Batches(s.ID, s.SQL)
	if err != nil {
		return err
	}
	for i, batch := range batches {
		if _, err := db.ExecContext(ctx, batch); err != nil {
			return fmt.Errorf("script %s batch %d: %w", s.ID, i+1, err)
		}
		if check != nil {
			if err := check(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *session) nonTransactional(ctx context.Context, item scripts.Pending, planHash string, check inputCheck) error {
	if err := check.before(ctx, s.opts, s.conn, item); err != nil {
		return err
	}
	state, _, err := s.inProgressState(ctx, item, "partial")
	if err != nil {
		return err
	}
	if err := recordState(ctx, s.conn, s.opts.Scope, item.Script.ID, state); err != nil {
		return err
	}
	if err := runBatches(ctx, s.conn, item.Script, nil); err != nil {
		return &ExecutionError{Outcome: "partial", Err: errors.Join(err, s.attempt(context.WithoutCancel(ctx), item.Script.ID, planHash, "partial", err.Error()))}
	}
	if err := check.afterRun(ctx, s.opts, s.conn, item); err != nil {
		return &ExecutionError{Outcome: "partial", Err: err}
	}
	state.Outcome = "applied"
	state.LastSuccess = nil
	if err := recordState(ctx, s.conn, s.opts.Scope, item.Script.ID, state); err != nil {
		return &ExecutionError{Outcome: "partial", Err: err}
	}
	if err := s.attempt(ctx, item.Script.ID, planHash, "applied", ""); err != nil {
		return &ExecutionError{Outcome: "applied", Err: err}
	}
	return nil
}

func (s *session) attempt(ctx context.Context, id, planHash, outcome, detail string) error {
	_, err := s.conn.ExecContext(ctx, `INSERT INTO dbo.sqlglot_migration_attempts(scope,path,plan_hash,outcome,detail) VALUES(@p1,@p2,@p3,@p4,@p5)`, s.opts.Scope, id, planHash, outcome, detail)
	return err
}
