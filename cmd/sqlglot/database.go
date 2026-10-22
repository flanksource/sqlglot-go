package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	_ "github.com/ClickHouse/clickhouse-go/v2"
	_ "github.com/go-sql-driver/mysql"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	inspection "github.com/flanksource/sqlglot-go/database/inspect"
	"github.com/flanksource/sqlglot-go/database/inspect/snapshot"
	"github.com/flanksource/sqlglot-go/database/migrate/review"
	"github.com/flanksource/sqlglot-go/database/target"
	"github.com/spf13/cobra"
)

type databaseFlags struct {
	dsn     string
	dialect string
	schema  string
}

func (f *databaseFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.dsn, "dsn", "", "Database connection string")
	cmd.Flags().StringVar(&f.dialect, "driver", "", "Database driver (defaults to connection dialect)")
	cmd.Flags().StringVar(&f.schema, "schema", "", "Schema to inspect or migrate (defaults to main, public or dbo)")
}

func (f *databaseFlags) open(ctx context.Context, readOnly bool) (*sql.DB, error) {
	if strings.TrimSpace(f.dsn) == "" {
		return nil, fmt.Errorf("database dsn is required")
	}
	dsn := f.dsn
	f.dialect = inspection.NormalizeDriver(f.dialect)
	if f.dialect == "" && strings.HasPrefix(dsn, "clickhouse://") {
		f.dialect = "clickhouse"
	}
	if f.dialect == "" || f.dialect == "sqlite" || f.dialect == "sqlserver" || f.dialect == "postgres" {
		t, err := dbtarget.Parse(dsn)
		if err != nil {
			return nil, err
		}
		if f.dialect != "" && f.dialect != string(t.Dialect) {
			return nil, fmt.Errorf("driver %s does not match the connection dialect %s", f.dialect, t.Dialect)
		}
		f.dialect, dsn = string(t.Dialect), t.DSN
	}
	if f.dialect == "sqlite" {
		u, err := url.Parse(dsn)
		if err != nil {
			return nil, err
		}
		q := u.Query()
		q.Del("_pragma")
		if readOnly {
			q.Set("mode", "ro")
		} else {
			q.Set("mode", "rw")
		}
		u.RawQuery = q.Encode()
		dsn = u.String()
	}
	if f.schema == "" {
		switch f.dialect {
		case "sqlite":
			f.schema = "main"
		case "sqlserver":
			f.schema = "dbo"
		default:
			f.schema = "public"
		}
	}
	db, err := sql.Open(f.dialect, dsn)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}

func addDatabaseCommands(root *cobra.Command) {
	schemaCmd := &cobra.Command{Use: "schema", Short: "Inspect database schemas"}
	flags := &databaseFlags{}
	var fullSnapshot bool
	inspectCmd := &cobra.Command{Use: "inspect", Short: "Read a catalog or migration snapshot", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) (returnErr error) {
		db, err := flags.open(cmd.Context(), true)
		if err != nil {
			return err
		}
		defer func() { returnErr = errors.Join(returnErr, db.Close()) }()
		var result any
		if fullSnapshot && flags.dialect == "sqlserver" {
			result, err = snapshot.Inspect(cmd.Context(), db, snapshot.Options{Schemas: []string{flags.schema}})
		} else if fullSnapshot {
			result, err = review.Inspect(cmd.Context(), db, flags.dialect, flags.schema)
		} else {
			result, err = inspection.Inspect(cmd.Context(), db, flags.dialect, inspection.Limits{})
		}
		if err != nil {
			return err
		}
		return writeDatabaseJSON(cmd, "", result)
	}}
	flags.bind(inspectCmd)
	inspectCmd.Flags().BoolVar(&fullSnapshot, "snapshot", false, "Return a complete migration snapshot for the selected schema")
	schemaCmd.AddCommand(inspectCmd)
	migrateCmd := &cobra.Command{Use: "migrate", Short: "Plan and apply reviewed database migrations"}
	migrateCmd.AddCommand(newMigrationPlanCommand(), newMigrationApplyCommand())
	root.AddCommand(schemaCmd, migrateCmd)
}

func writeDatabaseJSON(cmd *cobra.Command, path string, value any) (returnErr error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if path != "" {
		root, err := os.OpenRoot(filepath.Dir(path))
		if err != nil {
			return err
		}
		defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
		return root.WriteFile(filepath.Base(path), data, 0600)
	}
	_, err = cmd.OutOrStdout().Write(data)
	return err
}
