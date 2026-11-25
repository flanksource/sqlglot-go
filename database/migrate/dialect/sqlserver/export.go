package sqlserver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
)

// ExportOptions select what ExportHCL reads and how it writes it.
type ExportOptions struct {
	Schema string
	// Tables are the tables to export; empty exports every table of the schema.
	Tables []string
	// HCL configures the writer. DatabaseCollation is read from the database.
	HCL planner.HCLOptions
}

// ExportHCL reads the tables of one schema and writes them as SQL Server HCL
// that the bundle evaluator reads back to the same tables (see
// planner.ExportHCL). Whatever the inspector could not read of a selected table
// (a refused table or member) fails the export, unless HCL.OmitUnsupported
// turns it into a comment line at the top of the output.
func ExportHCL(ctx context.Context, q engine.Querier, opts ExportOptions) ([]planner.HCLFile, error) {
	names, err := selectTables(ctx, q, opts)
	if err != nil {
		return nil, err
	}
	objects := make([]migrate.Name, len(names))
	for i, n := range names {
		objects[i] = migrate.Name{Schema: opts.Schema, Name: n}
	}
	inspection, err := Dialect{}.Inspect(ctx, q, engine.Scope{Mode: engine.ScopeDeclared, Schemas: []string{opts.Schema}, Objects: objects})
	if err != nil {
		return nil, fmt.Errorf("sqlserver: read tables of %s: %w", opts.Schema, err)
	}
	hcl := opts.HCL
	if hcl.DatabaseCollation, err = databaseCollation(ctx, q); err != nil {
		return nil, err
	}
	if hcl.Notes, err = refusalNotes(inspection.Refusals, hcl.OmitUnsupported); err != nil {
		return nil, err
	}
	tables := migrate.Schema{Tables: inspection.Schema.Tables}
	return planner.ExportHCL(tables, hcl)
}

// selectTables lists the requested tables of the schema, or all of them, in
// catalog casing, and fails for a requested table that does not exist.
func selectTables(ctx context.Context, q engine.Querier, opts ExportOptions) ([]string, error) {
	all, err := schemaTables(ctx, q, opts.Schema)
	if err != nil {
		return nil, err
	}
	if len(opts.Tables) == 0 {
		return all, nil
	}
	for _, t := range opts.Tables {
		if !slices.Contains(all, t) {
			return nil, fmt.Errorf("sqlserver: table %s.%s does not exist", opts.Schema, t)
		}
	}
	return opts.Tables, nil
}

func schemaTables(ctx context.Context, q engine.Querier, schemaName string) (names []string, returnErr error) {
	rows, err := q.QueryContext(ctx, `SELECT t.name FROM sys.tables t JOIN sys.schemas s ON s.schema_id = t.schema_id
		WHERE s.name = @p1 AND t.is_ms_shipped = 0 ORDER BY t.name`, schemaName)
	if err != nil {
		return nil, fmt.Errorf("sqlserver: list tables of %s: %w", schemaName, err)
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func databaseCollation(ctx context.Context, q engine.Querier) (string, error) {
	var collation string
	err := q.QueryRowContext(ctx, `SELECT CONVERT(nvarchar(128), DATABASEPROPERTYEX(DB_NAME(), 'Collation'))`).Scan(&collation)
	if err != nil || strings.TrimSpace(collation) == "" {
		return "", errors.Join(errors.New("sqlserver: read the database collation"), err)
	}
	return collation, nil
}

// refusalNotes are the comment lines for what the inspector refused, or an
// error naming each when they may not be left out.
func refusalNotes(refusals []engine.Refusal, omit bool) ([]string, error) {
	var notes []string
	var problems []error
	for _, r := range refusals {
		label := r.Object.Schema + "." + r.Object.Name
		if r.Member != "" {
			label += ", " + r.Member
		}
		if omit {
			notes = append(notes, fmt.Sprintf("omitted: table %s: %s", label, r.Reason))
			continue
		}
		problems = append(problems, fmt.Errorf("sqlserver: table %s cannot be read faithfully: %s; set OmitUnsupported to leave it out", label, r.Reason))
	}
	return notes, errors.Join(problems...)
}
