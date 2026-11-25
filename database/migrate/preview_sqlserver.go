package migrate

import (
	"context"
	"database/sql"
	"io/fs"
)

// previewSQLServer previews a SQL Server bundle that has no scripts as its
// schema artifact, the one Apply embeds in its plan file (what an unreviewed
// apply runs, unless WithReviewedPlan). A bundle with scripts previews as that
// plan file instead (PreviewBundle routes it to planSQLServer).
func previewSQLServer(ctx context.Context, db *sql.DB, files fs.FS, cfg options) (unreviewedSelection, error) {
	realm, err := sqlServerDesired(files, cfg)
	if err != nil {
		return unreviewedSelection{}, err
	}
	return planSchemaStep(ctx, db, sqlServerBundle{realm: realm}, cfg)
}
