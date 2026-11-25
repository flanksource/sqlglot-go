package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

type Outcome string

const (
	Applied        Outcome = "applied"
	AlreadyApplied Outcome = "already-applied"
	Failed         Outcome = "failed"
	CommitUnknown  Outcome = "commit-unknown"
)

type UnitResult struct {
	ID         string  `json:"id"`
	Outcome    Outcome `json:"outcome"`
	Statements int     `json:"statements"`
}

type Result struct {
	Hash    string       `json:"hash"`
	Outcome Outcome      `json:"outcome"`
	Units   []UnitResult `json:"units,omitempty"`
}

type ApplyRequest struct {
	Artifact         Artifact
	Hash             string
	Acknowledgements []string
	LockTimeout      time.Duration
}

// PreconditionError reports a precondition query that returned a row.
type PreconditionError struct {
	Unit    string
	ID      string
	Message string
}

func (e *PreconditionError) Error() string {
	return fmt.Sprintf("engine: precondition %s of unit %s failed: %s", e.ID, e.Unit, e.Message)
}

var errPostcondition = errors.New("database does not match the desired state of the unit")

// ValidateRequest checks the reviewed inputs without touching a database.
func ValidateRequest(req ApplyRequest) error {
	if err := Validate(req.Artifact); err != nil {
		return err
	}
	if req.Hash != req.Artifact.Hash {
		return fmt.Errorf("engine: reviewed hash %q does not match migration artifact %s", req.Hash, req.Artifact.Hash)
	}
	if req.LockTimeout < 0 || req.LockTimeout > math.MaxInt32*time.Millisecond {
		return fmt.Errorf("engine: migration lock timeout must be between zero and %s, got %s", math.MaxInt32*time.Millisecond, req.LockTimeout)
	}
	return ValidateAcknowledgements(req.Artifact, req.Acknowledgements)
}

// Apply runs the artifact's units in order, each in its own transaction (or on
// a dedicated connection when it holds a non-transactional statement). A unit
// already at its desired state is skipped. The first error stops the run;
// earlier units stay committed and are reported, so a re-run resumes.
func Apply(ctx context.Context, db *sql.DB, req ApplyRequest) (Result, error) {
	result := Result{Hash: req.Hash, Outcome: Failed}
	if ctx == nil || db == nil {
		return result, errors.New("engine: artifact execution requires a context and database")
	}
	if err := ValidateRequest(req); err != nil {
		return result, err
	}
	d, err := Lookup(req.Artifact.Dialect)
	if err != nil {
		return result, err
	}
	if err := checkCapabilities(ctx, d, db, req.Artifact); err != nil {
		return result, err
	}
	ran := false
	for _, u := range req.Artifact.Units {
		ur, err := applyUnit(ctx, db, d, req, u)
		result.Units = append(result.Units, ur)
		if err != nil {
			result.Outcome = ur.Outcome
			return result, err
		}
		ran = ran || ur.Outcome == Applied
	}
	result.Outcome = AlreadyApplied
	if ran {
		result.Outcome = Applied
	}
	return result, nil
}

// checkCapabilities refuses an artifact planned against a database that has
// since changed what it can do. An empty fingerprint marks an artifact planned
// offline from two snapshots, which never saw a database, so there is nothing to compare.
func checkCapabilities(ctx context.Context, d Dialect, db *sql.DB, a Artifact) error {
	if a.Capabilities.Fingerprint == "" {
		return nil
	}
	now, err := d.Capabilities(ctx, db)
	if err != nil {
		return fmt.Errorf("engine: read %s capabilities: %w", a.Dialect, err)
	}
	if now.Fingerprint != a.Capabilities.Fingerprint {
		return fmt.Errorf("engine: planned against %q, database now reports %q: re-plan", a.Capabilities.Fingerprint, now.Fingerprint)
	}
	return nil
}

// applyUnit runs one unit on a dedicated connection through the dialect's
// session. Finish is called exactly once: commit=true only after the
// postcondition passed, false on every other path.
func applyUnit(ctx context.Context, db *sql.DB, d Dialect, req ApplyRequest, u Unit) (res UnitResult, err error) {
	res = UnitResult{ID: u.ID, Outcome: Failed}
	conn, err := db.Conn(ctx)
	if err != nil {
		return res, fmt.Errorf("unit %s: open migration connection: %w", u.ID, err)
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("unit %s: close migration connection: %w", u.ID, cerr))
			if res.Outcome == AlreadyApplied {
				res.Outcome = Failed
			}
		}
	}()
	s, err := d.Open(ctx, conn, SessionOptions{LockTimeout: req.LockTimeout, Unit: u.ID, Transactional: u.Transactional})
	if err != nil {
		return res, fmt.Errorf("unit %s: open session: %w", u.ID, err)
	}
	commit, err := runUnit(ctx, d, s.Q, req.Artifact, u, &res)
	if ferr := s.Finish(ctx, commit); ferr != nil {
		if commit {
			res.Outcome = CommitUnknown
			return res, fmt.Errorf("unit %s commit outcome is unknown: %w", u.ID, ferr)
		}
		if res.Outcome == AlreadyApplied {
			res.Outcome = Failed
		}
		return res, errors.Join(err, fmt.Errorf("unit %s: finish session: %w", u.ID, ferr))
	}
	if commit {
		res.Outcome = Applied
	}
	return res, err
}

// runUnit classifies the unit, runs its preconditions and statements, and
// checks the postcondition. It reports whether the work may be committed.
func runUnit(ctx context.Context, d Dialect, q Querier, a Artifact, u Unit, res *UnitResult) (commit bool, err error) {
	state, err := classifyUnit(ctx, d, q, a, u)
	if err != nil {
		return false, err
	}
	if state == UnitApplied {
		res.Outcome = AlreadyApplied
		return false, nil
	}
	if err = runPreconditions(ctx, q, u); err != nil {
		return false, err
	}
	if res.Statements, err = runStatements(ctx, d, q, u); err != nil {
		return false, err
	}
	if state, err = classifyUnit(ctx, d, q, a, u); err != nil || state != UnitApplied {
		return false, fmt.Errorf("unit %s postcondition failed (state %q): %w", u.ID, state, errors.Join(err, errPostcondition))
	}
	return true, nil
}

func runPreconditions(ctx context.Context, q Querier, u Unit) error {
	for _, p := range u.Preconditions {
		failed, err := returnsRows(ctx, q, p.SQL)
		if err != nil {
			return fmt.Errorf("unit %s precondition %s: %w", u.ID, p.ID, err)
		}
		if failed {
			return &PreconditionError{Unit: u.ID, ID: p.ID, Message: p.Message}
		}
	}
	return nil
}

func returnsRows(ctx context.Context, q Querier, query string) (bool, error) {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return false, err
	}
	found := rows.Next()
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return false, err
	}
	return found, nil
}

func runStatements(ctx context.Context, d Dialect, q Querier, u Unit) (int, error) {
	count := 0
	for _, op := range u.Ops {
		for _, s := range op.Statements {
			if err := d.Exec(ctx, q, s); err != nil {
				return count, fmt.Errorf("unit %s op %s: %w", u.ID, op.ID, err)
			}
			count++
		}
	}
	return count, nil
}
