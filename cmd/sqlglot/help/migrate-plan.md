Inspect a target and write a plan file without changing the database. Choose source paths, `--desired FILE`, `--bundle DIR`, or `--csv-root DIR`. The plan file goes to `--out FILE`, `.sqlglot-go/plan.json` by default, a summary of what will change goes to stderr, and the structured plan to stdout.

A single directory source or `--bundle DIR` reads `.sqlglot-go.yaml` at its root when present. With no explicit source, plan uses the configuration in the current directory or at the Git root, in that order. Use `--target dev` to resolve an alias from `connections` in the same configuration. See `sqlglot migrate --help` for examples.

Source paths are files and directories whose content decides how they are read: a file that parses as an HCL body is schema, a file whose first statement past comments is SQL is a script, and `.sql` / `.hcl` names are taken at their word. A directory contributes every such file under it (hidden entries skipped), named relative to it, and lists on stderr the files it skips because they hold neither. A file argument that holds neither is an error. A script found under another extension is named by its kind, so `PostDeployment_Seed/SQLData.xml` plans as the script `PostDeployment_Seed/SQLData.sql`.

`--label SELECTOR` (repeatable) plans only the project sources whose labels match, and replaces the `select` list of `.sqlglot-go.yaml` entirely. `key=pattern` keeps sources with a matching value, `!key=pattern` drops sources with one, and a pattern may start or end with `*`. Entries for one key are alternatives for includes, and every exclude applies. A source with no value for a key passes excludes and fails includes. It fails when nothing is selected, or when there is no project configuration.

```sh
sqlglot migrate plan --dsn connection://inventory --bundle ./db
sqlglot migrate plan --dsn connection://inventory ./scripts ./schema.hcl
sqlglot migrate plan --dsn connection://inventory --bundle ./db --label '!scope=vendor' --label 'country=ZW'
```

See `sqlglot migrate --help` for source formats, HCL and SQL examples, and script invalidation.
