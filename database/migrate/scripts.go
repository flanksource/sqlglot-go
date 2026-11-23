package migrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/flanksource/commons/logger"
	sharedscripts "github.com/flanksource/sqlglot-go/database/migrate/scripts"
	"github.com/lib/pq"
	mssql "github.com/microsoft/go-mssqldb"
)

const (
	// migrationLockTimeout bounds how long a transactional migration waits for a
	// table lock before Postgres cancels it (55P03). This turns an indefinite
	// block behind a live reader (e.g. a running server SELECT-ing the same
	// captain_* tables) into a retryable timeout instead of a hang or deadlock.
	migrationLockTimeout = "15s"
	// migrationMaxAttempts is the total number of times a transactional migration
	// is tried before giving up when it keeps hitting lock contention.
	migrationMaxAttempts = 6
	// migrationRetryBackoff is the base delay between retries; it grows linearly
	// with the attempt number so a busy server gets progressively longer gaps.
	migrationRetryBackoff = 250 * time.Millisecond
)

type scriptPhase string

const (
	phasePre  scriptPhase = "pre"
	phasePost scriptPhase = "post"
)

type script struct {
	id            string
	name          string
	path          string
	content       string
	phase         scriptPhase
	dependsOn     []string
	always        bool
	transactional bool
	hash          []byte
}

// WithScriptSources sets the metadata for scripts keyed by bundle-relative SQL path.
func WithScriptSources(sources map[string]sharedscripts.Source) Option {
	return func(o *options) { o.scriptSources = sources }
}

func loadScripts(schemaFS fs.FS, cfg options) (map[string]*script, error) {
	loaded, err := sharedscripts.Load(sharedscripts.LoadOptions{
		FS: schemaFS, Dir: cfg.dir, Skip: []string{cfg.routines}, Sources: cfg.scriptSources,
	})
	if err != nil {
		return nil, err
	}
	result := map[string]*script{}
	for _, item := range loaded {
		result[item.ID] = projectScript(item)
	}
	return result, nil
}

func parseScript(name, content string) (*script, error) {
	parsed, err := sharedscripts.Parse(name, content)
	if err != nil {
		return nil, err
	}
	return projectScript(parsed), nil
}

func projectScript(parsed sharedscripts.Script) *script {
	s := &script{id: parsed.ID, name: parsed.Name, path: parsed.Path, content: parsed.SQL, phase: scriptPhase(parsed.Phase), transactional: !parsed.NonTransactional, dependsOn: parsed.DependsOn, always: parsed.Always}
	sum := sha256.Sum256([]byte(parsed.SQL))
	s.hash = sum[:]
	return s
}

func topologicalScripts(items map[string]*script, selected map[string]bool) ([]*script, error) {
	ordered, err := sharedscripts.Ordered(scriptBundle(items))
	if err != nil {
		return nil, err
	}
	result := []*script{}
	for _, item := range ordered {
		if selected == nil || selected[item.ID] {
			result = append(result, items[item.ID])
		}
	}
	return result, nil
}

func ensureMetadataTables(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migration_scripts (
  scope text NOT NULL,
  path text NOT NULL,
  hash bytea NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (scope, path)
);
CREATE TABLE IF NOT EXISTS schema_migration_security (
  scope text PRIMARY KEY,
  state jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
)`)
	if err != nil {
		return fmt.Errorf("create migration metadata tables: %w", err)
	}
	return nil
}

func selectScripts(ctx context.Context, db *sql.DB, scope string, scripts map[string]*script) (_ map[string]bool, returnErr error) {
	rows, err := db.QueryContext(ctx, `SELECT path, hash FROM schema_migration_scripts WHERE scope = $1`, scope)
	if err != nil {
		return nil, fmt.Errorf("read SQL migration hashes: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	hashes := map[string][]byte{}
	for rows.Next() {
		var name string
		var hash []byte
		if err := rows.Scan(&name, &hash); err != nil {
			return nil, err
		}
		hashes[name] = hash
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return selectFromRecordedHashes(scripts, hashes), nil
}

// selectFromRecordedHashes decides which scripts run. Hash-dirty scripts —
// genuine content changes — cascade to their dependents (a dependent may bind
// objects the changed script redefines). `runs: always` scripts are
// re-executed every Apply but must NOT cascade: their execution is free by
// contract (CREATE OR REPLACE), while their dependents replay heavyweight DDL
// (DROP/CREATE TRIGGER takes ACCESS EXCLUSIVE) that deadlocks concurrent
// readers when an Apply runs against a live database. An always script's hash
// is still recorded, so editing its content cascades exactly once.
func selectFromRecordedHashes(scripts map[string]*script, hashes map[string][]byte) map[string]bool {
	dirty := map[string]bool{}
	for name, s := range scripts {
		dirty[name] = !equalBytes(hashes[name], s.hash)
	}
	changed := true
	for changed {
		changed = false
		for name, s := range scripts {
			if dirty[name] {
				continue
			}
			for _, dep := range s.dependsOn {
				if dirty[dep] {
					dirty[name] = true
					changed = true
					break
				}
			}
		}
	}
	selected := map[string]bool{}
	for name, s := range scripts {
		selected[name] = s.always || dirty[name]
	}
	return selected
}

type scriptPhaseOptions struct {
	Scope   string
	Ordered []*script
	Phase   scriptPhase
}

func runScriptPhase(ctx context.Context, db *sql.DB, opts scriptPhaseOptions) error {
	for _, s := range opts.Ordered {
		if s.phase != opts.Phase {
			continue
		}
		if s.transactional {
			if err := runTransactionalScript(ctx, db, opts.Scope, s); err != nil {
				return err
			}
			continue
		}
		if _, err := db.ExecContext(ctx, s.content); err != nil {
			return fmt.Errorf("execute non-transactional SQL migration %s: %w", s.path, err)
		}
		if err := recordScript(ctx, db, opts.Scope, s); err != nil {
			return err
		}
	}
	return nil
}

// retryOnLockContention runs fn, retrying on transient lock contention (a
// detected deadlock, a lock_timeout, or a serialization failure) with linear
// backoff. A DDL statement (a transactional script, a dependent-view DROP, or a
// security reconciliation) takes ACCESS EXCLUSIVE on tables a concurrently
// running process may be reading; the bounded lock_timeout converts the
// resulting hang into a retryable 55P03, and a deadlock (40P01) aborts one party
// — either way retrying lets the migration win once the reader's short query
// completes, instead of failing the whole run. fn MUST be idempotent.
func retryOnLockContention(ctx context.Context, desc string, fn func() error) error {
	var lastErr error
	for attempt := 1; attempt <= migrationMaxAttempts; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		if !isRetryableMigrationErr(err) {
			return err
		}
		lastErr = err
		if attempt < migrationMaxAttempts {
			logger.Debugf("%s hit lock contention (attempt %d/%d): %v", desc, attempt, migrationMaxAttempts, err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * migrationRetryBackoff):
			}
		}
	}
	return fmt.Errorf("gave up after %d attempts under lock contention: %w", migrationMaxAttempts, lastErr)
}

// runTransactionalScript applies one transactional migration under a bounded
// lock_timeout, retrying on lock contention. The view/trigger scripts split out
// of the historical 50_views_and_triggers monolith are the canonical DDL that
// contends with live readers.
func runTransactionalScript(ctx context.Context, db *sql.DB, scope string, s *script) error {
	return retryOnLockContention(ctx, "execute SQL migration "+s.path, func() error {
		return applyTransactionalScript(ctx, db, scope, s)
	})
}

// applyTransactionalScript runs the script (and records it) in a single
// transaction with a bounded lock_timeout so it never blocks indefinitely.
func applyTransactionalScript(ctx context.Context, db *sql.DB, scope string, s *script) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin SQL migration %s: %w", s.path, err)
	}
	if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout = '"+migrationLockTimeout+"'"); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("set lock_timeout for SQL migration %s: %w", s.path, err)
	}
	if _, err := tx.ExecContext(ctx, s.content); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("execute SQL migration %s: %w", s.path, err)
	}
	if err := recordScript(ctx, tx, scope, s); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit SQL migration %s: %w", s.path, err)
	}
	return nil
}

// isRetryableMigrationErr reports whether err is transient lock contention that a
// retry can clear: a detected deadlock, a lock_timeout, or a serialization
// failure. A wrapped *pq.Error carries the SQLSTATE that classifies it.
func isRetryableMigrationErr(err error) bool {
	var mssqlErr mssql.Error
	if errors.As(err, &mssqlErr) {
		return mssqlErr.Number == 1205 || // deadlock victim
			mssqlErr.Number == 1222 // LOCK_TIMEOUT exceeded
	}
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) {
		return false
	}
	switch pqErr.Code {
	case "40P01", // deadlock_detected
		"55P03", // lock_not_available (lock_timeout fired)
		"40001": // serialization_failure
		return true
	default:
		return false
	}
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func recordScript(ctx context.Context, db execer, scope string, s *script) error {
	_, err := db.ExecContext(ctx, `
INSERT INTO schema_migration_scripts(scope, path, hash, updated_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (scope, path) DO UPDATE SET hash = EXCLUDED.hash, updated_at = now()`, scope, s.id, s.hash)
	if err != nil {
		return fmt.Errorf("record SQL migration %s: %w", s.path, err)
	}
	return nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
