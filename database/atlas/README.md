# Atlas runtime extensions

`sqlserver` supplies the SQL Server Atlas driver, HCL codec, inspection and connection management. Pure SQL generation remains in `migrate/atlas/sqlserver`. Raw catalog SQL lives in `database/inspect/catalog/sqlserver` and is shared with the catalog and complete snapshot inspectors.

`internal/sqlx` and `internal/specutil` originate from Atlas v0.38.0 internal packages, which external modules cannot import. Local changes adapt imports, preserve errors and support the SQL Server codec. Retain these changes when updating the upstream baseline. Apache-2.0 headers, attribution and license are preserved in `NOTICE` and `LICENSE.atlas`.

To inspect the upstream baseline without overwriting local changes:

```sh
ATLAS="$(go env GOMODCACHE)/ariga.io/atlas@$(go list -m -f '{{.Version}}' ariga.io/atlas)"
diff -u "$ATLAS/sql/internal/sqlx/diff.go" database/atlas/internal/sqlx/diff.go
diff -u "$ATLAS/sql/internal/specutil/convert.go" database/atlas/internal/specutil/convert.go
GOWORK=off go test ./database/atlas/...
```

When updating Atlas, compare all helper files, review the local changes and update the attribution version. Run both pure planner and runtime driver tests. commons-db's `atlas/sqlserver` package is a compatibility shim and must not acquire a second driver registration.
