Plan, review, and apply a database migration, terraform-style. `plan` compares the desired state with the target, writes a plan file and prints what will change. `review` reads a plan file offline. `apply` runs a plan file, refusing it when the target changed since the plan.

## Plan, review, and apply a schema bundle

```sh
sqlglot migrate plan --dsn connection://inventory --bundle ./db
sqlglot migrate review .sqlglot-go/plan.json
sqlglot migrate apply --dsn connection://inventory .sqlglot-go/plan.json
```

`plan` writes the plan file to `--out`, `.sqlglot-go/plan.json` by default, prints a summary of what will change to stderr and the structured plan to stdout. Review the SQL, any destructive changes, and warnings. Executable bundle plans wrap the artifact under `artifact` for PostgreSQL and SQLite, under `sqlServerArtifact` for SQL Server schema-only bundles, and under `scriptPlan` for SQL Server bundles with scripts. Plans from `--desired` and `--csv-root` put the artifact at the top level.

The plan file carries its own hash, which guards only its integrity: there is no separate approval. Repeat `--ack` once per reviewed destructive ID of a PostgreSQL, SQLite or CSV plan, quoting each complete ID as a single shell argument. PostgreSQL and SQLite report the IDs in `steps` marked `destructive`. A SQL Server script plan shows its destructive schema statements in the summary, and applying the file is their acknowledgement.

`--lock-timeout` accepts positive durations and defaults to `5s`. A schema artifact whose intended changes are already present reports `already-applied`. If the target changed since the plan, `apply` fails with `plan is stale`, naming what moved, and runs nothing: run `migrate plan` again.

## Migration sources

Set `--dsn` to a connection string, SQLite file path, or `connection://NAME`. The default schemas are `public` on PostgreSQL, `main` on SQLite, and `dbo` on SQL Server. Choose one source.

- `--desired FILE` takes Atlas HCL in the target dialect or a complete migration snapshot. SQL Server requires a snapshot. This input describes the complete desired scope, so omissions can generate drops.
- `--bundle DIR` loads HCL recursively as one declaration. Bundles preserve omitted tables and columns by default. Use `--allow-drops` to consider omitted objects for removal.
- `--csv-root DIR` plans a SQL Server import from CSV files and `*.csv.yaml` mappings.

To derive the desired schema from another SQL Server database, export a complete snapshot and plan against the target.

```sh
sqlglot schema inspect --dsn connection://warehouse-reference --snapshot > ./desired.json
sqlglot migrate plan --dsn connection://warehouse-target --desired ./desired.json --out ./reviewed.json
```

## Project source mappings

Place `.sqlglot-go.yaml` in the project directory. With no explicit source, `migrate plan` checks the current directory first and then the Git root. `migrate plan DIR` and `migrate plan --bundle DIR` use the configuration in that directory.

```yaml
version: 1
connections:
  dev: 'connection://inventory-dev'
  local: './inventory.db'
mappings:
  - match: 'schema/**/*.hcl'
  - match: 'migrations/**/*.sql'
    labels: {component: [billing]}
  - match: 'legacy/baseline.txt'
    transform: '{"hcl": file.content, "id": "schema/baseline.hcl"}'
ignore:
  - 'migrations/archive/**'
```

Run `sqlglot migrate plan --target dev` to use the configured sources and the `dev` connection. `--target` accepts an alias and is mutually exclusive with `--dsn`. Aliases can use existing `connection://` references or database DSNs. Relative SQLite paths such as `./inventory.db` resolve beside the project configuration.

Only matched files enter the bundle. Globs use case-sensitive, project-relative paths. An explicit `kind` overrides `.hcl` or `.sql` extension inference. For other extensions, the transform can determine the kind from its `sql` or `hcl` result key.

A `transform` is a gomplate CEL expression with `file.path` and `file.content`. It returns `sql` or `hcl` and can also set `id`, `labels`, and the SQL `name` and `phase`. An HCL ID is its logical `.hcl` path. A SQL ID is its migration key. Labels are named lists of strings included in structured plan output. A transform can replace a rule's label, and an empty list removes it.

For XML descriptors, `xmlpath(xml, selector)` requires one matching node and returns its text or attribute value. `read(path)` reads a companion file relative to the descriptor within the project. Ignore rules apply to both files. For example, `{"sql": read(xmlpath(file.content, "/root/entry[@name='SQLDATA']/@file")), "id": xmlpath(file.content, "/root/entry[@name='SQLSCRIPTGUID']").trim().lower()}` reads an OIPA deployment script and assigns its normalized GUID.

## HCL bundles

PostgreSQL bundles declare `public` and remap it to the existing schema selected with `--schema`. SQLite projects supported PostgreSQL-shaped HCL to `main`. SQL Server uses its own HCL types and declares `dbo`.

Save this example as `db/schema.hcl` for PostgreSQL or SQLite.

```hcl
schema "public" {}

table "items" {
  schema = schema.public
  column "id" {
    type = integer
    null = false
  }
  primary_key {
    columns = [column.id]
  }
}
```

Pass values for declared HCL variables with repeatable `--var 'NAME=HCL_LITERAL'` flags. For SQL Server, `--routines routines` loads one procedure or function per file from the bundle's `routines` directory. Name each file after its routine. The planner compares parsed definitions with the live definitions. Declared routines can be part of an executable schema artifact.

## SQL script headers

Put directives in leading `--` comment lines. PostgreSQL and SQL Server can preview script bundles. SQL Server scripts accept `GO` batch separators and reject SQLCMD commands and variables. A SQL Server script with no executable statements appears as skipped with a planning warning.

For a PostgreSQL example, save `db/010_items_view.sql` alongside the HCL file.

```sql
CREATE OR REPLACE VIEW item_labels AS
SELECT id FROM items;
```

Save its dependent view as `db/020_item_report.sql`.

```sql
-- phase: post
-- dependsOn: 010_items_view.sql
-- transaction: true
CREATE OR REPLACE VIEW item_report AS
SELECT id FROM item_labels;
```

- `phase` marks the script as `pre` or `post` schema reconciliation. The default is `post`.
- `dependsOn` accepts comma-separated paths relative to the bundle. A changed prerequisite makes its dependents pending, including indirect dependents. A `pre` script cannot depend on a `post` script.
- `runs: always` marks the script as pending every time. An unchanged prerequisite marked `always` leaves declared dependents up to date on that basis alone.
- `transaction` declares whether the script should run in a transaction. The default is `true`.

## Parsed dependency invalidation

The CLI preview reports each script's phase, classification (`ddl`, `dml`, `mixed`, or `unknown`), and parse diagnostics. Planning uses the current target state and skips execution of script bodies. A `pre` script classified as `ddl`, `mixed`, or `unknown` produces a warning.

A SQL Server bundle with scripts plans as one plan file under `scriptPlan`: its pre scripts, the schema statements and its post scripts, observed once. The summary lists `+ run` for new scripts, `~ re-run` with the reason (`source-changed` when the file changed, `script-dependency-changed` when a prerequisite changed, `read-changed` with the query and both answers when what it reads changed since it last ran), `! blocked` with the blocker and `? effects unknown` with the statements whose effects could not be determined. `scriptPlan.actions` holds the same in JSON. Apply re-reads every ledger row and observed input first and refuses a stale plan; after a script with unknown effects it re-plans, and a script that is now blocked stops the apply as stale. What runs is decided when planning: a write during the apply selects no other script, and the next plan picks up the readers it changed.

PostgreSQL phased SQL scripts and `role` or `permission` declarations produce advisory previews. `migrate apply` requires an executable schema, CSV or script plan.

## SQL Server CSV imports

### Map CSV files with YAML

Place `AS.csv.yaml` at the root passed to `--csv-root`. This example maps `ItemLabel.csv`, `ItemLabel_ZA.csv`, and overlays such as `overrides/qa/ItemLabel_ZA.csv` to the existing `dbo.ItemLabel` table. Use UTF-8 CSV with a header such as `ID~Label` and data rows such as `1~Notebook`.

```yaml
version: 1
defaults:
  schema: dbo
  tablePattern: '^(?:.*/)?(?P<table>ItemLabel)(_[A-Z]{2})?\.csv$'
  delimiter: '~'
layers:
  country:
    values: [ZA, KE]
    match: ['**/*_${value}.csv']
    valuePattern: '[A-Z]{2}'
  env:
    values: [dev, qa]
    match: ['overrides/${value}/**/*.csv']
tables:
  dbo.ItemLabel:
    key: [ID]
    phase: post-rules
```

- `defaults.tablePattern` matches paths relative to the CSV root and must have one named capture, `table`. `defaults.schema` supplies the inferred table's schema and defaults to the target schema. `tables.<schema.table>.sources` can list relative paths or globs for explicit routing. Plans report unmapped paths in `unmatched`.
- Use a sidecar such as `status.csv.yaml` beside `status.csv` with `version: 1` and `table: dbo.ItemStatus` to map a single file. File settings override table settings, which override `defaults`.
- `key` lists the columns used to match existing rows and defaults to the target table's primary key. Include those columns in every CSV header. `delimiter` defaults to `,`. `ignoreColumns` skips named CSV columns.
- `values.blank` accepts `empty-string` (the default), `sql-null`, or `error`. Unquoted `\N` means SQL NULL by default. Use `values.nullTokens`, `values.nullTokenCase` (`sensitive` or `insensitive`), and `values.columns.<column>` to customize decoding. Quotes preserve empty strings and literal NULL tokens.
- `delete: explicit` is the default. Set `$delete` to `true` on a row to delete its exact key. `delete: disabled` rejects delete rows. Table-level `delete: snapshot` deletes target keys absent from the selected dataset. List exactly the untagged base CSVs in `snapshotFiles` and retain at least one effective row to insert or update.

### Layer and phase selection

Use repeatable `--layer NAME=FILTER` with a name declared under `layers`. Each `match` pattern captures `${value}` from a relative path. `values` lists permitted tags, `valuePattern` constrains their spelling, and `defaultFilter` defaults to `none`. A file must pass every layer. `none` selects untagged files, `*` selects all values, and `!value` excludes a value. Commas combine selections in a layer. For the same key, an overlay supersedes a row when it matches all of that row's tags and adds at least one more. Duplicate rows with identical tags or competing overlays without a single most specific row fail planning.

`--csv-phase pre-rules` or `--csv-phase post-rules` selects CSV tables assigned to that phase in YAML. A table's selected files must agree on `phase`, which defaults to `pre-rules`. Omit the flag to include both phases. Apply runs `pre-rules` first, then `post-rules`, and commits each phase separately. To run rule deployment between them, plan and apply each phase separately around your rule deployment step. Pass the same phase to `apply` that you used for `plan`.

### Review and apply

Review which files were selected, the per-table counts, and any planned deletes.

```sh
sqlglot migrate plan --dsn connection://warehouse-target --csv-root ./data \
  --layer 'country=none,ZA' --layer 'env=!dev' --csv-phase post-rules --out ./csv-review.json
sqlglot migrate review ./csv-review.json
```

Apply with the same CSV root, filters, and phase. Apply revalidates the sources and target data. For each table with deletes, acknowledge `csv-delete:<schema.table>:<count>:<deleteHash>` using the sum of `explicitDeletes` and `snapshotDeletes` and the table's `deleteHash`.

For the example above, after confirming the review shows zero deletes, run the following command.

```sh
sqlglot migrate apply --dsn connection://warehouse-target --csv-root ./data \
  --layer 'country=none,ZA' --layer 'env=!dev' --csv-phase post-rules ./csv-review.json
```
