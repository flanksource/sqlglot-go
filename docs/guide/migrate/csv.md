---
title: CSV data migrations
description: Map CSV rows to SQL Server tables and review inserts, updates, and deletes before applying.
---

# CSV data migrations

Use CSV migrations to seed or reconcile rows in existing SQL Server tables. The planner reads the CSV files, YAML mappings, and current target rows and reports the changes by table.

## Map a file to a table

Use a disposable SQL Server database with a [saved connection](../connections) named `inventory-sqlserver`. Set up a target table and row for the example.

```sql
CREATE TABLE dbo.ItemLabel (
    ID int NOT NULL PRIMARY KEY,
    Label nvarchar(100) NOT NULL
);
INSERT INTO dbo.ItemLabel (ID, Label) VALUES (1, N'Notebook');
```

Save this UTF-8 file as `data/labels.csv`.

```csv
ID,Label
1,Hardcover notebook
2,Pencil
```

Save this mapping as `data/labels.csv.yaml` beside it.

```yaml
version: 1
table: dbo.ItemLabel
key: [ID]
```

Each CSV header must include its match columns. Omit `key` to use the target table's primary key. The default delimiter is `,`. By default an unquoted `\N` means SQL `NULL`. Quotes preserve literal null tokens and empty strings. Use `values` in YAML to define a different blank or null policy.

## Review and apply

```sh
sqlglot migrate plan --dsn connection://inventory-sqlserver --csv-root ./data --out ./csv-plan.json
sqlglot migrate review ./csv-plan.json
```

Confirm that `labels.csv` routes to `dbo.ItemLabel`, with one insert, one update, and zero deletes. The review reports which files were selected, any `unmatched` paths, and each table's `explicitDeletes` and `snapshotDeletes` counts.

```sh
sqlglot migrate apply --dsn connection://inventory-sqlserver --csv-root ./data ./csv-plan.json
```

Apply with the same source root, layer filters, and phase used to plan. Apply revalidates the source files and relevant target rows. Verify the example with `SELECT ID, Label FROM dbo.ItemLabel ORDER BY ID;`. It returns `1, Hardcover notebook` and `2, Pencil`.

## Review deletes and manage multiple files

- The default `delete: explicit` deletes a row only when `$delete` is `true` for its key. Use `delete: disabled` to reject delete requests.
- Table-level `delete: snapshot` removes target keys absent from the selected dataset. List exactly the untagged base CSVs in `snapshotFiles` and keep at least one effective row to insert or update. Review the resulting delete scope before applying.
- For each table with deletes, pass `--ack 'csv-delete:<schema.table>:<total-delete-count>:<deleteHash>'`, using the sum of `explicitDeletes` and `snapshotDeletes` and the reviewed table's `deleteHash`.
- For shared mappings, place `AS.csv.yaml` at the CSV root. It can declare defaults, per-table `sources` paths or globs, and named layers. Sidecar settings override table settings, which override defaults.
- For declared layers, repeat `--layer NAME=FILTER` to select overlays. `none` includes untagged files, `*` selects all values, and `!value` excludes a value. More specific tagged rows override less specific rows with the same key. Ambiguous overlays fail planning.
- Set table `phase` to `pre-rules` or `post-rules` and pass the matching `--csv-phase` to plan and apply one phase. With both phases selected, apply commits each separately in that order.

The [CSV mapping YAML reference](./csv-yaml) covers every field and a complete `AS.csv.yaml` example with layer patterns and filters.
