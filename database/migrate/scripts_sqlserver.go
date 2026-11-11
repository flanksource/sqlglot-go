package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/database/atlas/sqlserver"
	planner "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
	"github.com/flanksource/sqlglot-go/sqlglot"
)

// sqlServerMigrationsTable records the hash of every SQL Server script a
// bundle scope has run, so an unchanged script is not run again.
const sqlServerMigrationsTable = "dbo.sqlglot_migrations"

// sqlServerLockTimeoutMs bounds how long a transactional SQL Server script
// waits for a lock before failing with 1222, which retryOnLockContention retries.
const sqlServerLockTimeoutMs = 15000

// sqlServerBundle is what a SQL Server migration declares: the dbo realm of HCL
// tables and routines, and the hash-gated scripts.
type sqlServerBundle struct {
	realm   *schema.Realm
	scripts map[string]*script
}

// loadSQLServerBundle loads the realm and the scripts, splitting each script
// into the batches sqlcmd would send.
func loadSQLServerBundle(schemaFS fs.FS, cfg options) (sqlServerBundle, error) {
	realm, err := sqlServerDesired(schemaFS, cfg)
	if err != nil {
		return sqlServerBundle{}, err
	}
	scripts, err := loadScripts(schemaFS, cfg.dir, cfg.routines)
	if err != nil {
		return sqlServerBundle{}, err
	}
	for _, s := range scripts {
		if s.batches, err = sqlcmdBatches(s.path, s.content); err != nil {
			return sqlServerBundle{}, err
		}
	}
	target := realm.Schemas[0]
	if len(target.Tables)+len(target.Procs)+len(target.Funcs)+len(scripts) == 0 {
		return sqlServerBundle{}, fmt.Errorf("SQL Server migration bundle %q declares no tables, routines or scripts", cfg.name)
	}
	return sqlServerBundle{realm: realm, scripts: scripts}, nil
}

func (b sqlServerBundle) declaresObjects() bool {
	target := b.realm.Schemas[0]
	return len(target.Tables)+len(target.Procs)+len(target.Funcs) > 0
}

// sqlcmdBatches splits a script into the batches sqlcmd sends: plain T-SQL
// separated by GO on its own line. sqlcmd commands (:setvar, :r, :on error,
// !!) and $(var) scripting variables, which sqlcmd substitutes even inside
// strings, are refused, so a script means the same here as in sqlcmd and SSMS.
func sqlcmdBatches(name, content string) ([]string, error) {
	for i, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, ":") || strings.HasPrefix(trimmed, "!!") {
			return nil, fmt.Errorf("SQL script %s line %d: sqlcmd command %q is not supported; scripts are plain T-SQL batches separated by GO", name, i+1, trimmed)
		}
		if strings.Contains(line, "$(") {
			return nil, fmt.Errorf("SQL script %s line %d: sqlcmd scripting variables ($(...)) are not supported; sqlcmd would substitute them", name, i+1)
		}
	}
	split, err := sqlglot.SplitBatches(content, planner.Dialect)
	if err != nil {
		return nil, fmt.Errorf("SQL script %s: %w", name, err)
	}
	batches := make([]string, 0, len(split))
	for _, batch := range split {
		if strings.TrimSpace(batch) != "" {
			batches = append(batches, batch)
		}
	}
	if len(batches) == 0 {
		return nil, fmt.Errorf("SQL script %s has no statements", name)
	}
	return batches, nil
}

// recordedSQLServerHashes reads the scope's recorded script hashes; a missing
// metadata table means none were recorded yet.
func recordedSQLServerHashes(ctx context.Context, db *sql.DB, scope string) (_ map[string][]byte, returnErr error) {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT CASE WHEN OBJECT_ID(N'`+sqlServerMigrationsTable+`', N'U') IS NULL THEN 0 ELSE 1 END`).Scan(&exists); err != nil {
		return nil, fmt.Errorf("find %s: %w", sqlServerMigrationsTable, err)
	}
	hashes := map[string][]byte{}
	if !exists {
		return hashes, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT path, hash FROM `+sqlServerMigrationsTable+` WHERE scope = @p1`, scope)
	if err != nil {
		return nil, fmt.Errorf("read SQL migration hashes: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	for rows.Next() {
		var name string
		var hash []byte
		if err := rows.Scan(&name, &hash); err != nil {
			return nil, err
		}
		hashes[name] = hash
	}
	return hashes, rows.Err()
}

func ensureSQLServerMigrationsTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `IF OBJECT_ID(N'`+sqlServerMigrationsTable+`', N'U') IS NULL
CREATE TABLE `+sqlServerMigrationsTable+` (
  scope NVARCHAR(256) NOT NULL,
  path NVARCHAR(512) NOT NULL,
  hash VARBINARY(32) NOT NULL,
  updated_at DATETIME2 NOT NULL CONSTRAINT DF_sqlglot_migrations_updated_at DEFAULT SYSUTCDATETIME(),
  CONSTRAINT PK_sqlglot_migrations PRIMARY KEY (scope, path)
)`)
	if err != nil {
		return fmt.Errorf("create %s: %w", sqlServerMigrationsTable, err)
	}
	return nil
}

// orderedSQLServerScripts returns the scripts to run, in dependency order.
func orderedSQLServerScripts(ctx context.Context, db *sql.DB, scope string, scripts map[string]*script) ([]*script, error) {
	if len(scripts) == 0 {
		return nil, nil
	}
	hashes, err := recordedSQLServerHashes(ctx, db, scope)
	if err != nil {
		return nil, err
	}
	return topologicalScripts(scripts, selectFromRecordedHashes(scripts, hashes))
}

func runSQLServerScripts(ctx context.Context, db *sql.DB, opts scriptPhaseOptions) error {
	for _, s := range opts.Ordered {
		if s.phase != opts.Phase {
			continue
		}
		err := retryOnLockContention(ctx, "execute SQL migration "+s.path, func() error {
			if s.transactional {
				return applyTransactionalSQLServerScript(ctx, db, opts.Scope, s)
			}
			return applySQLServerScript(ctx, db, opts.Scope, s)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func applySQLServerScript(ctx context.Context, db *sql.DB, scope string, s *script) error {
	for i, batch := range s.batches {
		if _, err := db.ExecContext(ctx, batch); err != nil {
			return fmt.Errorf("execute non-transactional SQL migration %s batch %d: %w", s.path, i+1, err)
		}
	}
	return recordSQLServerScript(ctx, db, scope, s)
}

// applyTransactionalSQLServerScript runs every batch and records the hash in
// one transaction on one connection, under a bounded LOCK_TIMEOUT.
func applyTransactionalSQLServerScript(ctx context.Context, db *sql.DB, scope string, s *script) (returnErr error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("connect for SQL migration %s: %w", s.path, err)
	}
	defer func() { returnErr = errors.Join(returnErr, conn.Close()) }()
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("SET LOCK_TIMEOUT %d", sqlServerLockTimeoutMs)); err != nil {
		return fmt.Errorf("set LOCK_TIMEOUT for SQL migration %s: %w", s.path, err)
	}
	defer func() {
		if _, err := conn.ExecContext(context.WithoutCancel(ctx), "SET LOCK_TIMEOUT -1"); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("reset LOCK_TIMEOUT after SQL migration %s: %w", s.path, err))
		}
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin SQL migration %s: %w", s.path, err)
	}
	for i, batch := range s.batches {
		if _, err := tx.ExecContext(ctx, batch); err != nil {
			return errors.Join(fmt.Errorf("execute SQL migration %s batch %d: %w", s.path, i+1, err), tx.Rollback())
		}
	}
	if err := recordSQLServerScript(ctx, tx, scope, s); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit SQL migration %s: %w", s.path, err)
	}
	return nil
}

func recordSQLServerScript(ctx context.Context, db execer, scope string, s *script) error {
	_, err := db.ExecContext(ctx, `MERGE `+sqlServerMigrationsTable+` AS t
USING (SELECT @p1 AS scope, @p2 AS path) AS s ON t.scope = s.scope AND t.path = s.path
WHEN MATCHED THEN UPDATE SET hash = @p3, updated_at = SYSUTCDATETIME()
WHEN NOT MATCHED THEN INSERT (scope, path, hash) VALUES (@p1, @p2, @p3);`, scope, s.path, s.hash)
	if err != nil {
		return fmt.Errorf("record SQL migration %s: %w", s.path, err)
	}
	return nil
}

// PendingScript is one script Apply would run, and why: new or changed since
// it last ran, dependency (a script it depends on changed), or always (a
// `runs: always` script, which runs on every Apply and so is never a change
// by itself).
type PendingScript struct {
	Path   string `json:"path"`
	Phase  string `json:"phase"`
	Reason string `json:"reason"` // new | changed | dependency | always
}

// SQLServerPending is what Apply would do to a SQL Server database: the
// statements that reconcile the declared tables and routines, and the scripts
// it would run. It inspects only the objects the bundle names.
type SQLServerPending struct {
	Statements []string        `json:"statements,omitempty"`
	Scripts    []PendingScript `json:"scripts,omitempty"`
}

// Changed reports whether Apply would change anything beyond running its
// `runs: always` scripts.
func (p SQLServerPending) Changed() bool {
	if len(p.Statements) > 0 {
		return true
	}
	for _, s := range p.Scripts {
		if s.Reason != "always" {
			return true
		}
	}
	return false
}

// PendingSQLServer plans Apply on db without executing or recording anything.
func PendingSQLServer(ctx context.Context, db *sql.DB, schemaFS fs.FS, opts ...Option) (SQLServerPending, error) {
	cfg := resolveOptions(opts)
	bundle, err := loadSQLServerBundle(schemaFS, cfg)
	if err != nil {
		return SQLServerPending{}, err
	}
	var pending SQLServerPending
	if bundle.declaresObjects() {
		if pending.Statements, err = pendingSQLServerStatements(ctx, db, bundle, cfg); err != nil {
			return SQLServerPending{}, err
		}
	}
	hashes, err := recordedSQLServerHashes(ctx, db, migrationScope(sqlServerSchema, cfg.name))
	if err != nil {
		return SQLServerPending{}, err
	}
	selected := selectFromRecordedHashes(bundle.scripts, hashes)
	ordered, err := topologicalScripts(bundle.scripts, selected)
	if err != nil {
		return SQLServerPending{}, err
	}
	for _, phase := range []scriptPhase{phasePre, phasePost} {
		for _, s := range ordered {
			if s.phase != phase {
				continue
			}
			recorded, ok := hashes[s.path]
			reason := "changed"
			switch {
			case s.always:
				reason = "always"
			case !ok:
				reason = "new"
			case equalBytes(recorded, s.hash):
				reason = "dependency"
			}
			pending.Scripts = append(pending.Scripts, PendingScript{Path: s.path, Phase: string(s.phase), Reason: reason})
		}
	}
	return pending, nil
}

func pendingSQLServerStatements(ctx context.Context, db *sql.DB, bundle sqlServerBundle, cfg options) ([]string, error) {
	client, err := sqlserver.OpenClient(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("open SQL Server migration database: %w", err)
	}
	changes, err := sqlServerChanges(ctx, client, bundle.realm, cfg.exclude)
	if err != nil || len(changes) == 0 {
		return nil, err
	}
	plan, err := client.PlanChanges(ctx, "", changes)
	if err != nil {
		return nil, fmt.Errorf("plan %d schema changes: %w", len(changes), err)
	}
	statements := make([]string, len(plan.Changes))
	for i, change := range plan.Changes {
		statements[i] = change.Cmd
	}
	return statements, nil
}
