Inspect a target and write a plan file without changing the database. Choose source paths, `--desired FILE`, `--bundle DIR`, or `--csv-root DIR`. The plan file goes to `--out FILE`, `.sqlglot-go/plan.json` by default, a summary of what will change goes to stderr, and the structured plan to stdout.

Source paths are files and directories whose content decides how they are read: a file that parses as an HCL body is schema, a file whose first statement past comments is SQL is a script, and `.sql` / `.hcl` names are taken at their word. A directory contributes every such file under it (hidden entries skipped), named relative to it, and lists on stderr the files it skips because they hold neither. A file argument that holds neither is an error. A script found under another extension is named by its kind, so `PostDeployment_Seed/SQLData.xml` plans as the script `PostDeployment_Seed/SQLData.sql`.

```sh
sqlglot migrate plan --dsn connection://inventory --bundle ./db
sqlglot migrate plan --dsn connection://inventory ./scripts ./schema.hcl
```

See `sqlglot migrate --help` for source formats, HCL and SQL examples, and script invalidation.
