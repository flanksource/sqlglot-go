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

type Adoption struct {
	Preview  Preview            `json:"preview"`
	Evidence []AdoptionEvidence `json:"evidence"`
	Hash     string             `json:"hash"`
}

type AdoptionOptions struct {
	Adoption Adoption
	Verify   func(context.Context, Connection) error
}

func AdoptionPlan(preview Preview, evidence []AdoptionEvidence) (Adoption, error) {
	if preview.Plan.Hash == "" || len(evidence) == 0 {
		return Adoption{}, fmt.Errorf("adoption requires a preview and explicit evidence")
	}
	seen := map[string]bool{}
	sourceHashes := make(map[string]string, len(preview.Plan.Scripts))
	for _, item := range preview.Plan.Scripts {
		sourceHashes[item.Script.ID] = item.State.SourceHash
	}
	for _, e := range evidence {
		if e.ID == "" || seen[e.ID] || e.SourceHash == "" || e.Provenance == "" || e.Expected == "" {
			return Adoption{}, fmt.Errorf("invalid or duplicate adoption evidence for %s", e.ID)
		}
		if sourceHashes[e.ID] != e.SourceHash {
			return Adoption{}, fmt.Errorf("adoption evidence source mismatch for %s", e.ID)
		}
		if err := validateQuery(e.Query); err != nil {
			return Adoption{}, err
		}
		seen[e.ID] = true
	}
	result := Adoption{Preview: preview, Evidence: evidence}
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

// Adopt records independently verified current state without executing script bodies.
func (s *Session) Adopt(ctx context.Context, opts AdoptionOptions) (err error) {
	approved := opts.Adoption
	canonical, err := AdoptionPlan(approved.Preview, approved.Evidence)
	if err != nil {
		return err
	}
	if canonical.Hash != approved.Hash {
		return fmt.Errorf("adoption artifact hash mismatch")
	}
	fresh, err := s.Inspect(ctx, approved.Preview.Plan.Phase)
	if err != nil {
		return err
	}
	if fresh.Plan.Hash != approved.Preview.Plan.Hash {
		return fmt.Errorf("adoption source, history or inputs changed; review again")
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
	if opts.Verify != nil {
		if err = opts.Verify(ctx, tx); err != nil {
			return err
		}
	}
	for _, e := range approved.Evidence {
		var item *scripts.Pending
		for i := range fresh.Plan.Scripts {
			if fresh.Plan.Scripts[i].Script.ID == e.ID {
				item = &fresh.Plan.Scripts[i]
			}
		}
		if item == nil || item.State.SourceHash != e.SourceHash {
			return fmt.Errorf("adoption evidence source mismatch for %s", e.ID)
		}
		for _, block := range item.Blocked {
			if block != "adoption-required" {
				return fmt.Errorf("adoption dependency unavailable for %s: %s", e.ID, block)
			}
		}
		var value sql.NullString
		rows, queryErr := tx.QueryContext(ctx, e.Query)
		if queryErr != nil {
			return queryErr
		}
		columns, queryErr := rows.Columns()
		if queryErr != nil {
			_ = rows.Close()
			return queryErr
		}
		if len(columns) != 1 || !rows.Next() {
			_ = rows.Close()
			return fmt.Errorf("postcondition for %s must return one scalar row", e.ID)
		}
		if queryErr = rows.Scan(&value); queryErr != nil {
			_ = rows.Close()
			return queryErr
		}
		extra := rows.Next() || rows.NextResultSet()
		queryErr = errors.Join(rows.Err(), rows.Close())
		if queryErr != nil {
			return queryErr
		}
		if extra || !value.Valid || value.String != e.Expected {
			return fmt.Errorf("adoption postcondition mismatch for %s", e.ID)
		}
		if err = validateInputs(*item, observe(ctx, tx, item.Script, s.opts.Functions)); err != nil {
			return err
		}
		state := item.State
		state.Outcome = "adopted"
		state.EvidenceHash, state.Provenance = scripts.Digest(e.Query, e.Expected, e.SourceHash), e.Provenance
		if err = recordState(ctx, tx, s.opts.Scope, e.ID, state); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return &ExecutionError{Outcome: "commit-unknown", Err: err}
	}
	return nil
}
