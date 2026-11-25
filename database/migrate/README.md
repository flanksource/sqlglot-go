# HCL migrations

This package owns the HCL and script migration engine extracted from commons-db. See [standalone database documentation](../README.md) for the unified CLI, read-only bundle preview and reviewed PostgreSQL/SQLite schema artifacts. commons-db retains compatible API shims.

`migrate.Apply` is the single filesystem-backed migration seam for PostgreSQL, SQLite and SQL Server. The connection string selects the target, while callers pass the same embedded Atlas HCL bundle and options. SQL Server bundles are SQL Server-shaped rather than portable; see [SQL Server](#sql-server).

```go
err := migrate.Apply(ctx, dsn, schemaFS,
    migrate.WithDir("migrations"),
    migrate.WithName("runtime"),
)
```

## Target selection

PostgreSQL accepts `postgres://`, `postgresql://`, and keyword connection strings such as `host=localhost dbname=runtime sslmode=disable`.

SQLite accepts `sqlite://` URLs and plain file paths ending in `.db`:

```go
err := migrate.Apply(ctx, "sqlite://state/runtime.sqlite", schemaFS, migrate.WithDir("migrations"))
err := migrate.Apply(ctx, "/var/lib/example/runtime.db", schemaFS, migrate.WithDir("migrations"))
```

SQL Server accepts `sqlserver://` URLs in the go-mssqldb form, e.g. `sqlserver://user:password@host:1433?database=app`. `mssql://` is rejected so that one spelling reaches the migrator. ADO connection strings (`;`-separated pairs that name the server with `server`, `data source`, `address`, `addr` or `network address`, e.g. `server=db;user id=app;password=secret;database=acme`) and `odbc:` connection strings are SQL Server too, and are passed to go-mssqldb unchanged.

### Existing connections

`WithDB` runs a SQL Server or SQLite migration on a `*sql.DB` the caller already holds, with the connection string left empty. The dialect comes from the handle's driver. Apply neither closes the handle nor changes its pool settings:

```go
err := migrate.Apply(ctx, "", bundle,
	migrate.WithDB(writeDB),
	migrate.WithRoutines("routines"),
	migrate.WithName("acme/routines"))
```

Passing both a connection string and `WithDB` is an error. A PostgreSQL (lib/pq) handle is accepted for the default schema: each schema unit sets and restores its own `lock_timeout` on a dedicated connection. `WithSchema` with another schema needs a connection string, because the script phases and metadata tables resolve unqualified names through `search_path`, which only a connection the migration opens can scope.

Relative SQLite paths are resolved from the process working directory. SQLite is always file-backed; `:memory:` and `mode=memory` are rejected. The normalized connection enables foreign keys, a 5-second busy timeout, and WAL while preserving caller-supplied query parameters and unmanaged pragmas.

Unknown URI schemes written as `scheme://...` fail instead of being passed to PostgreSQL accidentally. Scheme-like strings without `://` are treated as PostgreSQL DSNs unless they are SQL Server ADO/ODBC strings or their path ends in `.db`; use one of the explicit forms above rather than relying on that ambiguity. A PostgreSQL keyword DSN remains PostgreSQL even when its `dbname` ends in `.db`.

`db.NewGorm` uses the same DSN resolver, so migration and application connections cannot disagree about the target.

## Portable canonical HCL

A cross-database bundle declares PostgreSQL-shaped Atlas HCL. PostgreSQL evaluates the declaration directly. SQLite evaluates the same declaration with the PostgreSQL evaluator, validates the resulting realm against the compatibility contract below, and then projects it to SQLite. Acceptance by the HCL parser alone does not mean an object is SQLite-compatible.

All `.hcl` files below `WithDir` are loaded recursively in lexical path order and evaluated as one realm. Every file may start with the same `schema "public" {}` block, as a tool that evaluates one file alone requires: identical `schema` blocks repeated across files are one schema, for PostgreSQL, SQLite and SQL Server alike. Two blocks of one name that differ are an error naming both files, and a bundle that declares two different schemas keeps the error of its dialect. `variable` blocks are supported on both targets when callers provide values through `WithVariables`; the resulting schema must still fit the portable set.

### Supported cross-database blocks

| HCL block | Supported portable form | SQLite behavior |
| --- | --- | --- |
| `variable` | Atlas variable declarations referenced by schema expressions | Evaluated before projection using `WithVariables` values |
| `schema` | Exactly one `schema "public" {}` | Projected to SQLite's `main` schema |
| `table` | One or more tables assigned to `schema.public` | Created with their declared portable columns and constraints |
| `column` | `type`, `null`, and a literal or `sql(...)` default | Type is mapped as documented below; default SQL is preserved verbatim |
| `primary_key` | Column references only | Preserved when the table is first created or safely rebuilt |
| `unique` | Named constraint over column references | Projected to a named unique index |
| `index` | Column references, optional `unique`, and optional `where`; default or explicit B-tree method only | Projected to a SQLite index; `where` is preserved verbatim |
| `foreign_key` | Local and referenced columns plus `on_update` and `on_delete` actions | Preserved when the table is first created or safely rebuilt |
| `check` | Named check with `expr` and no dialect attributes | Preserved verbatim when the table is first created or safely rebuilt |

Index expressions, per-column index attributes such as sort direction or operator classes, foreign-key attributes, and table or check attributes outside the forms above are not in the portable set.

### Supported portable column types

The following table lists the PostgreSQL type families accepted after HCL evaluation, not every type accepted by Atlas's PostgreSQL parser. Underscored names such as `character_varying` and `double_precision` are direct HCL aliases. Space-containing SQL spellings and the less common aliases noted below are accepted through `sql("...")` and normalize to the same family.

| PostgreSQL HCL type | SQLite declaration | Compatibility notes |
| --- | --- | --- |
| `uuid` | `text` | No SQLite UUID-format constraint is added |
| `text`, `char`, `character`, `varchar`, `character_varying`, `bpchar`, `name`; SQL spelling `character varying` | `text` | Length modifiers and PostgreSQL's internal `name` limit are not preserved |
| `json`, `jsonb` | `text` plus `json_valid(column)` | The generated check is named `<table>_<column>_json` |
| `date`, `time`, `timetz`, `timestamp`, `timestamptz`; SQL spellings `time with time zone`, `time without time zone`, `timestamp with time zone`, `timestamp without time zone` | `datetime` | Precision and timezone enforcement are not preserved by SQLite |
| `int2`, `int4`, `int8`, `int`, `integer`, `smallint`, `bigint`, `xid`, `xid8` | `integer` | PostgreSQL width and transaction-ID distinctions collapse to SQLite integer affinity |
| `bool`, `boolean` | `bool` | SQLite gives the declared `bool` type NUMERIC affinity; it has no native boolean type |
| `bytea` | `blob` | Binary values remain binary |
| `numeric`, `decimal` | `numeric` | Precision and scale modifiers are not preserved |
| `real`, `float`, `float4`, `float8`, `double_precision` | `real` | Precision modifiers and PostgreSQL width distinctions are not preserved |

Use only type modifiers whose loss is acceptable to the application, or enforce the invariant with a portable named `check`. Evaluated type families outside this table fail projection; they are not coerced to text.

### Portable example

```hcl
schema "public" {}

table "projects" {
  schema = schema.public

  column "id" {
    type = uuid
    null = false
  }
  column "payload" {
    type    = jsonb
    null    = false
    default = sql("'{}'")
  }
  column "created_at" {
    type    = timestamptz
    null    = false
    default = sql("CURRENT_TIMESTAMP")
  }

  primary_key {
    columns = [column.id]
  }
}

table "events" {
  schema = schema.public

  column "id" {
    type = uuid
    null = false
  }
  column "project_id" {
    type = uuid
    null = false
  }
  column "event_key" {
    type = text
    null = false
  }
  column "active" {
    type    = bool
    null    = false
    default = false
  }

  primary_key {
    columns = [column.id]
  }
  foreign_key "events_project_id_fkey" {
    columns     = [column.project_id]
    ref_columns = [table.projects.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }
  index "events_active_key" {
    unique  = true
    columns = [column.project_id, column.event_key]
    where   = "active"
  }
  check "events_event_key_nonempty" {
    expr = "length(event_key) > 0"
  }
}
```

Defaults, checks, and partial-index predicates are SQL strings, not a portable expression language. `commons-db` does not translate them. Every expression in a cross-database bundle must be accepted with the intended meaning by both PostgreSQL and SQLite. `CURRENT_TIMESTAMP` and `sql("'{}'")` are portable examples; PostgreSQL-only casts such as `::jsonb`, functions such as `now()`, operators, and JSON expressions must not be assumed portable.

### SQLite rejection list

SQLite rejects the following before schema mutation:

| Incompatible declaration or option | Reason |
| --- | --- |
| No table declarations | The SQLite reconciler requires at least one declared table |
| More than one schema, or a schema other than `public` | A portable bundle maps exactly one canonical schema to SQLite `main` |
| Realm or schema attributes and objects, including enums, domains, extensions, sequences, views, functions, and procedures | There is no defined lossless projection |
| Table triggers | Trigger SQL is dialect-specific |
| Column types outside the supported type table | The projector never guesses an affinity |
| Column attributes such as generated or identity behavior | PostgreSQL attributes cannot be preserved by the current projection |
| Table attributes other than plain named checks | Storage parameters, partitioning, and other PostgreSQL metadata are not portable |
| Check attributes | Only the check name and expression are portable |
| Non-B-tree index methods | SQLite cannot preserve PostgreSQL access-method behavior |
| Expression indexes or index-part attributes | Only direct column references are portable |
| Foreign-key attributes beyond columns, references, and actions | PostgreSQL-only metadata would be lost |
| A user check named `<table>_<json-column>_json` with a different expression | That name is reserved for the generated `json_valid` check |
| Any `.sql` migration file | SQL phases are PostgreSQL-only; SQLite does not skip them silently |
| `role` or `permission` blocks | Managed security is PostgreSQL-only |
| `WithExclude(...)` naming a member of a table (`table.column`) | A rebuild cannot keep a column, index or key the plan cannot see; whole tables may be excluded |
| `WithSchema(...)` with a value other than `public` | SQLite has no PostgreSQL schema namespace |

These declarations are parser-valid PostgreSQL HCL but are not SQLite-compatible: bit strings, network and MAC types, spatial types, intervals, serial and identity types, XML, money, arrays, ranges and multiranges, full-text search types, enums, domains, and other user-defined types.

### Accepted declarations with different SQLite semantics

Some declarations are accepted because their storage representation is useful on both targets, but SQLite cannot enforce every PostgreSQL semantic:

- UUIDs are stored as text without a canonical-format check.
- String length, numeric precision and scale, floating-point width, and time precision are normalized to SQLite affinities.
- `json` and `jsonb` both become validated JSON text; PostgreSQL JSONB indexing and operators are not provided.
- Date and time families become `datetime`; SQLite does not enforce PostgreSQL timezone or temporal-type distinctions.
- Integer widths collapse to SQLite integer affinity.
- Boolean declarations have NUMERIC affinity and do not restrict stored values to `0` or `1`; add a portable check when that invariant matters.
- Defaults, checks, foreign-key actions, and partial-index predicates are carried as written. Syntax or semantics that differ between engines fail at apply time or behave according to the target engine.

### SQLite migration-time limits

PostgreSQL and SQLite bundles are planned by the shared planner (`migrate/engine`): the HCL is evaluated with its variables, projected to the dialect, diffed against the live database and applied one unit (a table with its dependents) per transaction. A table rebuilt to change a column keeps its rows. Nothing undeclared is dropped by default, on either dialect: omitted columns, indexes, keys, checks, tables and types stay, and a rebuild keeps them in the rebuilt table, so valid changes in the same migration still apply.

`WithDrops()` makes the bundle the complete description of its schema: drops are planned for every undeclared table, type, column, index, key and check, and the unreviewed `Apply` acknowledges them. On SQLite it inspects all user tables, so user tables omitted from the bundle are removed. The bundle's own `schema_migration_*` tables are never dropped. A rename represented by Atlas as a column removal and addition preserves the old column by default; this API does not infer rename mappings.

`WithExclude(...)` hides objects from the plan on both sides (live and declared), so they are neither created, altered nor dropped, even under `WithDrops()`.

Each unit disables foreign-key enforcement while applying changes, verifies that no new foreign-key violations appear before commit, and restores enforcement afterward. An incompatible value or constraint rolls the unit back. A rebuild that would discard an unmanaged trigger is refused. SQLite's native column-addition rules still apply, and the migrator does not backfill values. Views remain outside the table bundle; callers must assess views that depend on changed columns. Rebuilds no longer require the removed `WithRebuilds()` option; remove it from existing callers.

## PostgreSQL schema-scoped bundles

`WithSchema` applies one bundle inside a normalized PostgreSQL schema and leaves the default behavior on `public` unchanged:

```go
err := migrate.Apply(ctx, dsn, schemaFS,
    migrate.WithName("runtime"),
    migrate.WithSchema("agent_tenant_context"),
)
```

The schema name must be a lowercase PostgreSQL identifier no longer than 63 bytes. The migration creates the schema and scopes Atlas inspection, HCL objects, SQL migration metadata, dependent-view handling, and managed security to it. A reusable HCL bundle still declares only `schema "public" {}`; migration remaps that declaration to the selected schema.

SQL files run with the selected schema as `search_path`. Keep objects owned by the bundle unqualified so the same bundle can target different schemas. Explicitly qualified SQL remains explicit and is not rewritten.

## PostgreSQL SQL phases and security

SQL files default to the post-Atlas phase. Directives are read from the leading comment header:

```sql
-- phase: pre
-- dependsOn: 001_extensions.sql
-- runs: always
-- transaction: false
```

- `phase` is `pre` or `post`; omitted means `post`.
- `dependsOn` uses paths relative to the migration root. Dependencies are topologically ordered and changes rerun all transitive dependents.
- Scripts are transactional by default; the SQL and its SHA-256 migration log commit atomically.
- `runs: always` re-runs a script on every apply. It is an escape hatch for reconciliation that truly must occur at every boot, not a replacement for dependency tracking.

Views that read a column the schema step retypes are dropped and re-created automatically around the change, from their live definitions, with their owner, options, comment and grants, whether a script owns them or not. A script's recorded hash is left alone, so it does not re-run only because a column changed; edit the script when the view itself should change. Prefer small hash-gated files, such as one file per view or shared dependency, so steady-state startup remains DDL-free.

Atlas-style PostgreSQL `role` and `permission` blocks may live in the same HCL files as tables. The OSS migration layer supports roles and memberships plus schema, table, column, and sequence permissions. Managed grants are reconciled exactly within the bundle's metadata scope; unrelated roles and grants remain untouched. Different scopes must not manage the same role or grantee/object pair.

## Reusable PostgreSQL test templates

`migrate.NewProvisioner` adapts an HCL and SQL bundle to `dbtest.Options.Provisioner`:

```go
handle := dbtest.ForT(t, dbtest.Options{
    Name:    "query_sessions",
    LogName: "query-sessions-test",
    Provisioner: migrate.NewProvisioner(
        querySchema,
        migrate.WithDir("migrations"),
        migrate.WithName("query"),
    ),
})
```

The provisioner fingerprints the migration name, directory, Atlas options, variables, and every HCL and SQL file. `dbtest` prepares one sealed PostgreSQL template for that fingerprint, clones an isolated database for each call, and keeps the template for later tests. Normal hash-gated SQL is already present in the clone; `runs: always` scripts and security reconciliation still run for every instance.

This option requires database creation. `dbtest` fails before invoking the provisioner when `COMMONS_DB_CREATE=false` because that mode targets the configured database directly.

## Programmatic SQLite declarations

The `migrate/sqlite` subpackage remains the lower-level seam for callers that already own a `*sql.DB` or `*sql.Tx` and construct Atlas `*schema.Table` declarations in Go. Its `ReconcileTables` function uses the same additive-only policy but does not load or project HCL.

```go
declared, err := sqlitetable.Table{Name: "events", Columns: columns, StoredAs: stored, PrimaryKey: []string{"id"}}.Declare()
if err != nil {
    return err
}
if err := sqlitemigrate.ReconcileTables(ctx, tx, sqlitemigrate.ReconcileOptions{}, declared); err != nil {
    return err
}
```

The record store uses this lower-level API to add newly declared columns to tables that may already contain rows. New filesystem-backed migrations should call the target-aware parent `migrate.Apply` instead.

## SQL Server

### Reviewed CSV data

Plan against a saved SQL Server connection. Review the counts and selected files in `review.json`, then apply the plan file with the same source root and filters. This example assumes the review lists zero deletes.

```sh
sqlglot migrate plan --csv-root ./data --dsn connection://example \
  --layer 'country=none,BW' --layer 'env=!dev' --out ./review.json
sqlglot migrate apply --csv-root ./data --dsn connection://example \
  --layer 'country=none,BW' --layer 'env=!dev' ./review.json
```

For a plan with deletes, review the count for each table and pass each exact acknowledgement reported by the preview using repeatable `--ack` flags. The artifact contains source digests, selected files, filters, and operation counts. Source CSVs stay in the import root. The HTTP workflow accepts uploaded files with `source.kind: csv` and a `layerFilters` map. Apply takes both the artifact and the uploaded files.

#### Select files and layers

Place this `AS.csv.yaml` at the import root to map an example `AsCode.csv` and its country and environment overlays.

```yaml
version: 1
defaults:
  schema: dbo
  tablePattern: '^(.*/)?(?P<table>AsCode)(_[A-Z]{2})?\.csv$'
  delimiter: "~"
layers:
  country:
    values: [BW, KE]
    match: ["**/*_${value}.csv"]
    valuePattern: "[A-Z]{2}"
  env:
    values: [dev, qa, prod]
    match: ["overrides/${value}/**/*.csv"]
tables:
  dbo.AsCode:
    key: [CODENAME, CODEVALUE]
```

Use `tables.<schema.table>.sources` for exact paths or globs with different names. Use a sidecar such as `lookup.csv.yaml` with `version: 1` and `table: dbo.Lookup` to route a single file. Settings inherit in order from defaults, the table, and the sidecar. `--csv-phase` selects the tables with the matching YAML `phase`, which defaults to `pre-rules`. By default apply processes `pre-rules` first and `post-rules` second, committing each separately. Plan and apply each phase separately to schedule rule deployment between them.

Layer filters are independent. `none` selects untagged files, `*` selects every value, and `!value` excludes a value. Within a layer, a comma-separated filter includes the union of positive terms and subtracts exclusions. Files pass every layer. Use `--layer name=filter` for additional layers. An overlay row supersedes a less specific row when its tags include every tag of that row. Incomparable records for the same key require a more specific record or a narrower filter.

Unquoted `\N` defaults to SQL NULL. Quoting preserves it as text. An unquoted blank follows `values.blank` (`empty-string`, `sql-null`, or `error`). A quoted empty field always means an empty string. Configure alternatives through `values.nullTokens`, `values.nullTokenCase`, and per-column settings under `values.columns`.

The optional `$delete` column deletes the exact key on `true`. Missing, blank, and `false` mean upsert. Under the default `delete: explicit`, unmatched database rows remain. A table using `delete: snapshot` must list every untagged source in `snapshotFiles` and retain a nonempty effective dataset after filtering.

#### Bulk staging and replay

Plan and apply use SQL Server bulk copy to stream input into transaction-local staging tables. SQL Server converts the staged values to the destination column types before comparing rows. Apply revalidates the reviewed inputs and target state under its migration lock. If the source or target state changed, generate and review a fresh plan. After a successful apply, a fresh plan shows zero writes when the data matches.

In a three-table SQL Server clone with 17,900 selected CSV rows, median native apply time across three runs fell from 6.07 to 1.97 seconds when rows already matched, and from 12.73 to 2.62 seconds for 487 inserts and 475 updates. The final row hashes matched across both staging implementations. These timings cover apply with an existing reviewed artifact.

### Schema bundles

`sqlserver://` targets reconcile through the Atlas SQL Server driver in `atlas/sqlserver`, which commons-db ships because Atlas OSS has none. A bundle declares tables in SQL Server-shaped HCL and stored procedures and functions as plain `.sql` files:

The driver delegates its differ, SQL Server types and migration planner to `github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver`. Commons-db owns catalog inspection, HCL loading, connections, locks, transactions and applying the resulting SQL. Consumers that need a database-independent typed plan can register sqlglot's pure `migrate/sqlserver` dialect and call `engine.Plan`; this does not expand the HCL or inspection support described below.

```go
//go:embed db
var bundle embed.FS

err := migrate.Apply(ctx, "sqlserver://app:secret@db:1433?database=acme", bundle,
    migrate.WithDir("db"),
    migrate.WithRoutines("db/routines"),
    migrate.WithName("acme/orders"),
)
```

### Routines come from `.sql` files

`WithRoutines(dir)` declares one routine per `.sql` file under `dir`. Each file holds exactly one `CREATE [OR ALTER] PROCEDURE|FUNCTION` batch, optionally followed by `GO`, and is named after the routine it creates (`acme_count_orders.sql` creates `acme_count_orders`). The file is the routine's only source: there are no procedure or function HCL blocks. It runs as written in SSMS or sqlcmd, and migrate runs the same text, with `CREATE` rewritten to `CREATE OR ALTER` and every other byte kept, as its own batch.

A routine is compared with the live `sys.sql_modules` definition through sqlglot's canonical T-SQL: comments, whitespace, keyword case, `[brackets]` and `OR ALTER` do not count as changes, while a literal or any other token does and produces one `CREATE OR ALTER`. A definition, declared or live, that sqlglot cannot parse is an error naming the routine, never a guessed change. Routine files are never hash-gated SQL scripts.

A routine that is removed from the bundle is left in the database; migrate never drops procedures or functions.

### Tables from HCL

`.hcl` files under `WithDir` are optional when the bundle has routines. They declare exactly `schema "dbo" {}` and its tables:

| Block | Supported form |
| --- | --- |
| `column` | `type`, `null`, `default` (a number, a string, or `sql("getdate()")`), `default_name = "DF_x"` (the default constraint's name, instead of `DF_<table>_<column>`), `collate = "..."`, and an `identity { seed, increment }` block |
| `primary_key` | `columns`, optional name, `clustered = false` for a nonclustered key (clustered by default) |
| `index` | `columns` (with `desc` parts), `unique`, `constraint = true` (a UNIQUE constraint rather than a unique index; no `where` or `include`), `clustered = true` (nonclustered by default), `include = [column.x]`, `where = "..."` |
| `foreign_key` | `columns`, `ref_columns`, `on_delete`, `on_update` |
| `check` | `expr` |

Types: `bit`, `tinyint`, `smallint`, `int`, `bigint`, `decimal(p,s)`, `numeric(p,s)`, `money`, `smallmoney`, `float(n)` (stored as `real` up to 24 bits), `real`, `char(n)`, `varchar(n)`, `nchar(n)`, `nvarchar(n)`, `text`, `ntext`, `binary(n)`, `varbinary(n)`, `image`, `date`, `time(p)`, `datetime`, `datetime2(p)`, `smalldatetime`, `datetimeoffset(p)`, `uniqueidentifier`, `xml`, `sql_variant` and `rowversion` (`timestamp`). A `(max)` size is written with `sql()`: `type = sql("nvarchar(max)")`. Defaults are named `DF_<table>_<column>`; changing one drops the live default constraint, whatever its name, and adds the new one. String defaults on `nchar`/`nvarchar`/`ntext` columns are written as `N'...'`.

`sqlglot schema inspect --hcl` writes the HCL of tables a database already has, and `ExportHCL` in `migrate/sqlserver` (read from a database by `dialect/sqlserver.ExportHCL`) does the same from a `migrate.Schema`. It refuses, naming the table, the member and the attribute, what this HCL cannot declare (ROWGUIDCOL, index fill factor and the other index options, NOT FOR REPLICATION, a foreign key to a table outside the export), unless `OmitUnsupported` leaves it out with one comment line each.

### Scoped inspection

An OIPA-sized database holds thousands of objects, so migrate inspects only the tables and routines the bundle names. Undeclared objects are never read, and so never changed. Inside declared tables, columns, keys, foreign keys and checks are reconciled to the declaration, but indexes the bundle does not declare are kept, so tuning indexes added by a DBA survive. An apply that finds nothing to change runs no DDL. Every apply holds a session `sp_getapplock` named after `WithName` (one-minute wait), so concurrent applies of one bundle serialize.

### The schema step is a reviewed artifact

The tables and routines of a bundle are planned together, in one declared-scope batch, by the shared schema engine (`migrate/engine`, dialect `sqlserver`): `PlanSchema(ctx, db, fsys, opts...)` returns that `engine.Artifact` without changing anything, and `Apply` and `PendingSQLServer` embed it as `bundle.schema` of the plan file (version 3). Dependents of a changed column are dropped and re-created around it, and the artifact lists each forced `rebuild` with its cause, the data guards (a `varchar(36)` column becoming `uniqueidentifier` is refused while a value is not a GUID) and the drops it left alone because nothing undeclared is ever dropped. The schema step runs between the pre and post scripts, one transaction per unit, with a 15 second lock timeout (`WithSchemaLockTimeout` on `scripts/sqlserver.Apply` changes it). Pre scripts that ran before a failing unit stay committed and recorded. A database that moved since the plan, or whose capabilities changed, makes the plan file stale (`*StalePlanError`); a unit that already committed is skipped when the plan file is applied again. A version 2 plan file without a schema step still applies; one with schema statements is refused and must be planned again.

### Scripts are sqlcmd files

Every other `.sql` file under `WithDir` (outside the routine directory) is a hash-gated script with the same `-- phase:`, `-- dependsOn:`, `-- runs: always` and `-- transaction:` headers as on PostgreSQL. It is plain T-SQL that runs unchanged with `sqlcmd -i` or in SSMS: migrate splits it at `GO` on its own line (never inside a string or comment) and sends each batch in turn. sqlcmd commands (`:setvar`, `:r`, `:on error`, `!!`) and `$(var)` scripting variables, which sqlcmd substitutes even inside strings, are refused, so a file never means something different here than in sqlcmd. `pre` scripts run before the table and routine reconcile, `post` scripts after, all under the bundle's `sp_getapplock`. A transactional script runs every batch and records its SHA-256 in `dbo.sqlglot_migrations` (created on first use) in one transaction, the record appended to its last batch unless that batch defines a module or may end early, under `SET LOCK_TIMEOUT 15000`; deadlocks (1205) and lock timeouts (1222) are retried. Apply plans the bundle into a `sqlserver.PlanFile` and runs exactly that file, refusing it as stale when the target moved during the apply. `PendingSQLServer` reports what an Apply would do without executing anything: the reconcile statements, each script it would run with why (`new`, `changed`, `dependency` or `always`), and the plan file itself (`Plan`).

### Merging ledger scopes

The script ledger in `dbo.sqlglot_migrations` is kept per scope (the bundle's `WithName`, or `scripts/sqlserver.Options.Scope`), so two bundles on one database never see each other's runs. A caller that wants several scopes to count as one, for example a database that received the same scripts from different repositories under one scope each, sets `Options.AbsorbScopes` to accept those scopes. Plan then reads the newest row of each path across `Scope` and the accepted scopes (by `updated_at`, then hash), so a script whose body differs from what ran last on the database is re-run whichever scope recorded that run. The plan file lists the scopes in `absorbed`, and its stale check covers their rows. Apply, under the scope's migration lock and in one transaction, keeps the newest row of each path, re-keys it and the scopes' attempts to `Scope`, and drops the absorbed scopes' observation snapshots. Later plans find nothing left to absorb.

### Compatibility level 100

Generated DDL and the catalog queries avoid syntax newer than compatibility level 100 (no `STRING_AGG`, `OPENJSON` or `DROP ... IF EXISTS`). `CREATE OR ALTER` depends on the engine version (2016 SP1+), not the database's compatibility level.

### Refused on SQL Server

These fail with an error instead of being ignored:

- sqlcmd commands in scripts (see above);
- `WithSchema` other than `dbo` and `WithDrops`;
- `role` and `permission` blocks;
- `view`, `trigger`, `sequence`, `function` and `procedure` HCL blocks, computed columns, user-defined and alias types (including `sysname`), and any unknown column, index or table attribute;
- declared tables that the database has partitioned or carries triggers on, and IDENTITY changes on an existing column.
