// Package history keeps every inspection, comparison, patch and migration
// sqlglot runs, with its result, in a local SQLite file, so a past run can be
// reviewed with every database it touched offline. It is also clicky's durable
// task RunStore, so a run's task progress and logs outlive the process.
package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Kind string

const (
	KindInspection Kind = "inspection"
	KindComparison Kind = "comparison"
	KindPatch      Kind = "patch"
	KindMigration  Kind = "migration"
)

type Status string

const (
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

// ErrNotFound is returned by Get for an id that was never recorded.
var ErrNotFound = errors.New("history record not found")

// Record is one finished run and its result. Its ID is the run's clicky group
// id, which also keys the run's task snapshots. An inspection that belongs to a
// comparison names it as Parent; Reference is empty for a single connection.
type Record struct {
	ID        string          `json:"id"`
	Kind      Kind            `json:"kind"`
	Parent    string          `json:"parent,omitempty"`
	Dialect   string          `json:"dialect,omitempty"`
	Reference string          `json:"reference,omitempty"`
	Target    string          `json:"target,omitempty"`
	Schemas   []string        `json:"schemas"`
	Status    Status          `json:"status"`
	Error     string          `json:"error,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
	Summary   map[string]int  `json:"summary"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// Complete sets the record's status from err, and its payload from value; a
// failed run keeps a value too, such as a migration's partial result.
func (r *Record) Complete(value any, err error) error {
	r.Status = StatusSucceeded
	if err != nil {
		r.Status, r.Error = StatusFailed, err.Error()
		for _, dsn := range []string{r.Reference, r.Target} {
			if dsn != "" {
				r.Error = strings.ReplaceAll(r.Error, dsn, Redact(dsn))
			}
		}
	}
	if value == nil {
		return nil
	}
	payload, marshalErr := json.Marshal(value)
	if marshalErr != nil {
		return fmt.Errorf("marshal history record %s: %w", r.ID, marshalErr)
	}
	r.Payload, marshalErr = redactPayload(payload)
	if marshalErr != nil {
		return marshalErr
	}
	return nil
}

// Redact is the form of a connection that may be recorded: a connection://
// reference or a SQLite file path whole, a URL DSN without its password or
// query, and nothing of a keyword DSN, whose credentials cannot be told apart
// from the rest.
func Redact(dsn string) string {
	if strings.HasPrefix(dsn, "connection://") {
		return dsn
	}
	u, err := url.Parse(dsn)
	switch {
	case strings.Contains(dsn, "="):
		if err != nil || u.Scheme == "" || u.Opaque != "" {
			return "(redacted DSN)"
		}
	case err != nil || u.Scheme == "":
		return dsn
	case u.Opaque != "":
		return "(redacted DSN)"
	}
	if u.User != nil {
		u.User = url.User(u.User.Username())
	}
	u.RawQuery, u.Fragment = "", ""
	return u.String()
}

// Filter narrows a listing; Connection matches either side.
type Filter struct {
	Kind       Kind
	Connection string
	Limit      int
}

const defaultLimit = 200

type Store struct{ db *sql.DB }

// DefaultPath is $XDG_DATA_HOME/sqlglot-go/history.db.
func DefaultPath() string {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			panic(fmt.Errorf("resolve the history directory: %w", err))
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "sqlglot-go", "history.db")
}

// Open creates the history file and its tables when missing. The CLI and the
// ui server may write it at once, hence WAL and a busy timeout.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("history path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create history directory: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open history %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, errors.Join(fmt.Errorf("create history tables in %s: %w", path, err), db.Close())
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS claims (id TEXT PRIMARY KEY);
CREATE TABLE IF NOT EXISTS records (
	id TEXT PRIMARY KEY, kind TEXT NOT NULL, parent TEXT NOT NULL, dialect TEXT NOT NULL,
	reference TEXT NOT NULL, target TEXT NOT NULL, schemas TEXT NOT NULL, status TEXT NOT NULL,
	error TEXT NOT NULL, created_at TEXT NOT NULL, summary TEXT NOT NULL, payload TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS records_created ON records (created_at DESC);
CREATE TABLE IF NOT EXISTS runs (
	id TEXT PRIMARY KEY, kind TEXT NOT NULL, status TEXT NOT NULL, started_at TEXT NOT NULL,
	meta TEXT NOT NULL, snapshots TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS runs_started ON runs (started_at DESC);`

// Save records a finished run, with its connections redacted. A run is
// recorded once: a second record with the same id is refused.
func (s *Store) Save(ctx context.Context, r Record) error {
	switch {
	case r.ID == "":
		return fmt.Errorf("history record needs an id")
	case r.Kind == "":
		return fmt.Errorf("history record %s needs a kind", r.ID)
	case r.Status == "":
		return fmt.Errorf("history record %s needs a status", r.ID)
	}
	for _, connection := range []*string{&r.Reference, &r.Target} {
		if *connection != "" {
			*connection = Redact(*connection)
		}
	}
	if r.Schemas == nil {
		r.Schemas = []string{}
	}
	if r.Summary == nil {
		r.Summary = map[string]int{}
	}
	schemas, err := json.Marshal(r.Schemas)
	if err != nil {
		return err
	}
	summary, err := json.Marshal(r.Summary)
	if err != nil {
		return err
	}
	exists, err := s.Has(ctx, r.ID)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("history record %s already exists", r.ID)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO records (id, kind, parent, dialect, reference, target, schemas, status, error, created_at, summary, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.ID, r.Kind, r.Parent, r.Dialect, r.Reference, r.Target, string(schemas), r.Status, r.Error,
		r.CreatedAt.UTC().Format(time.RFC3339Nano), string(summary), string(r.Payload))
	if err != nil {
		return fmt.Errorf("save history record %s: %w", r.ID, err)
	}
	return nil
}

func (s *Store) Has(ctx context.Context, id string) (bool, error) {
	var found int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM records WHERE id = ?`, id).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("look up history record %s: %w", id, err)
	}
	return found > 0, nil
}

// Get returns one record with its payload.
func (s *Store) Get(ctx context.Context, id string) (Record, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+recordColumns+`, payload FROM records WHERE id = ?`, id)
	if err != nil {
		return Record{}, fmt.Errorf("read history record %s: %w", id, err)
	}
	records, err := scanRecords(rows, true)
	if err != nil {
		return Record{}, fmt.Errorf("read history record %s: %w", id, err)
	}
	if len(records) == 0 {
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return records[0], nil
}

// List returns matching records newest first, without their payloads.
func (s *Store) List(ctx context.Context, filter Filter) ([]Record, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	query, args := `SELECT `+recordColumns+` FROM records WHERE 1=1`, []any{}
	if filter.Kind != "" {
		query, args = query+` AND kind = ?`, append(args, filter.Kind)
	}
	if filter.Connection != "" {
		connection := Redact(filter.Connection)
		query, args = query+` AND (reference = ? OR target = ?)`, append(args, connection, connection)
	}
	rows, err := s.db.QueryContext(ctx, query+` ORDER BY created_at DESC, id LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("list history: %w", err)
	}
	records, err := scanRecords(rows, false)
	if err != nil {
		return nil, fmt.Errorf("list history: %w", err)
	}
	return records, nil
}

const recordColumns = `id, kind, parent, dialect, reference, target, schemas, status, error, created_at, summary`

func scanRecords(rows *sql.Rows, withPayload bool) (_ []Record, returnErr error) {
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	records := []Record{}
	for rows.Next() {
		var r Record
		var schemas, created, summary, payload string
		dest := []any{&r.ID, &r.Kind, &r.Parent, &r.Dialect, &r.Reference, &r.Target, &schemas, &r.Status, &r.Error, &created, &summary}
		if withPayload {
			dest = append(dest, &payload)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		var err error
		if r.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, fmt.Errorf("record %s created_at: %w", r.ID, err)
		}
		if err := json.Unmarshal([]byte(schemas), &r.Schemas); err != nil {
			return nil, fmt.Errorf("record %s schemas: %w", r.ID, err)
		}
		if err := json.Unmarshal([]byte(summary), &r.Summary); err != nil {
			return nil, fmt.Errorf("record %s summary: %w", r.ID, err)
		}
		if payload != "" {
			r.Payload = json.RawMessage(payload)
		}
		records = append(records, r)
	}
	return records, rows.Err()
}
