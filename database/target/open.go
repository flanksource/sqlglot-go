package dbtarget

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/flanksource/sqlglot-go/database/connections"
	inspection "github.com/flanksource/sqlglot-go/database/inspect"
)

type OpenOptions struct {
	// DSN is a connection string or connection://[<namespace>/]<name>.
	DSN string
	// Driver overrides the dialect parsed from the DSN; it must agree with it.
	Driver string
	// Schema defaults to main, dbo or public by dialect.
	Schema         string
	ConnectionsDir string
	ReadOnly       bool
}

// Connection is an open, pinged database with its resolved dialect and schema.
type Connection struct {
	DB      *sql.DB
	Dialect string
	Schema  string
}

// Open resolves a named connection, normalizes the driver and DSN, and pings
// the database. SQLite opens read-only or read-write as requested.
func Open(ctx context.Context, opts OpenOptions) (Connection, error) {
	if strings.TrimSpace(opts.DSN) == "" {
		return Connection{}, fmt.Errorf("database dsn is required")
	}
	dsn := opts.DSN
	if strings.HasPrefix(dsn, connections.Prefix) {
		resolved, err := connections.Resolve(ctx, connections.ResolveOptions{Dir: opts.ConnectionsDir, Reference: dsn})
		if err != nil {
			return Connection{}, err
		}
		dsn = resolved
	}
	dialect := inspection.NormalizeDriver(opts.Driver)
	if dialect == "" && strings.HasPrefix(dsn, "clickhouse://") {
		dialect = "clickhouse"
	}
	if dialect == "" || dialect == "sqlite" || dialect == "sqlserver" || dialect == "postgres" {
		t, err := Parse(dsn)
		if err != nil {
			return Connection{}, err
		}
		if dialect != "" && dialect != string(t.Dialect) {
			return Connection{}, fmt.Errorf("driver %s does not match the connection dialect %s", dialect, t.Dialect)
		}
		dialect, dsn = string(t.Dialect), t.DSN
	}
	if dialect == "sqlite" {
		u, err := url.Parse(dsn)
		if err != nil {
			return Connection{}, err
		}
		q := u.Query()
		q.Del("_pragma")
		if opts.ReadOnly {
			q.Set("mode", "ro")
		} else {
			q.Set("mode", "rw")
		}
		u.RawQuery = q.Encode()
		dsn = u.String()
	}
	schema := opts.Schema
	if schema == "" {
		switch dialect {
		case "sqlite":
			schema = "main"
		case "sqlserver":
			schema = "dbo"
		default:
			schema = "public"
		}
	}
	db, err := sql.Open(dialect, dsn)
	if err != nil {
		return Connection{}, err
	}
	if err := db.PingContext(ctx); err != nil {
		return Connection{}, errors.Join(err, db.Close())
	}
	return Connection{DB: db, Dialect: dialect, Schema: schema}, nil
}
