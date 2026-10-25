# Standalone database inspection and migrations

These packages own the database implementation previously provided by commons-db. They import no commons-db packages. The parser and pure SQL Server planner remain independent of database drivers under `sqlglot` and `migrate`.

| Package | Responsibility |
| --- | --- |
| `database/inspect` | Bounded, uncached PostgreSQL, SQLite, SQL Server, MySQL and ClickHouse catalogs |
| `database/capabilities` | Live backend version and feature probes |
| `database/inspect/snapshot` | Complete supported SQL Server snapshots, including modules and dependencies |
| `database/atlas/sqlserver` | SQL Server Atlas HCL codec and runtime driver |
| `database/migrate` | Existing HCL, scripts, security and provisioner API; read-only bundle preview |
| `database/migrate/review` | Reviewed PostgreSQL and SQLite schema artifacts and transactional execution |
| `database/migrate/sqlserver` | Existing SQL Server v1 artifact execution |
| `database/migrate/sqlite` | Programmatic SQLite table reconciliation |
| `database/migrate/viewdeps` | PostgreSQL dependent view capture, invalidation and restoration |

commons-db preserves its migration and inspection APIs through aliases and forwarding functions. Its catalog cache, refresh policy and metadata remain in commons-db. Supported SQLite rebuilds are automatic; the previous `WithRebuilds` and `AllowRebuilds` names remain accepted. Column and table drops remain opt-in for bundle reconciliation.

## CLI

Build with `make build`. Database commands emit JSON. Set the database connection in your shell rather than writing credentials into artifacts.

```sh
.tmp/sqlglot schema inspect --dsn "$DATABASE_URL"
.tmp/sqlglot schema inspect --dsn "$DATABASE_URL" --snapshot --schema public
.tmp/sqlglot migrate plan --dsn "$DATABASE_URL" --desired examples/database/schema.hcl --out .tmp/reviewed.json
.tmp/sqlglot migrate apply --dsn "$DATABASE_URL" --artifact .tmp/reviewed.json --hash "$REVIEWED_HASH"
```

`--dsn` is required. Migration connections support PostgreSQL URLs or keyword DSNs, SQL Server URLs/ADO/ODBC strings, and SQLite file paths or file URIs. `--driver` selects a catalog backend explicitly, including `mysql` with its native driver DSN and `clickhouse` with its native URL. `--schema` defaults to `public`, `dbo`, or `main`. Catalog inspection is bounded and may report truncation; `--snapshot` requests a migration snapshot and rejects unsupported features instead of truncating.

### Named connections

`--dsn connection://<name>` (or `connection://<namespace>/<name>`) resolves a named connection instead of a literal DSN. Connections are commons-db connection YAML files, one per file, in `$XDG_CONFIG_HOME/sqlglot-go/connections` (default `~/.config/sqlglot-go/connections`, override with `--connections-dir`). The file stem is the name. `url` is the full DSN template: `$(username)` and `$(password)` insert the query-escaped credentials, and `{{.properties.<key>}}` inserts a property. Any value may be a `secret://<name>/<key>`, `configmap://<name>/<key>` or `helm://` reference, read from the connection's `namespace` through the kubeconfig context named by `properties.kubecontext` (the current context when unset). Files hold references only, never secret values.

```yaml
# ~/.config/sqlglot-go/connections/tenant-x.yaml
namespace: tenant-x
type: sql_server
url: sqlserver://$(username):$(password)@{{.properties.host}}?database={{.properties.database}}
username: secret://db/username
password: secret://db/password
properties:
  kubecontext: example-cluster
  host: secret://db/host
  database: secret://db/database
```

```sh
.tmp/sqlglot connections list
.tmp/sqlglot schema inspect --dsn connection://tenant-x --snapshot --schema dbo
```

`migrate plan` requires exactly one of `--desired` or `--bundle`. Desired input is backend HCL or a saved snapshot JSON; SQL Server uses its typed snapshot JSON. `--out` stores the JSON with owner-only permissions. Bundle directories use the existing portable HCL conventions and default to retaining omitted columns, tables and types. `--allow-drops` includes their removal in the reviewed plan. SQL Server routine files can be selected with `--routines` relative to the bundle directory.

Supply bundle variables with repeatable `--var 'name=HCL-literal'`, for example `--var 'prefix="app"'` or `--var 'limit=10'`. Variables, routines and the drop option require a bundle input. SQLite apply opens an existing file with `mode=rw`; the reviewed executor owns its temporary connection pragmas.

```sh
.tmp/sqlglot migrate plan --dsn "$DATABASE_URL" --bundle ./db --out .tmp/preview.json
.tmp/sqlglot migrate apply --dsn "$DATABASE_URL" --bundle ./db --hash "$REVIEWED_HASH"
.tmp/sqlglot migrate apply --dsn "$DATABASE_URL" --artifact .tmp/reviewed.json --hash "$REVIEWED_HASH" --ack "$DESTRUCTIVE_STEP_ID"
```

Applying a bundle replans it and requires the resulting hash to match the reviewed hash. Applying an artifact executes its saved SQL after validating it against the backend planner. `--hash` is mandatory. `--ack` accepts each required destructive ID, repeated or comma separated; missing, duplicate and unknown IDs fail. `--lock-timeout` bounds migration lock waiting and defaults to five seconds. PostgreSQL/SQLite zero selects the default; SQL Server zero means no wait. A commit error returns `commit-unknown` because loss of the commit response cannot prove rollback.

SQLite inspection and preview open existing files in read-only mode. They do not create files or change journal mode. Bundle preview never runs pre/post scripts, creates migration metadata or creates a PostgreSQL schema. Pre-scripts are parsed with sqlglot and reported as DML, DDL, mixed or unknown, with parser completeness and diagnostics. DDL or unknown pre-scripts produce warnings, and schema planning is still attempted against the current database. Planning errors return the script analysis and a nonzero exit code.

## Reviewed schema scope

PostgreSQL and SQLite artifacts use version 2. They bind dialect, schema, baseline and desired HCL snapshots, ordered SQL, destructive IDs and advisory reasons to a SHA-256 hash. SQL Server retains its existing version 1 artifact and hash algorithm. All use the same CLI commands.

PostgreSQL apply acquires a transaction-owned advisory lock before drift inspection. SQLite uses one pinned connection for pragma changes, `BEGIN IMMEDIATE`, drift inspection, exact SQL, foreign key checks, commit/rollback and pragma restoration. A current schema that matches the desired state reports `already-applied`; any other departure from the baseline is rejected. This shortcut applies only to schema artifacts.

The PostgreSQL/SQLite reviewed path supports schema-only transactional table changes and PostgreSQL enums within one existing schema. Views, routines, user triggers, virtual tables, PostgreSQL partitioning/inheritance, domains, standalone sequences, row security and unsupported constraint states are refused. Snapshot round-trip checks reject metadata the HCL codec cannot preserve. Nontransactional plans are refused. Check expressions must compare to the backend-rendered form; unsupported normalization fails rather than accepting an unverified postcondition.

Bundles containing scripts or security declarations produce advisory schema previews. Advisory artifacts cannot execute. Full reviewed PostgreSQL script/security phases, completion records and retry semantics are tracked separately. The existing library `migrate.Apply` retains its complete PostgreSQL script and security lifecycle; callers of that direct API are responsible for authorizing execution.

## Library use

`inspect.Inspect(ctx, db, driver, Limits{})` accepts a caller-owned `*sql.DB` and returns an uncached catalog. `review.Inspect` returns a supported migration snapshot. `review.Build` accepts `Options{Dialect, Schema, DesiredHCL}` and performs read-only planning. `review.Apply` requires `Request{Artifact, Hash, Acknowledgements, LockTimeout}`. None closes the caller's pool or changes its pool settings.

Run the [SQLite example](../examples/database/main.go) with `go run ./examples/database`. It uses a disposable directory, inspects, plans, applies and repeats an artifact, then removes the directory. The adjacent `schema.hcl` is a PostgreSQL CLI example.

## Verification

```sh
GOWORK=off go test ./database/... ./cmd/sqlglot ./migrate/...
GOWORK=off make build
GOWORK=off make lint
GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 make build-database-tests
bash hack/database-integration.sh postgres
bash hack/database-integration.sh sqlserver
```

Install golangci-lint v2.13.1 for the lint target. The integration harness uses disposable containers with no published ports, network access or host mounts, copies Linux test binaries into them and removes them on exit. It works with a remote Docker context. Build the test binaries for the Docker server architecture; the SQL Server image requires amd64. For an externally managed disposable server, run integration-tagged review tests with `SQLGLOT_TEST_POSTGRES_URL`, or SQL Server executor tests with `COMMONS_DB_SQLSERVER_ARTIFACT_TEST_URL`. These tests create and remove isolated databases.

The harness rebuilds its test binaries automatically. Set `SQLGLOT_DOCKER_ARCH=arm64` for PostgreSQL on an ARM64 Docker server; the default is amd64. Migration advisory locks coordinate cooperating reviewed executors. Other DDL writers must be serialized with deployment execution.
