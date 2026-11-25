# Standalone database inspection and migrations

These packages own the database implementation previously provided by commons-db. They import no commons-db packages. The parser and pure SQL Server planner remain independent of database drivers under `sqlglot` and `migrate`.

| Package | Responsibility |
| --- | --- |
| `database/inspect` | Bounded, uncached PostgreSQL, SQLite, SQL Server, MySQL and ClickHouse catalogs |
| `database/capabilities` | Live backend version and feature probes |
| `database/inspect/snapshot` | Complete supported SQL Server snapshots, including modules and dependencies |
| `database/atlas/sqlserver` | SQL Server Atlas HCL codec and runtime driver |
| `database/migrate` | Existing HCL, scripts, security and provisioner API; read-only bundle preview |
| `database/migrate/dialect` | The SQL Server, PostgreSQL and SQLite dialects of the schema engine in `migrate/engine`, registered by name when imported |
| `database/migrate/workflow` | The plan, review and apply workflow the CLI and web UI share, over the one reviewed artifact |
| `database/migrate/csvdata` | [Reviewed SQL Server CSV migrations](migrate/README.md#reviewed-csv-data) |
| `database/migrate/sqlite` | Programmatic SQLite table reconciliation |

commons-db preserves its migration and inspection APIs through aliases and forwarding functions. Its catalog cache, refresh policy and metadata remain in commons-db. Supported SQLite rebuilds are automatic; the previous `WithRebuilds` and `AllowRebuilds` names remain accepted. An unreviewed bundle apply never drops a column or a table unless `WithDrops` is set: it reports each undeclared column it left in place, and a table marked `partial = true` keeps every member it does not name.

## CLI

Build with `make build`. Database commands emit JSON. Set the database connection in your shell rather than writing credentials into artifacts. See the [migration CLI guide](../docs/guide/cli.md#migrate) for HCL, SQL, script headers, dependency invalidation, and CSV examples.

```sh
.tmp/sqlglot schema inspect --dsn "$DATABASE_URL"
.tmp/sqlglot schema inspect --dsn "$DATABASE_URL" --snapshot --schema public
.tmp/sqlglot migrate plan --dsn "$DATABASE_URL" --desired examples/database/schema.hcl --out .tmp/reviewed.json
.tmp/sqlglot migrate apply --dsn "$DATABASE_URL" .tmp/reviewed.json
```

`--dsn` is required. Migration connections support PostgreSQL URLs or keyword DSNs, SQL Server URLs/ADO/ODBC strings, and SQLite file paths or file URIs. `--driver` selects a catalog backend explicitly, including `mysql` with its native driver DSN and `clickhouse` with its native URL. `--schema` defaults to `public`, `dbo`, or `main`. Catalog inspection is bounded and may report truncation; `--snapshot` requests a migration snapshot and rejects unsupported features instead of truncating.

### Named connections

`--dsn connection://<name>` (or `connection://<namespace>/<name>`) resolves a named connection instead of a literal DSN. Connections are typed mission-control `Connection` resources, one per file, read from the commons-db connection store in three layers, each overriding the one before by namespace and name:

1. `$XDG_CONFIG_HOME/flanksource/connections` (default `~/.config/flanksource/connections`), shared with every Flanksource tool;
2. `$XDG_CONFIG_HOME/sqlglot-go/connections`, sqlglot's own layer, where `sqlglot ui` saves connections (override with `--connections-dir`);
3. `./.connections` in the working directory.

The file stem is the name when `metadata.name` is unset. `spec.url` is the full DSN template: `$(username)` and `$(password)` insert the query-escaped credentials, and `{{.properties.<key>}}` inserts a property. Credentials and properties may be `secret://<name>/<key>`, `configmap://<name>/<key>` or `helm://` references (`valueFrom` in the resource), read from the connection's `namespace` through the kubeconfig context named by `properties.kubecontext` (the current context when unset). The UI refuses literal credentials, so files it writes hold references only. A flat connection file from earlier versions (top-level `type`/`url`) fails with an error naming the file and the expected `apiVersion`/`kind`.

```yaml
# ~/.config/sqlglot-go/connections/tenant-x.yaml
apiVersion: mission-control.flanksource.com/v1
kind: Connection
metadata:
  name: tenant-x
  namespace: tenant-x
spec:
  type: sql_server
  url:
    value: sqlserver://$(username):$(password)@{{.properties.host}}?database={{.properties.database}}
  username:
    valueFrom: {secretKeyRef: {name: db, key: username}}
  password:
    valueFrom: {secretKeyRef: {name: db, key: password}}
  properties:
    kubecontext: example-cluster
    host: secret://db/host
    database: secret://db/database
```

```sh
.tmp/sqlglot connections list
.tmp/sqlglot schema inspect --dsn connection://tenant-x --snapshot --schema dbo
```

`migrate plan` accepts a desired schema with `--desired`, a schema bundle with `--bundle`, or a [SQL Server CSV folder](migrate/README.md#reviewed-csv-data) with `--csv-root`. Desired input is backend HCL or a saved snapshot JSON. SQL Server uses its typed snapshot JSON. `--out` writes the reviewed JSON and creates new files with owner-only permissions. Bundle directories never drop an omitted table by default, and `--allow-drops` considers omitted tables for removal. A table a bundle declares is complete unless it says `partial = true`: the reviewed plan lists the drops of its omitted columns, indexes, keys and checks as destructive changes to acknowledge, and a partial table keeps them. Use `--routines` to select SQL Server routine files relative to the bundle directory.

Supply bundle variables with repeatable `--var 'name=HCL-literal'`, for example `--var 'prefix="app"'` or `--var 'limit=10'`. Variables, routines and the drop option require a bundle input. SQLite apply opens an existing file with `mode=rw`; the reviewed executor owns its temporary connection pragmas.

```sh
.tmp/sqlglot migrate plan --dsn "$DATABASE_URL" --bundle ./db --out .tmp/preview.json
.tmp/sqlglot migrate review .tmp/preview.json
.tmp/sqlglot migrate apply --dsn "$DATABASE_URL" .tmp/preview.json --ack "$DESTRUCTIVE_STEP_ID"
```

Schema apply takes the saved plan file as its argument. The file carries its own hash, which guards only its integrity; apply validates the artifact and executes its saved SQL. Pass each required destructive ID as its own `--ack` argument. Missing, duplicate and unknown IDs fail. Omit `--ack` when the artifact has no destructive steps. `--lock-timeout` accepts positive durations and defaults to five seconds. The executor records a commit error as `commit-unknown` because the commit response may have been lost.

SQLite inspection and preview open existing files in read-only mode. Bundle preview parses SQL with sqlglot and reports scripts as DML, DDL, mixed or unknown, with parser completeness and diagnostics. Pre-scripts classified as DDL, mixed or unknown produce warnings, and schema planning uses the current database without executing the scripts. Bundles with phased scripts produce advisory previews.

### Comparing two connections

`schema compare` inspects a reference connection (the desired schema) and a target connection (the database to patch) of the same dialect, and lists every difference as a selectable change grouped by object, with each object's definition on both sides and a `diffHash`. `schema patch` re-inspects both sides, refuses with a schema drift error when the differences no longer match `diffHash`, and plans only the `--select`ed change IDs into a reviewed artifact plus a plain SQL script. Selecting a change without the changes it `requires` fails. Apply the artifact to the target with `migrate apply`, passing the returned `acks`.

```sh
.tmp/sqlglot schema compare --reference connection://dev --target connection://dev-0a > .tmp/compare.json
.tmp/sqlglot schema patch --reference connection://dev --target connection://dev-0a \
  --diff-hash "$(jq -r .diffHash .tmp/compare.json)" --select '["add","table","dbo","customers",""]' > .tmp/patch.json
jq .artifact .tmp/patch.json > .tmp/artifact.json
.tmp/sqlglot migrate apply --dsn connection://dev-0a .tmp/artifact.json
```

Repeat `--select` once per change ID; each value is taken whole, never split on commas. Over HTTP, `select` is a JSON array of IDs.

PostgreSQL and SQLite comparisons cover one schema with the same name on both sides. SQL Server accepts several `--schema` values. On SQLite, a change that forces a table rebuild requires every other change to that table, because the rebuild applies the table's full desired definition. Once a partial artifact is applied, `already-applied` and the postcondition expect exactly the unselected changes to remain.

### Run history

Every `schema inspect`, `schema compare`, `schema patch`, `migrate plan` and `migrate apply` is recorded with its result, or its error, in a local SQLite file, `$XDG_DATA_HOME/sqlglot-go/history.db` (override with the global `--history` flag). A comparison records the comparison and the inspection of each side (`<run>/reference`, `<run>/target`); a patch names the comparison it was reviewed in (`--comparison`). Each compare and patch runs as one clicky task group whose id is the run id (`--run`, a new UUID by default), and its task progress and logs are stored with it. Recorded runs are read back without contacting any database, so a comparison stays reviewable after its databases go offline. Connections are recorded as `connection://` references or SQLite paths whole; other DSNs lose their password and query, and keyword DSNs are withheld.

```sh
.tmp/sqlglot history list --kind comparison --connection connection://dev-0a
.tmp/sqlglot history get <run-id> --format json | jq .payload.diffHash
```

`sqlglot ui` serves the same workflow in a browser: manage connections, pick the reference and target, tick the changes to carry, and download the patch. Each comparison and patch streams its task progress over clicky's `/api/v1/tasks/stream` while it runs, opens from history by its run id afterwards, and the History page lists every recorded run, offline. Over HTTP, history is `GET /api/v1/history` and `GET /api/v1/history/{id}`. `make build` and `make install` embed the UI. For frontend work, `sqlglot ui --dev` (run from the repository root) starts Vite over `www/` with `@flanksource/clicky-ui` resolved to the sibling `../clicky-ui` checkout and serves it through the same address; `SQLGLOT_LOCAL_UI=0` selects the published package instead.

## Reviewed schema scope

SQL Server, PostgreSQL and SQLite plan through `migrate/engine` and write the same version 2 artifact: dialect, capabilities fingerprint, scope, the baseline and desired schemas, the changes, the units of ordered SQL with their preconditions, and a SHA-256 hash of all of it. All use the same CLI commands. See [reviewed migration artifacts](../migrate/ARTIFACTS.md) and the [plan file guide](../docs/guide/migrate/plan-files.md).

PostgreSQL holds a transaction-owned advisory lock for each unit, with a lock timeout. SQLite uses one pinned connection for pragma changes, `BEGIN IMMEDIATE`, the unit's statements, foreign key checks, commit or rollback and pragma restoration. SQL Server holds an application lock and runs each unit in one transaction. A unit whose objects already match the artifact's desired state is skipped, so applying a plan file again resumes after the units that committed; objects that match neither state are drift and fail the unit.

Inspection refuses, object by object, what a dialect cannot reproduce faithfully (for example PostgreSQL partitioning or inheritance, domains, row security, SQLite virtual tables, SQL Server computed columns), and a refusal blocks a plan only when a change or a forced rebuild touches that object. Check expressions compare as parsed SQL, so rendered parentheses are not an edit.

Bundles containing scripts or security declarations produce advisory schema previews. Advisory artifacts cannot execute. Full reviewed PostgreSQL script/security phases, completion records and retry semantics are tracked separately. The existing library `migrate.Apply` retains its complete PostgreSQL script and security lifecycle; callers of that direct API are responsible for authorizing execution.

## Library use

`inspect.Inspect(ctx, db, driver, Limits{})` accepts a caller-owned `*sql.DB` and returns an uncached catalog. `dialect.Inspect` returns a supported migration snapshot. `engine.PlanLive(ctx, db, engine.PlanLiveRequest{Dialect, Scope, Desired, Selection})` plans against the live database, read-only; `Selection` limits it to a subset of change IDs. `engine.Plan` plans offline from two schemas. `compare.Compare` and `compare.BuildPatch` back the CLI comparison commands. `engine.Apply` requires `ApplyRequest{Artifact, Hash, Acknowledgements, LockTimeout}`, and `workflow.Plan`, `Review` and `Apply` wrap the same steps for the CLI and the web UI. None closes the caller's pool or changes its pool settings.

Run the [SQLite example](../examples/database/main.go) with `go run ./examples/database`. It uses a disposable directory, inspects, plans, applies and repeats an artifact, then removes the directory. The adjacent `schema.hcl` is a PostgreSQL CLI example.

## Verification

```sh
GOWORK=off go test ./database/... ./cmd/sqlglot ./migrate/...
GOWORK=off go test -tags integration -count=1 ./database/migrate/scripts/sqlserver/integration ./database/migrate/csvdata/integration
GOWORK=off make build
GOWORK=off make lint
GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 make build-database-tests
bash hack/database-integration.sh postgres
bash hack/database-integration.sh sqlserver
```

Install golangci-lint v2.15.0 for the lint target. The integration harness uses disposable containers with no published ports, network access or host mounts, copies Linux test binaries into them and removes them on exit. It works with a remote Docker context. Build the test binaries for the Docker server architecture; the SQL Server image requires amd64. For an externally managed disposable server, run integration-tagged dialect tests with `SQLGLOT_TEST_POSTGRES_URL`, or SQL Server tests with `COMMONS_DB_SQLSERVER_URL`. These tests create and remove isolated databases.

The harness rebuilds its test binaries automatically. Set `SQLGLOT_DOCKER_ARCH=arm64` for PostgreSQL on an ARM64 Docker server; the default is amd64. Migration advisory locks coordinate cooperating reviewed executors. Other DDL writers must be serialized with deployment execution.
