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
	if err := validateInputs(item, opts.observe(ctx, newObservationEpoch(q), item.Script).inputs); err != nil {
		return &inputDrift{err}
	}
	return nil
}

// afterRun observes what c checks once item's body ran on q.
func (c inputCheck) afterRun(ctx context.Context, opts Options, q queryer, item scripts.Pending) error {
	if c.full {
		return validateInputs(item, opts.observe(ctx, newObservationEpoch(q), item.Script).inputs)
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
	uncertain, previous := s.inProgressState(item, "commit-unknown")
	marked := s.mayEndTransaction(item)
	if marked {
		if err := recordState(ctx, s.conn, s.opts.Scope, item.Script.ID, uncertain); err != nil {
			return err
		}
	}
	for attempt := 0; attempt < 6; attempt++ {
		err := s.transaction(ctx, item, planHash, check)
		var drift *inputDrift
		if errors.As(err, &drift) {
			if !marked {
				return err
			}
			after, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			return errors.Join(err, restoreState(after, s.conn, s.opts.Scope, item.Script.ID, previous))
		}
		if err == nil {
			if err := s.recordReads(ctx, item); err != nil {
				return &ExecutionError{Outcome: "applied", Err: err}
			}
			return nil
		}
		var execution *ExecutionError
		if errors.As(err, &execution) {
			after, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if execution.committing {
				return errors.Join(err, s.attempt(after, item.Script.ID, planHash, execution.Outcome, err.Error()))
			}
			return errors.Join(err, s.record(after, outcome{script: item.Script.ID, planHash: planHash, detail: err.Error(), state: uncertain}))
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
		if recordErr := s.record(after, outcome{script: item.Script.ID, planHash: planHash, detail: err.Error(), state: failed}); recordErr != nil {
			return &ExecutionError{Outcome: "commit-unknown", Err: errors.Join(err, fmt.Errorf("record rollback for %s: %w", item.Script.ID, recordErr))}
		}
		return err
	}
	return fmt.Errorf("migration retries exhausted")
}

// mayEndTransaction reports whether running item may end the migration's
// transaction, which makes its writes outlive a failure of its ledger row. Only
// such a script is marked commit-unknown before it runs: any other commits
// with its ledger row, so the row alone says whether it landed.
func (s *session) mayEndTransaction(item scripts.Pending) bool {
	a := s.opts.analysis[item.Script.ID]
	return a.TransactionControl || a.Dynamic || len(a.Blocked) > 0 || len(a.Calls) > 0 || len(item.EffectsUnknown) > 0
}

// recordReads adds what the reads of item answer, now that it ran, to its
// record: what a later plan compares. One that fails is not recorded.
func (s *session) recordReads(ctx context.Context, item scripts.Pending) error {
	reads := s.opts.analysis[item.Script.ID].Reads
	if len(reads) == 0 {
		return nil
	}
	queries := make([]string, len(reads))
	for i, read := range reads {
		queries[i] = read.SQL
	}
	answers, err := answerReads(ctx, s.conn, queries)
	if err != nil {
		return fmt.Errorf("record what script %s reads: %w", item.Script.ID, err)
	}
	state := item.State
	state.Outcome, state.Reads = "applied", map[string]scripts.ReadAnswer{}
	for _, read := range reads {
		if answer := answers[read.SQL]; answer.Error == "" {
			state.Reads[read.Key()] = answer
		}
	}
	if err := recordState(ctx, s.conn, s.opts.Scope, item.Script.ID, state); err != nil {
		return err
	}
	s.states[item.Script.ID] = state
	return nil
}

type ExecutionError struct {
	Outcome string
	Err     error
	// committing is set when the commit itself failed: the ledger row, which
	// commits with the script, says whether it landed.
	committing bool
}

// inProgressState is item.State marked outcome, carrying the last success
// forward, and the record it replaces.
func (s *session) inProgressState(item scripts.Pending, outcome string) (scripts.State, scripts.State) {
	state := item.State
	state.Outcome = outcome
	previous := s.states[item.Script.ID]
	state.LastSuccess = previous.LastSuccess
	if previous.LastSuccess == nil && previous.Hash != "" &&
		(previous.Outcome == "applied" || previous.Version == 1) {
		state.LastSuccess = &previous
	}
	return state, previous
}

// inputDrift is a failed pre-run input validation: an input changed, or could
// not be observed, with no predicted upstream write. The script did not run,
// and apply refuses the plan as stale.
type inputDrift struct{ err error }

func (e *inputDrift) Error() string { return e.err.Error() }
func (e *inputDrift) Unwrap() error { return e.err }

func (e *ExecutionError) Error() string { return "script " + e.Outcome + ": " + e.Err.Error() }
func (e *ExecutionError) Unwrap() error { return e.Err }

// transaction runs item and records its success in one transaction, the record
// in the same request as item's last batch. The commit is a request of its own:
// a failed statement does not stop the batch it is in, so what follows it runs
// whether or not the script succeeded.
func (s *session) transaction(ctx context.Context, item scripts.Pending, planHash string, check inputCheck) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	done := outcome{script: item.Script.ID, planHash: planHash, state: item.State}
	done.state.Outcome = "applied"
	completion, err := s.completion(done)
	if err != nil {
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
	if err = check.before(ctx, s.opts, tx, item); err != nil {
		return err
	}
	if err = s.runTransactional(ctx, tx, item.Script, completion); err != nil {
		return err
	}
	if err = check.afterRun(ctx, s.opts, tx, item); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return &ExecutionError{Outcome: "commit-unknown", committing: true, Err: fmt.Errorf("script %s commit: %w", item.Script.ID, err)}
	}
	s.states[item.Script.ID] = done.state
	return nil
}

// runTransactional runs the batches of script on tx, each followed by a check
// that the migration's transaction is open and the last by completion.
func (s *session) runTransactional(ctx context.Context, tx *sql.Tx, script scripts.Script, completion string) error {
	batches, err := Batches(script.ID, script.SQL)
	if err != nil {
		return err
	}
	tails := s.opts.analysis[script.ID].Tails
	if len(tails) != len(batches) {
		return fmt.Errorf("script %s has %d batches, its analysis %d", script.ID, len(batches), len(tails))
	}
	for i, batch := range batches {
		after, last := countCheck("<1")+";", i == len(batches)-1
		if last {
			after = completion
		}
		for _, statement := range request(batch, tails[i], after, last) {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return countError(fmt.Errorf("script %s batch %d: %w", script.ID, i+1, err))
			}
		}
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

// runBatches runs each batch of s on db.
func runBatches(ctx context.Context, db sessionDB, s scripts.Script) error {
	batches, err := Batches(s.ID, s.SQL)
	if err != nil {
		return err
	}
	for i, batch := range batches {
		if _, err := db.ExecContext(ctx, batch); err != nil {
			return fmt.Errorf("script %s batch %d: %w", s.ID, i+1, err)
		}
	}
	return nil
}

func (s *session) nonTransactional(ctx context.Context, item scripts.Pending, planHash string, check inputCheck) error {
	if err := check.before(ctx, s.opts, s.conn, item); err != nil {
		return err
	}
	state, _ := s.inProgressState(item, "partial")
	if err := recordState(ctx, s.conn, s.opts.Scope, item.Script.ID, state); err != nil {
		return err
	}
	if err := runBatches(ctx, s.conn, item.Script); err != nil {
		return &ExecutionError{Outcome: "partial", Err: errors.Join(err, s.attempt(context.WithoutCancel(ctx), item.Script.ID, planHash, "partial", err.Error()))}
	}
	if err := check.afterRun(ctx, s.opts, s.conn, item); err != nil {
		return &ExecutionError{Outcome: "partial", Err: err}
	}
	state.Outcome = "applied"
	state.LastSuccess = nil
	if err := s.record(ctx, outcome{script: item.Script.ID, planHash: planHash, state: state}); err != nil {
		return &ExecutionError{Outcome: "partial", Err: err}
	}
	if err := s.recordReads(ctx, item); err != nil {
		return &ExecutionError{Outcome: "applied", Err: err}
	}
	return nil
}

func (s *session) attempt(ctx context.Context, id, planHash, outcome, detail string) error {
	_, err := s.conn.ExecContext(ctx, `INSERT INTO dbo.sqlglot_migration_attempts(scope,path,plan_hash,outcome,detail) VALUES(@p1,@p2,@p3,@p4,@p5)`, s.opts.Scope, id, planHash, outcome, detail)
	return err
}
