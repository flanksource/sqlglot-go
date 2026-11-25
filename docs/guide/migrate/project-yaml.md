---
title: The .sqlglot-go.yaml project file
description: Map project files to migration sources, label them, and select which labelled sources a plan includes.
---

# The `.sqlglot-go.yaml` project file

`.sqlglot-go.yaml` maps the files of a project to migration sources. `sqlglot migrate plan` reads it from the project directory, or from the Git root when the current directory has none. Other Go modules load it with `workflow.LoadProject` and read each source's ID, name, phase, labels, path and content through `Project.Files`.

```yaml
version: 1
connections:
  dev: 'connection://inventory-dev'
mappings:
  - match: 'schema/tenant/**/*.hcl'
    labels: {scope: [tenant]}
  - match: 'schema/vendor/**/*.hcl'
    labels: {scope: [vendor]}
  - match: 'migrations/**/*.sql'
    transform: '{"sql": file.content, "labels": {"country": file.path.startsWith("migrations/zw_") ? ["ZW"] : []}}'
select: ['!scope=vendor']
ignore:
  - 'migrations/archive/**'
```

## Fields

| Field | Meaning |
| --- | --- |
| `version` | Must be `1`. |
| `connections` | Target aliases for `--target`, as `connection://` references or database DSNs. |
| `mappings` | Rules that turn matching files into sources. A file may match only one rule. |
| `select` | The default label selectors. See [Selecting sources by label](#selecting-sources-by-label). |
| `ignore` | Globs of files and directories that never become sources. |

Each mapping takes these fields.

| Field | Meaning |
| --- | --- |
| `match` | A case-sensitive, project-relative glob. |
| `kind` | `sql` or `hcl`. Without it the file extension decides, or the transform's result key. |
| `labels` | Named lists of strings attached to every source of the rule. Labels appear in the plan's source list and in each planned script, and `select` and `--label` can select on them. |
| `transform` | A gomplate CEL expression over `file.path` and `file.content` that returns `sql` or `hcl` and can set `id`, `labels`, and the SQL `name` and `phase`. A transform replaces a rule's label with its own list, and an empty list removes it. |

## Selecting sources by label

A plan includes every mapped source unless selectors narrow it. `select` in the file is the default, and each `--label` on `sqlglot migrate plan` replaces the whole default.

| Selector | Keeps |
| --- | --- |
| `key=pattern` | Sources with a value for `key` that matches `pattern`. |
| `!key=pattern` | Sources with no value for `key` that matches `pattern`. |

A pattern is one value, optionally starting or ending with `*`, and matches case-insensitively. A list in one entry, such as `country=BW,ZW`, is an error: write one entry per value. An entry without `=`, or with an empty key or pattern, is an error that names the entry.

A source is kept when, for every key that has selectors, none of its values for that key matches an exclude, and, if the key has any include, at least one of its values matches an include. Several includes for one key are alternatives. A source with no value for a key passes the exclude-only selectors and fails the includes.

With the example above, `select: ['!scope=vendor']` plans the tenant tables and every script, because unlabelled scripts have no `scope` value. Planning only the tenant tables takes `--label scope=tenant`. Planning the vendor tables as a deliberate step takes `--label scope=vendor`, which ignores the default exclude.

```sh
sqlglot migrate plan --target dev
sqlglot migrate plan --target dev --label 'scope=tenant'
sqlglot migrate plan --target dev --label '!scope=vendor' --label 'country=ZW'
```

The plan fails when the selection leaves no source, naming the selectors. The project preview reports how many mapped sources the selection left out beside the ignored count.

## Using a project from Go

```go
project, err := workflow.LoadProject(fsys)
tenant, err := project.Select([]string{"scope=tenant"})
for _, file := range tenant.Files() {
    fmt.Println(file.ID, file.Name, file.Phase, file.Labels, file.Path, file.BundlePath, len(file.Content))
}
```

`ProjectFile.Inputs` lists every project-relative path whose bytes went into the source's content, the mapped file first and then each companion its transform read with `read()`, sorted and listed once, so a consumer can map an edit to a companion file back to its source.

`workflow.ParseProjectConfig` reads a config from bytes, so a caller can supply a default when the filesystem has none. `ProjectConfig.Load` maps a filesystem with a config built in code. `Project.Bundle` returns the bundle filesystem, the script metadata and the source inventory that `workflow.Plan` uses.
