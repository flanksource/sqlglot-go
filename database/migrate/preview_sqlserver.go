package migrate

import (
	"context"
	"database/sql"
	"io/fs"

	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// previewSQLServer previews a SQL Server bundle that has no scripts as its
// schema artifact, the one Apply embeds in its plan file. A bundle with scripts
// previews as that plan file instead (PreviewBundle routes it to planSQLServer).
func previewSQLServer(ctx context.Context, db *sql.DB, files fs.FS, cfg options) (engine.Artifact, error) {
	realm, err := sqlServerDesired(files, cfg)
	if err != nil {
		return engine.Artifact{}, err
	}
	return planSchemaArtifact(ctx, db, sqlServerBundle{realm: realm})
}
