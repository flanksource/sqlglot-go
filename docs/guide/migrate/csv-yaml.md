---
title: CSV mapping YAML reference
description: Reference for AS.csv.yaml, per-file sidecars, row keys, value decoding, layers, and delete modes.
---

# CSV mapping YAML reference

CSV migrations target existing SQL Server tables. Pass a directory to `--csv-root`. Put the optional `AS.csv.yaml` at its root and file-specific settings in `NAME.csv.yaml` beside `NAME.csv`. Each YAML file uses a single document with `version: 1`. Unknown keys, nested root configs, symbolic links, and sidecars without a corresponding CSV fail validation. Paths below are relative to the CSV root and use `/`.

## Root config and routing

```yaml
version: 1
defaults:
  schema: dbo
  tablePattern: '^(?:.*/)?(?P<table>ItemLabel)(_[A-Z]{2})?\.csv$'
  delimiter: '~'
  values:
    blank: empty-string
    nullTokens: ['\N']
    nullTokenCase: sensitive
tables:
  dbo.ItemLabel:
    key: [ID]
    phase: post-rules
  dbo.ItemStatus:
    sources: ['status.csv']
    delimiter: ','
    ignoreColumns: [SourceNote]
    delete: disabled
    values:
      columns:
        Comment:
          blank: sql-null
layers:
  country:
    values: [ZA, KE]
    match: ['**/*_${value}.csv']
    valuePattern: '[A-Z]{2}'
    defaultFilter: none
  env:
    values: [dev, qa]
    match: ['overrides/${value}/**/*.csv']
    defaultFilter: none
```

In this example `ItemLabel.csv`, `ItemLabel_ZA.csv`, and `overrides/qa/ItemLabel_ZA.csv` all route to `dbo.ItemLabel`. Give those files the same `ID~Label` header. `status.csv` routes to `dbo.ItemStatus` and uses commas.

| Root field | Meaning |
| --- | --- |
| `version` | Required integer `1`. |
| `defaults` | Shared [file settings](#file-settings), plus `schema` and `tablePattern` for inferred routing. |
| `defaults.schema` | Schema for inferred tables. Defaults to the selected target schema, normally `dbo`. |
| `defaults.tablePattern` | Optional Go regular expression matching the entire root-relative CSV path. It must contain exactly one named capture, `(?P<table>...)`. The captured identifier becomes the table name. |
| `tables` | Map keyed by qualified table name, such as `dbo.ItemLabel`. Each entry can set file options, `sources`, and `snapshotFiles`. |
| `tables.<schema.table>.sources` | List of explicit source paths or globs. `**` can match nested directories. A match in more than one table is an error. |
| `tables.<schema.table>.snapshotFiles` | Exact untagged base CSV paths for `delete: snapshot`. Required for that mode and valid only with it. Globs are rejected. |
| `layers` | Map of named [file selection layers](#layers-and-filters). |

Explicit `sources` and a sidecar `table` can route the same file when they agree. The regex is used when neither gives a target. Unmapped CSV paths appear in the review's `unmatched` list. A sidecar can omit `table` if the root config already routes its CSV to a table.

## File settings

Defaults apply first, table settings next, then sidecar settings. The root config is optional when each file has its own sidecar. A sidecar accepts `version`, `table`, and every field in the table below. For example, `labels.csv.yaml` could contain:

```yaml
version: 1
table: dbo.ItemLabel
key: [ID]
delimiter: ','
```

| Setting | Default | Accepted values and behavior |
| --- | --- | --- |
| `key` | Table primary key | An ordered list of existing key columns. Each file header must include them. Key cells must be nonempty and non-NULL. A table without a primary key requires an explicit key. |
| `delimiter` | `,` | A single CSV separator character, for example `~`, `;`, or `"\t"`. |
| `ignoreColumns` | Empty list | Header names to ignore, matched case-insensitively. Key columns and `$delete` must remain usable. |
| `phase` | `pre-rules` | `pre-rules` or `post-rules`. All selected files for one table must agree. |
| `delete` | `explicit` | `disabled`, `explicit`, or `snapshot`. Set `snapshot` only on a table. |
| `values` | See below | Rules for unquoted blank and NULL values, with optional column overrides. |

The CSV must be UTF-8. A UTF-8 byte-order mark is accepted. The first record is a header. Headers are trimmed and matched without case. Repeated, empty, unknown, or unwritable column names fail validation unless the column is explicitly ignored where allowed. Files for a table must share its ordered key, phase, and snapshot mode. A data row may omit a field by omitting that column from the file's header. The SQL Server target handles conversion to the destination types. Use `0x`-prefixed hex for binary values, a 36-character UUID for `uniqueidentifier`, and `0`, `1`, `true`, or `false` for `bit`.

### Value decoding

| Field | Default | Meaning |
| --- | --- | --- |
| `values.blank` | `empty-string` | Decode an unquoted empty field as `empty-string` or `sql-null`, or reject it with `error`. |
| `values.nullTokens` | `['\N']` | Exact tokens representing SQL `NULL` in unquoted fields. Use `[]` to clear inherited tokens. Empty strings are governed by `blank`. |
| `values.nullTokenCase` | `sensitive` | `sensitive` or `insensitive`. |
| `values.columns.<column>` | Inherited | Overrides `blank`, `nullTokens`, or `nullTokenCase` for one target column. Column settings cannot nest. |

Quoted cells keep their text. For example, unquoted `\N` is SQL `NULL` by default, while `"\N"` is the literal text. `""` is an empty string. Only character columns accept empty strings as values.

## Layers and filters

| Layer field | Meaning |
| --- | --- |
| `values` | Required nonempty list of allowed tags. `none` is reserved for paths without a match. |
| `match` | Required nonempty list of root-relative patterns. Each has one `${value}` capture. `*` and `?` match within one path segment, and `**/` matches zero or more directory levels. |
| `valuePattern` | Optional full-match Go regular expression for tag spelling. Default `[A-Za-z0-9][A-Za-z0-9_-]*`. |
| `defaultFilter` | Optional selection expression for this layer. Defaults to `none`. |

Override a filter with repeatable `--layer NAME=FILTER`. A file must pass every layer. A filter can include `none`, specific tags, or `*` for all tags and untagged files. Commas combine terms. `!TAG` excludes a tag. With only exclusions, selection starts with all values. Unknown values and conflicting tags fail planning.

For the example above, `--layer 'country=none,ZA' --layer 'env=none,qa'` includes the base files, the ZA files, and the QA overlays. For a shared key, the most specific row wins when its tags include all the other row's non-base tags and at least one additional tag. Identical tag sets and competing overlays without one most specific row are errors.

## Delete policies and phase execution

| Policy | Behavior |
| --- | --- |
| `disabled` | A `$delete` value of `true` is an error. |
| `explicit` | `$delete=true` deletes the exact key. Blank or `false` means apply the row normally. |
| `snapshot` | Deletes existing keys absent from the effective selected data. Set at table scope, declare the exact untagged `snapshotFiles`, and include at least one effective insert or update row. |

Plan against the intended target, then review selected files, unmatched paths, row counts, and any `explicitDeletes` or `snapshotDeletes`. For each table with planned deletes, pass one `--ack 'csv-delete:<schema.table>:<count>:<deleteHash>'`, using the sum of the two reviewed delete counts and the table's `deleteHash`.

`--csv-phase` selects one phase on both plan and apply. Without the flag, apply commits `pre-rules` first and `post-rules` second. Plan and apply each phase separately to deploy rules between them. Reuse `--csv-root`, all `--layer` filters, and `--csv-phase` at apply time. Changed inputs or target rows require a new plan.

For the complete plan–review–apply example, see [CSV data migrations](./csv).
