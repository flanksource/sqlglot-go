# Command line

SQL commands accept one positional SQL argument, `--file`, or standard input. Use `--dialect` (or `-d`) to select a dialect; `neutral` names the default. List the available names with `sqlglot dialects`.

## Parse

```sh
sqlglot parse --dialect tsql 'SELECT TOP 2 name FROM users'
sqlglot parse --format json --dialect postgres 'SELECT id FROM users LIMIT 2'
```

The default output includes formatted SQL and the expression class. JSON output includes the reference-shaped expression tree.

For T-SQL scripts, the parser scans `GO` batches and reports unsupported statements with file, line, column, and a bounded SQL excerpt. It exits nonzero if any statement is unsupported. From a checkout:

```sh
sqlglot parse -d tsql --file testdata/tsql-routines/if_begin_else.sql
```

## Validate a query

```sh
sqlglot validate --dialect postgres 'SELECT id FROM users'
```

`validate` requires one `SELECT`, `UNION`, `INTERSECT`, or `EXCEPT` query and reports its class. It does not check execution, schema names, or read-only policy. Queries can have side effects.

## Format and tokenize

```sh
sqlglot format --dialect tsql 'select top 2 name from users'
printf 'SELECT id FROM users' | sqlglot tokens --format json
```

`format` changes whitespace and can handle SQL outside the parser's supported grammar. Formatting is not a parsing or validation check.

## Database commands

Use `sqlglot schema inspect` to read a catalog or export a migration snapshot. See [Set up database connections](./connections) for DSNs and saved connection files, and [Database inspection](./inspection) for a local walkthrough.

## Migrate

The [migration guides](./migrate/index.md) include separate walkthroughs for [HCL schema migrations](./migrate/hcl), [SQL script migrations](./migrate/sql), and [CSV data migrations](./migrate/csv).

Migrations work terraform-style. `sqlglot migrate plan` compares a desired schema or dataset with a target, writes a plan file (`--out`, `.sqlglot-go/plan.json` by default), prints a summary of what will change to stderr and the structured plan to stdout. `sqlglot migrate review PLAN` opens a plan file offline and prints the same summary. `sqlglot migrate apply PLAN` runs it: the file's own hash guards only its integrity, so there is no separate approval, and apply refuses the file with `plan is stale … run sqlglot migrate plan again`, running nothing, when the target changed since the plan. PostgreSQL, SQLite and CSV plans still take one `--ack` per destructive step.

### Choose a source

| Flag for `plan` | Contents | Target |
| --- | --- | --- |
| `--desired FILE` | A single Atlas HCL file in the target dialect or a complete JSON snapshot from `sqlglot schema inspect --snapshot` | PostgreSQL and SQLite accept HCL or a matching snapshot. SQL Server requires a snapshot. |
| `--bundle DIR` | Atlas `.hcl` files loaded recursively as one schema declaration, plus optional `.sql` files | PostgreSQL, SQLite and SQL Server. Script and security support varies by target. |
| `--csv-root DIR` | CSV sources and `*.csv.yaml` mapping files | SQL Server reviewed data import. |

Use `--dsn` to select the target by DSN, SQLite file path, or `connection://name`. `--driver` can override driver detection. The default target schema is `public` on PostgreSQL, `main` on SQLite and `dbo` on SQL Server. Use `--schema` to select an existing PostgreSQL schema. SQL Server HCL bundles are scoped to `dbo`.

To use another database as the desired schema, first export a complete snapshot from the reference, then plan against the target. The example uses two saved SQL Server connections and their default `dbo` schema.

```sh
sqlglot schema inspect --dsn connection://warehouse-reference --snapshot > ./desired.json
sqlglot migrate plan --dsn connection://warehouse-target --desired ./desired.json --out ./reviewed.json
```

### Plan and apply an HCL bundle

In a new working directory, create an example SQLite database using `sqlite3`.

```sh
sqlite3 ./inventory-target.db 'CREATE TABLE items (id INTEGER NOT NULL PRIMARY KEY, label TEXT NOT NULL);'
mkdir ./bundle
```

Save this declaration as `bundle/schema.hcl`. It adds a nullable `notes` column to `items`.

```hcl
schema "public" {}

table "items" {
  schema = schema.public

  column "id" {
    type = integer
    null = false
  }
  column "label" {
    type = text
    null = false
  }
  column "notes" {
    type = text
    null = true
  }

  primary_key {
    columns = [column.id]
  }
}
```

PostgreSQL and SQLite bundles use PostgreSQL-shaped HCL and declare `schema "public" {}`. PostgreSQL remaps that declaration when you pass `--schema`. SQLite projects supported HCL types and constraints onto `main`. With `--desired`, supply HCL in the target dialect and declare the target schema directly.

Bundle variables are declared with HCL `variable` blocks and supplied with repeatable `--var 'name=HCL-literal'` flags, for example `--var 'label="Inventory"'`. A bundle never drops an omitted table by default, and `--allow-drops` considers omitted tables for removal. A table a bundle declares is complete unless its block says `partial = true`: the omitted columns, indexes, keys and checks of a complete table appear as destructive changes to review, and a partial table keeps them. Review the resulting destructive changes. Treat a `--desired` file as the complete desired schema. Its omissions can generate drops without `--allow-drops`.

Plan against the existing database. The summary goes to stderr and the plan file to `.sqlglot-go/plan.json`; inspect it with `jq`.

```sh
sqlglot migrate plan --dsn ./inventory-target.db --bundle ./bundle
jq '{hash: .artifact.hash, changes: [.artifact.changes[].id], advisory}' .sqlglot-go/plan.json
```

The bundle artifact is nested under `artifact` for every dialect, and under `scriptPlan` for a SQL Server bundle with scripts, whose schema step holds the same artifact. A plan from `--desired` writes the artifact at the top level. Stdout carries the same JSON as the plan file.

After reviewing the SQL, apply the plan file. The example adds a nullable column and requires no destructive acknowledgement.

```sh
sqlglot migrate apply --dsn ./inventory-target.db .sqlglot-go/plan.json
sqlite3 ./inventory-target.db 'PRAGMA table_info(items);'
```

Take acknowledgement IDs from the plan summary or the `acks` of `sqlglot migrate review`, and pass each complete ID once as its own `--ack` argument. For a SQL Server bundle with scripts, the summary shows the destructive schema statements and applying the file is their acknowledgement. `--lock-timeout` defaults to `5s` and accepts positive durations such as `30s`. A schema artifact reports `already-applied` when its intended state is already present. If the target changed since the plan, apply fails as stale and runs nothing: plan again.

### Add SQL scripts and headers

PostgreSQL and SQL Server bundles can contain SQL scripts alongside the HCL. Put directives in leading `--` comment lines, before the first SQL statement or block comment. The directive names are case-sensitive.

| Header | What it declares |
| --- | --- |
| `-- phase: pre` or `-- phase: post` | Place the script before or after schema reconciliation. The default is `post`. |
| `-- dependsOn: sql/010_seed.sql, sql/020_backfill.sql` | Declare these bundle-relative paths as prerequisites. A changed prerequisite makes its dependents pending, including indirect dependents. Missing paths, duplicate dependencies, cycles and dependencies from `pre` to `post` are rejected. Ready scripts in the same phase are ordered by path. |
| `-- runs: always` | Mark the script as pending every time. Rerunning an unchanged prerequisite alone leaves its declared dependents up to date. |
| `-- transaction: true` or `-- transaction: false` | Declare whether the script should run in a transaction. The default is `true`. |

For a PostgreSQL preview, reuse the HCL example and save the following as `bundle/010_items_view.sql`.

```sql
-- phase: post
CREATE OR REPLACE VIEW item_labels AS
SELECT id, label FROM items;
```

Save the dependent view as `bundle/020_item_report.sql`.

```sql
-- dependsOn: 010_items_view.sql
CREATE OR REPLACE VIEW item_report AS
SELECT id, label FROM item_labels;
```

Set `POSTGRES_URL` to a PostgreSQL target with an existing `public` schema that the planner can inspect.

```sh
sqlglot migrate plan --dsn "$POSTGRES_URL" --bundle ./bundle --out ./preview.json
sqlglot migrate review ./preview.json | jq '{scripts, warnings}'
```

`scripts` reports each file's path, phase, classification (`ddl`, `dml`, `mixed` or `unknown`), parse completeness and any diagnostics. A `pre` script classified as `ddl`, `mixed` or `unknown` adds a warning because planning uses the current database state. Planning executes no script bodies and creates no migration metadata.

A PostgreSQL script bundle, or a PostgreSQL bundle with `role` or `permission` declarations, produces an advisory preview that `migrate apply` refuses. Schema-only bundles produce executable plans on PostgreSQL, SQLite, and SQL Server, and SQL Server bundles with scripts produce an executable script plan.

On SQL Server, use SQL Server-shaped HCL with `schema "dbo" {}`. For routines, pass `--routines routines` to treat that bundle subdirectory as declarations. Each file must be named after the routine and contain one `CREATE [OR ALTER] PROCEDURE` or `FUNCTION` batch, optionally followed by `GO`. The planner compares parsed definitions with the live routines. Script files outside that directory accept `GO` batch separators. SQLCMD directives and `$(...)` variables are rejected.

### Lint SQL scripts

sqlglot has no lint command. Use [`uir lint`](https://github.com/flanksource/uir/blob/main/docs/lint.md), which parses each script with sqlglot and reports findings at their source location. For the example above, save this `.uir.yaml` next to `bundle/`.

```yaml
version: 1
sources:
  - match: bundle/**/*.sql
    dialect: postgres
```

Run `uir lint` from that directory. A statement sqlglot cannot parse is a `sql-parse` error, and error findings make the command exit nonzero. `dialect` accepts `tsql`, `postgres`, or `sqlite`. For SQL Server scripts, set `lint.compatibility_level` to report features that need a higher level as `sql-compatibility-level` errors. For example, `GENERATE_SERIES` needs level 160. Add `lint.rules` for project checks. Run `gavel lint uir` instead for Gavel baselines, snapshots, and the results UI.

### Plan and apply SQL Server scripts

A SQL Server bundle with scripts plans in one pass: the pre scripts, the schema statements that reconcile the declared tables and routines, then the post scripts. sqlglot derives what each script reads, writes and calls from its SQL. The summary looks like this.

```text
Plan for sql01/inventory (scope dbo:default)

Pre-deployment scripts
  ~ re-run   pre/010_asenv.sql  source-changed

Schema (1 declared objects)
  ~ ALTER TABLE [dbo].[acme_env] ADD [label] nvarchar(20) NULL

Post-deployment scripts
  + run      post/030_seed.sql  new
  ~ re-run   post/020_report.sql  read-changed
      ~ reads SELECT Country FROM dbo.AsEnv  "ZW" → "MW"
  ! blocked  post/040_legacy.sql  reconciliation required: partial
      ? effects unknown line 3: EXEC sp_executesql @sql

Warnings
  ! script post/050_index.sql runs outside a transaction: a failure leaves it partial, blocked until reconciled

Plan: 1 to run, 2 to re-run, 1 blocked, 4 unchanged, 1 schema statements (0 destructive).
```

`+ run` is a new script, `~ re-run` names why a script runs again (`source-changed`, `script-dependency-changed`, `function-result-changed`, `read-changed`, and `inputs-unrecorded` for a script whose version 1 content-only ledger row never recorded the inputs it now observes), `! blocked` names the blocker, and `? effects unknown` lists the statements whose effects could not be determined. `scriptPlan.actions` holds the same in JSON, and the file also records the target, the scripts with their SQL and effects, the ledger and every observed input.

A script is selected for its own source, inputs and reads as they are when the plan is made, never for what an earlier script in the plan will write; the next plan picks that up. After a script runs, apply runs each query it reads with and records the answer in the ledger. A plan runs the query again and selects the script with `read-changed` when the answer differs, listing the query, the recorded answer and the new one under the action's `reads`. A query with no recorded answer is listed but not run, and one that fails is shown with its error and selects nothing. A read that cannot run on its own, because it uses a changing variable, a temporary table or the date, or that a routine makes, is listed under `untrackedReads` and selects nothing.

`migrate apply` checks that the plan file is for the connected server and database, then re-reads the ledger and every observed input. A version 1 plan file, written before ledger scopes could be absorbed, is refused: re-run `sqlglot migrate plan`. Any difference fails as `plan is stale: <what moved>; run sqlglot migrate plan again` before anything runs. An input an upstream script writes is re-observed after that writer. After a script with unknown effects, apply re-plans; a script that is now blocked stops the apply as stale, with the scripts already committed recorded.

### Review a SQL Server CSV import

Plan a CSV import with `--csv-root`. Review the selected `files`, per-table insert and update counts, and `explicitDeletes` and `snapshotDeletes`. Apply the plan file with the same CSV root and filters. For each table with deletes, pass `--ack 'csv-delete:<schema.table>:<total-delete-count>:<deleteHash>'` using the sum of the two reviewed delete counts and the table's `deleteHash`.

Run `sqlglot migrate --help` for the `AS.csv.yaml` example, including table mappings, per-file sidecars, row keys, value policies, and delete modes. The commands below use that example's `country` and `env` layers and its `post-rules` table.

```sh
sqlglot migrate plan --dsn connection://inventory-sqlserver --csv-root ./data \
  --layer 'country=none,ZA' --layer 'env=!dev' --csv-phase post-rules --out ./csv-review.json
sqlglot migrate review ./csv-review.json | jq '{hash, files, tables}'
```

For a reviewed import with zero deletes, apply with the following command.

```sh
sqlglot migrate apply --dsn connection://inventory-sqlserver --csv-root ./data \
  --layer 'country=none,ZA' --layer 'env=!dev' --csv-phase post-rules ./csv-review.json
```

Repeat `--layer NAME=FILTER` for each layer you want to override. `none` selects untagged files, `*` selects every value and `!value` excludes that value. The default for each layer comes from `defaultFilter`, which defaults to `none`.

`--csv-phase` selects tables by the `phase` set in YAML, which defaults to `pre-rules`. Omit the flag to include both phases. Apply commits `pre-rules` first and `post-rules` second. To run rule deployment between them, plan and apply each CSV phase separately. Apply revalidates the CSV sources, filters, phase, and target rows covered by the artifact.
