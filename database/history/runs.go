package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	clickytask "github.com/flanksource/clicky/task"
)

var _ clickytask.RunStore = (*Store)(nil)

func (s *Store) Claim(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("run id is required")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO claims (id) SELECT ? WHERE NOT EXISTS (SELECT 1 FROM records WHERE id = ?) ON CONFLICT DO NOTHING`, id, id)
	if err != nil {
		return fmt.Errorf("reserve run %s: %w", id, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("run %s already exists", id)
	}
	return nil
}

// SaveRun upserts a run's snapshots keyed by its clicky group id; clicky writes
// a run when it finishes and again when it is evicted, and the newest wins.
func (s *Store) SaveRun(ctx context.Context, groupID string, snapshots []clickytask.TaskSnapshot) error {
	record, ok := clickytask.RunFromSnapshots(snapshots)
	if !ok {
		return fmt.Errorf("run %s carries no group snapshot", groupID)
	}
	meta, err := json.Marshal(record.RunMeta)
	if err != nil {
		return fmt.Errorf("marshal run %s: %w", record.ID, err)
	}
	payload, err := json.Marshal(snapshots)
	if err != nil {
		return fmt.Errorf("marshal snapshots of run %s: %w", record.ID, err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO runs (id, kind, status, started_at, meta, snapshots) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET kind = excluded.kind, status = excluded.status, started_at = excluded.started_at, meta = excluded.meta, snapshots = excluded.snapshots`,
		record.ID, record.Kind, record.Status, record.StartedAt, string(meta), string(payload))
	if err != nil {
		return fmt.Errorf("save run %s: %w", record.ID, err)
	}
	return nil
}

// Runs lists stored runs matching filter, newest first.
func (s *Store) Runs(ctx context.Context, filter clickytask.RunFilter) (_ []clickytask.RunMeta, returnErr error) {
	rows, err := s.db.QueryContext(ctx, `SELECT meta FROM runs ORDER BY started_at DESC LIMIT ?`, defaultLimit)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	runs := []clickytask.RunMeta{}
	for rows.Next() {
		var text string
		var meta clickytask.RunMeta
		if err := rows.Scan(&text); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(text), &meta); err != nil {
			return nil, fmt.Errorf("decode stored run: %w", err)
		}
		if filter.Matches(meta) {
			runs = append(runs, meta)
		}
	}
	return runs, rows.Err()
}

// Snapshot returns a stored run's snapshots, or none for an unknown id.
func (s *Store) Snapshot(ctx context.Context, id string) ([]clickytask.TaskSnapshot, error) {
	var text string
	err := s.db.QueryRowContext(ctx, `SELECT snapshots FROM runs WHERE id = ?`, id).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read run %s: %w", id, err)
	}
	var snapshots []clickytask.TaskSnapshot
	if err := json.Unmarshal([]byte(text), &snapshots); err != nil {
		return nil, fmt.Errorf("decode run %s: %w", id, err)
	}
	return snapshots, nil
}

// Control refuses: a stored run has finished and has no live owner to drive.
func (s *Store) Control(_ context.Context, id string, action clickytask.ControlAction) error {
	return fmt.Errorf("stored run %s cannot be controlled (%s); it has finished", id, action)
}
