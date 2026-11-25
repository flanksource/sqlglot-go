---
title: Project YAML reference
description: Configure migration sources, transforms, labels, and target aliases in .sqlglot-go.yaml.
---

# Project YAML reference

Place `.sqlglot-go.yaml` in a migration project. Use it to select HCL and SQL sources and give your database targets stable names. Use [CSV mapping YAML](./csv-yaml) for CSV imports.

## Complete example

```yaml
version: 1
connections:
  dev: 'connection://inventory/inventory-sqlserver'
  local: './inventory.db'
mappings:
  - match: 'schema/**/*.hcl'
  - match: 'scripts/**/*.sql'
    labels:
      component: [inventory]
  - match: 'descriptors/**/*.xml'
    kind: sql
    transform: >-
      {"sql": read(xmlpath(file.content, "/migration/@script")),
       "id": xmlpath(file.content, "/migration/@id").trim().lower(),
       "phase": "post"}
ignore:
  - 'scripts/archive/**'
```

The descriptor rule reads a file such as `descriptors/labels.xml` containing `<migration id="seed-labels" script="labels.sql"/>`. Keep `labels.sql` beside the XML file and outside the direct `scripts/**/*.sql` match. Its ledger ID is `seed-labels`. `dev` uses a [saved SQL Server connection](../connections); `local` is an example alias for projects with SQLite-compatible sources.

```sh
sqlglot migrate plan --target dev --out ./reviewed.json
sqlglot migrate review ./reviewed.json
sqlglot migrate apply --target dev ./reviewed.json
```

## Root fields

| Field | Type | Rule |
| --- | --- | --- |
| `version` | Integer | Required. Must be `1`. |
| `connections` | Map of alias to string | Each value is a nonempty DSN, SQLite `.db` path, or `connection://` reference. Alias names start with an ASCII letter or digit and may continue with letters, digits, `_`, `.`, or `-`. |
| `mappings` | List of mapping rules | Selects the HCL and SQL sources. With mappings configured, only matched files enter the bundle. |
| `ignore` | List of globs | Excludes matching files and directories, including companion files read by transforms. |

Use one YAML document. Unknown keys fail validation. At least one mapping or connection is required. `--target` is mutually exclusive with `--dsn`. Relative SQLite `.db` paths in an alias are resolved beside this YAML file. `connection://` aliases use the normal saved connection layers.

Without a source argument, `migrate plan` searches the working directory first and the Git root second. `migrate plan DIR` and `migrate plan --bundle DIR` look for the file in `DIR`. An explicit `--desired` or `--csv-root` selects its own source, and can use `--target` for the database.

## Mapping fields

| Field | Type | Rule |
| --- | --- | --- |
| `match` | Glob | Required. Match a case-sensitive, project-relative path using `/`; `**` spans directories. |
| `kind` | `hcl` or `sql` | Optional. Defaults to the `.hcl` or `.sql` extension, or to the transform result for another extension. |
| `labels` | Map of name to string list | Optional metadata shown in the structured plan and script review. Label names and list entries must be nonempty. |
| `transform` | CEL expression | Optional source conversion and metadata projection. It receives `file.path` and `file.content`. |

Hidden files and directories are skipped during discovery. A file can match at most one mapping, and every selected source needs a unique ID and bundle path. By default the ID is the matched path normalized to the source extension. Planning fails if configured mappings select no sources.

## Transform result

Return a map with exactly one content field, `sql` or `hcl`. Its kind must agree with any explicit or inferred kind.

| Key | Type | Rule |
| --- | --- | --- |
| `sql` | String | SQL text to plan. |
| `hcl` | String | HCL declaration to plan. |
| `id` | String | Optional stable source ID. For HCL it must be a project-relative `.hcl` path. For SQL it is the ledger ID and the name other scripts use in `dependsOn`. |
| `name` | String | Optional SQL display name. Defaults to the ID and determines the order of ready scripts within a phase. |
| `phase` | `pre` or `post` | Optional SQL phase. If the SQL also declares `-- phase:`, the two values must agree. |
| `labels` | Map of name to string list | Overrides individual mapping labels. An empty list removes the label. |

`read(path)` reads a regular file relative to the matched source's directory. Use a relative path within the project. If that companion matches an ignore rule, the mapping is skipped. `xmlpath(xml, selector)` returns the text or attribute value from exactly one XPath match. Zero or multiple matches fail the plan.
