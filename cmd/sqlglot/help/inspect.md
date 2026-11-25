Read a database without changing it. With no output flag the result is a catalog listing. `--snapshot` returns the complete migration snapshot as JSON, which `sqlglot migrate plan --desired` reads back.

`--hcl` writes SQL Server tables as the HCL a bundle declares, so a team can adopt tables it already has. The HCL evaluates back to the same tables: column type, nullability, default (with its constraint name unless SQL Server generated it), identity, collation when it differs from the database default, the primary key with its name and clustered flag, unique constraints, indexes with key direction, included columns, filter and clustered flag, foreign keys with their actions, and checks. Planning the export against the database it came from in declared scope has no changes.

```sh
sqlglot schema inspect --dsn connection://erp --hcl --table AsPolicy --table AsClient > tables.hcl
sqlglot schema inspect --dsn connection://erp --hcl --out db/tables
sqlglot schema inspect --dsn connection://erp --hcl --table AsPolicy --columns AsPolicy=PolicyGUID,Status --out db/partial
```

Without `--out` one document goes to stdout. With `--out DIR` there is one file per table, each starting with its own `schema "dbo" {}` block, which a bundle reads as one schema; add `--document` for a single `schema.hcl` in the directory. A foreign key can only be written when the table it references is in the same export, so select related tables together.

`--columns TABLE=COLUMN,COLUMN` (repeatable) declares only the named columns of a table, with no keys, indexes, foreign keys or checks, for a table you do not own.

The HCL cannot declare ROWGUIDCOL, index options (fill factor, PAD_INDEX, IGNORE_DUP_KEY, row and page locks, STATISTICS_NORECOMPUTE, data compression, filegroup), NOT FOR REPLICATION, or a foreign key to a table outside the export, and the inspector refuses what it cannot read (partitioned tables, computed columns and the like). Each of these fails the export, naming the table, the member and the attribute. `--omit-unsupported` leaves them out instead and writes one comment line per omission, starting `# omitted:`.

Only tables are exported: views, routines and the other objects of the schema are not part of the table HCL.
