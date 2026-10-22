package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"

	"github.com/flanksource/sqlglot-go/database/inspect/snapshot"
	native "github.com/flanksource/sqlglot-go/migrate"
	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
)

func previewSQLServer(ctx context.Context, db *sql.DB, files fs.FS, cfg options, result Preview, advisory []string) (Preview, error) {
	if cfg.schemaSet && cfg.schema != "dbo" {
		return result, fmt.Errorf("SQL Server bundle scope must be dbo")
	}
	if len(cfg.exclude) > 0 {
		return result, fmt.Errorf("reviewed SQL Server artifacts require complete schema inspection")
	}
	realm, err := sqlServerTables(files, cfg)
	if err != nil {
		return result, err
	}
	if cfg.routines != "" {
		if err := addRoutines(files, cfg.routines, realm.Schemas[0]); err != nil {
			return result, err
		}
	}
	current, err := snapshot.Inspect(ctx, db, snapshot.Options{Schemas: []string{"dbo"}})
	if err != nil {
		return result, err
	}
	if err := resolveSQLServerDefaults(ctx, db, realm, current.Schema); err != nil {
		return result, err
	}
	desired, err := planner.FromAtlas(realm)
	if err != nil {
		return result, err
	}
	if !cfg.allowDrops {
		preserveSQLServerOmitted(current.Schema, &desired)
	}
	artifact, err := planner.BuildArtifact(native.ArtifactRequest{Schemas: current.Schemas, Request: native.Request{Current: current.Schema, Desired: desired}})
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	result.Warnings = append(result.Warnings, advisory...)
	if len(advisory) > 0 {
		result.SQLServerPlan = &artifact.Plan
	} else {
		result.SQLServerArtifact = &artifact
	}
	return result, nil
}

func preserveSQLServerOmitted(current native.Schema, desired *native.Schema) {
	for _, before := range current.Tables {
		found := false
		for i := range desired.Tables {
			after := &desired.Tables[i]
			if after.Name != before.Name {
				continue
			}
			found = true
			for _, column := range before.Columns {
				present := false
				for _, candidate := range after.Columns {
					if candidate.Name == column.Name {
						present = true
						break
					}
				}
				if !present {
					after.Columns = append(after.Columns, column)
				}
			}
			for _, index := range before.Indexes {
				present := false
				for _, candidate := range after.Indexes {
					if candidate.Name == index.Name {
						present = true
						break
					}
				}
				if !present {
					after.Indexes = append(after.Indexes, index)
				}
			}
			break
		}
		if !found {
			desired.Tables = append(desired.Tables, before)
		}
	}
	for _, definition := range current.Definitions {
		present := false
		for _, candidate := range desired.Definitions {
			if candidate.Name == definition.Name {
				present = true
				break
			}
		}
		if !present {
			desired.Definitions = append(desired.Definitions, definition)
		}
	}
}
