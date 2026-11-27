package migrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flanksource/commons/logger"
	pgdialect "github.com/flanksource/sqlglot-go/database/migrate/dialect/postgres"
	sqlitedialect "github.com/flanksource/sqlglot-go/database/migrate/dialect/sqlite"
	native "github.com/flanksource/sqlglot-go/migrate"
	"github.com/lib/pq"
)

// schemaLedgerTable records, per bundle scope, the hash of the desired schema
// the last successful unreviewed apply brought the database to.
const schemaLedgerTable = "schema_migration_schema"

// schemaLedgerVersion is hashed into every recorded desired schema. Bump it
// when the planner or a dialect may plan differently for the same desired
// schema, so every database is planned once more.
const schemaLedgerVersion = 1

// schemaLedger is where an unreviewed apply records the desired schema it
// brought the database to, so the next apply of the same bundle skips the
// inspection and planning of an unchanged schema.
type schemaLedger struct {
	Scope string
	// Scripts identify the bundle's SQL scripts by path and content hash; a
	// script may change tables too, so editing one plans the schema again.
	Scripts []string
	// Replan plans even when the recorded hash matches, because a pre script
	// that may change the tables runs in this apply.
	Replan bool
}

// desiredHash covers everything that decides what the step plans: the desired
// schema (HCL, variables, exclusions and partial tables applied), the dialect,
// the managed schema, the drop scope, the inspection exclusions and scripts.
func (s schemaStep) desiredHash(scripts []string) (string, error) {
	body, err := json.Marshal(struct {
		Version    int
		Dialect    string
		Schema     string
		AllowDrops bool
		Exclude    []string
		Desired    native.Schema
		Scripts    []string
	}{schemaLedgerVersion, s.Dialect, s.Schema, s.AllowDrops, s.Exclude, s.Desired, scripts})
	if err != nil {
		return "", fmt.Errorf("hash desired %s schema: %w", s.Dialect, err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// runRecorded runs the step unless the ledger records that the database was
// brought to this desired schema, and records the schema once it is applied.
// A database changed outside the bundle since then is not repaired until the
// bundle changes.
func (s schemaStep) runRecorded(ctx context.Context, db *sql.DB, ledger schemaLedger) error {
	hash, err := s.desiredHash(ledger.Scripts)
	if err != nil {
		return err
	}
	if err := ensureSchemaLedger(ctx, db); err != nil {
		return err
	}
	if !ledger.Replan {
		recorded, err := recordedSchemaHash(ctx, db, ledger.Scope)
		if err != nil {
			return err
		}
		if recorded == hash {
			logger.GetLogger("migrate").Debugf("Schema of %s unchanged since its last apply", ledger.Scope)
			return nil
		}
	}
	if err := s.run(ctx, db); err != nil {
		return err
	}
	return recordSchemaHash(ctx, db, ledger.Scope, hash)
}

// ForgetRecordedSchemas deletes the schema ledger of every bundle scope in the
// given schemas, so the next Apply of any bundle there plans against the
// database again instead of skipping. Every path that changes a PostgreSQL or
// SQLite schema without Apply (the reviewed workflow) calls it, before and
// after applying. SQL Server bundles keep no schema ledger.
func ForgetRecordedSchemas(ctx context.Context, db *sql.DB, dialect string, schemas []string) error {
	switch dialect {
	case "sqlserver":
		return nil
	case sqlitedialect.Name:
		return forgetSchemaLedger(ctx, db, schemaLedgerTable, `SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = $1`, schemaLedgerTable)
	case pgdialect.Name:
		for _, schemaName := range schemas {
			table := pq.QuoteIdentifier(schemaName) + "." + schemaLedgerTable
			if err := forgetSchemaLedger(ctx, db, table, `SELECT count(*) FROM pg_tables WHERE schemaname = $1 AND tablename = $2`, schemaName, schemaLedgerTable); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("forget recorded schemas: unsupported dialect %q", dialect)
	}
}

// forgetSchemaLedger empties table when the exists query counts it.
func forgetSchemaLedger(ctx context.Context, db *sql.DB, table, exists string, args ...any) error {
	var count int
	if err := db.QueryRowContext(ctx, exists, args...).Scan(&count); err != nil {
		return fmt.Errorf("find %s: %w", table, err)
	}
	if count == 0 {
		return nil
	}
	// #nosec G202 -- table is the ledger's constant name, qualified by a quoted schema identifier.
	if _, err := db.ExecContext(ctx, "DELETE FROM "+table); err != nil {
		return fmt.Errorf("forget the schemas recorded in %s: %w", table, err)
	}
	return nil
}

func ensureSchemaLedger(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+schemaLedgerTable+` (
  scope text PRIMARY KEY,
  hash text NOT NULL,
  updated_at timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP
)`); err != nil {
		return fmt.Errorf("create %s: %w", schemaLedgerTable, err)
	}
	return nil
}

func recordedSchemaHash(ctx context.Context, db *sql.DB, scope string) (string, error) {
	var hash string
	err := db.QueryRowContext(ctx, `SELECT hash FROM `+schemaLedgerTable+` WHERE scope = $1`, scope).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read recorded schema of %s: %w", scope, err)
	}
	return hash, nil
}

func recordSchemaHash(ctx context.Context, db *sql.DB, scope, hash string) error {
	if _, err := db.ExecContext(ctx, `INSERT INTO `+schemaLedgerTable+` (scope, hash, updated_at)
VALUES ($1, $2, CURRENT_TIMESTAMP)
ON CONFLICT (scope) DO UPDATE SET hash = excluded.hash, updated_at = CURRENT_TIMESTAMP`, scope, hash); err != nil {
		return fmt.Errorf("record applied schema of %s: %w", scope, err)
	}
	return nil
}
