package sqlserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	"github.com/flanksource/sqlglot-go/sqlglot"
)

type AdoptionEvidence struct {
	ID         string `json:"id"`
	SourceHash string `json:"sourceHash"`
	Provenance string `json:"provenance"`
	Query      string `json:"query"`
	Expected   string `json:"expected"`
}

// Adoption is a plan file and the evidence that the target already holds the
// effect of some of its scripts.
type Adoption struct {
	Plan     PlanFile           `json:"plan"`
	Evidence []AdoptionEvidence `json:"evidence"`
	Hash     string             `json:"hash"`
}

type AdoptionOptions struct {
	Adoption Adoption
	Verify   func(context.Context, Connection) error
}

// AdoptionPlan binds evidence to the scripts of plan it adopts.
func AdoptionPlan(plan PlanFile, evidence []AdoptionEvidence) (Adoption, error) {
	if err := plan.Verify(); err != nil {
		return Adoption{}, err
	}
	if len(evidence) == 0 {
		return Adoption{}, fmt.Errorf("adoption requires explicit evidence")
	}
	seen := map[string]bool{}
	for _, e := range evidence {
		if e.ID == "" || seen[e.ID] || e.SourceHash == "" || e.Provenance == "" || e.Expected == "" {
			return Adoption{}, fmt.Errorf("invalid or duplicate adoption evidence for %s", e.ID)
		}
		if item, ok := plan.action(e.ID); !ok || item.State.SourceHash != e.SourceHash {
			return Adoption{}, fmt.Errorf("adoption evidence source mismatch for %s", e.ID)
		}
		if err := validateQuery(e.Query); err != nil {
			return Adoption{}, err
		}
		seen[e.ID] = true
	}
	result := Adoption{Plan: plan, Evidence: evidence}
	content, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	result.Hash = scripts.SourceHash(string(content))
	return result, nil
}

func validateQuery(query string) error {
	tree, err := sqlglot.ParseOne(query, "tsql")
	if err != nil {
		return fmt.Errorf("adoption postcondition: %w", err)
	}
	if tree.Class != "Select" || tree.Args["into"] != nil {
		return fmt.Errorf("adoption postcondition must be one read-only SELECT without INTO")
	}
	var unsupported string
	tree.Walk(func(node *sqlglot.Expression) bool {
		if node.Class == "NextValueFor" {
			unsupported = "NEXT VALUE FOR"
		}
		return true
	})
	if unsupported != "" {
		return fmt.Errorf("adoption postcondition cannot use %s", unsupported)
	}
	return nil
}

// Adopt records independently verified current state without executing script
// bodies. It re-plans the adoption's plan file like Apply, refusing it as
// stale when the target moved; no script body runs, so each adopted script
// records the inputs that re-plan observed.
func Adopt(ctx context.Context, db *sql.DB, opts AdoptionOptions, options ...ApplyOption) (returnErr error) {
	approved := opts.Adoption
	canonical, err := AdoptionPlan(approved.Plan, approved.Evidence)
	if err != nil {
		return err
	}
	if canonical.Hash != approved.Hash {
		return fmt.Errorf("adoption artifact hash mismatch")
	}
	prepared, err := approved.Plan.options(ctx, db, resolveApply(options))
	if err != nil {
		return err
	}
	s, err := open(ctx, prepared)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, s.Close()) }()
	return s.adopt(ctx, approved, opts.Verify)
}

func (s *session) adopt(ctx context.Context, approved Adoption, verify func(context.Context, Connection) error) (err error) {
	fresh, err := s.verified(ctx, approved.Plan)
	if err != nil {
		return err
	}
	if err := ensureStore(ctx, s.conn); err != nil {
		return err
	}
	tx, err := s.conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				err = &ExecutionError{Outcome: "commit-unknown", Err: errors.Join(err, rollbackErr)}
			}
		}
	}()
	if verify != nil {
		if err = verify(ctx, tx); err != nil {
			return err
		}
	}
	for _, e := range approved.Evidence {
		if err = s.adoptOne(ctx, tx, fresh, e); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return &ExecutionError{Outcome: "commit-unknown", Err: err}
	}
	return nil
}

func (s *session) adoptOne(ctx context.Context, tx *sql.Tx, fresh PlanFile, e AdoptionEvidence) error {
	item, ok := fresh.action(e.ID)
	if !ok || item.State.SourceHash != e.SourceHash {
		return fmt.Errorf("adoption evidence source mismatch for %s", e.ID)
	}
	for _, block := range item.Blocked {
		if block != "adoption-required" {
			return fmt.Errorf("adoption dependency unavailable for %s: %s", e.ID, block)
		}
	}
	if err := checkPostcondition(ctx, tx, e); err != nil {
		return err
	}
	state := item.State
	state.Outcome = "adopted"
	state.EvidenceHash, state.Provenance = scripts.Digest(e.Query, e.Expected, e.SourceHash), e.Provenance
	return recordState(ctx, tx, s.opts.Scope, e.ID, state)
}

// checkPostcondition runs e's query, which must return exactly e.Expected.
func checkPostcondition(ctx context.Context, tx *sql.Tx, e AdoptionEvidence) error {
	rows, err := tx.QueryContext(ctx, e.Query)
	if err != nil {
		return err
	}
	columns, err := rows.Columns()
	if err != nil {
		return errors.Join(err, rows.Close())
	}
	if len(columns) != 1 || !rows.Next() {
		return errors.Join(fmt.Errorf("postcondition for %s must return one scalar row", e.ID), rows.Close())
	}
	var value sql.NullString
	if err := rows.Scan(&value); err != nil {
		return errors.Join(err, rows.Close())
	}
	extra := rows.Next() || rows.NextResultSet()
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if extra || !value.Valid || value.String != e.Expected {
		return fmt.Errorf("adoption postcondition mismatch for %s", e.ID)
	}
	return nil
}
