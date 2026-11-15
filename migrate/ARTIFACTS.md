# Reviewed migration artifacts

`sqlserver.BuildArtifact(migrate.ArtifactRequest{Schemas, Request})` binds complete current and desired snapshots, their explicit schema scope, and the exact generated plan to a SHA-256 hash. The artifact owns its snapshot data; later edits to the input cannot alter it. Named collections are sorted for deterministic output while column and key order is retained.

```go
artifact, err := sqlserver.BuildArtifact(migrate.ArtifactRequest{
    Schemas: []string{"dbo"},
    Request: request,
})
```

`ValidateArtifact` checks the version, content hash and consistency with the planner. It rejects edited SQL even when somebody recomputes the content hash. A planner upgrade that changes emitted SQL requires a new preview. The hash provides integrity and review identity; authorization and trusted storage remain the caller's responsibility.

`Request.Selection` limits the plan to a subset of change IDs (each `migrate.Change.ID` is the JSON array `[action, kind, schema, name, member]`). The planner still lists every candidate in `Plan.Changes`, each with the `Requires` IDs that must be selected with it: a new foreign key needs its new referenced table, a new index its new column, and a dropped column or table the drops of the keys, indexes and tables that still reference it. An unknown ID or a missing requirement fails. The selection is sorted and hashed, so `ValidateArtifact` reproduces the same partial plan, and `Acknowledgements` lists only the selected destructive changes.

`CheckArtifact(artifact, actual)` uses native SQL semantics to compare a complete live snapshot. It returns `ArtifactApplied` when the desired state, minus any unselected changes, already exists, or `ArtifactPending` when the baseline still matches. Any other state fails with drift details. This includes changes to unselected target members captured in the baseline. Catalog expression formatting is compared through sqlglot rather than raw text hashes.

These guards run in Go and do not open a database. They do not make the plan's individual SQL strings a self-guarding deployment script. A database executor must inspect under the same transaction and application lock used to execute the exact reviewed steps, check the complete postcondition, and commit only after it matches. Commons-db's `migrate/sqlserver` supplies this execution path.

## Post-deployment scripts

`sqlserver.RenderScript(artifact, sqlserver.ScriptOptions{Acknowledgements: ids})` renders an artifact as one self-contained T-SQL batch for tools that run scripts instead of calling the executor, such as a `PostDeployment_*` script. It first runs `ValidateArtifact` and `ValidateAcknowledgements`, the same check the executor uses: each destructive change ID exactly once, with unknown or repeated IDs refused. The output is deterministic and has these parts:

1. A header comment with the artifact hash, schemas, change IDs and acknowledged destructive IDs. Nothing checks the hash when the script runs. A hand-edited script is no longer the reviewed artifact, so re-render it instead of editing it.
2. `DECLARE @options int = @@OPTIONS`, then `SessionOptions` (the `SET XACT_ABORT ON` and module/filtered-index options the executor also uses), then `BEGIN TRANSACTION`.
3. `sp_getapplock` on `LockResource` (`commons-db/migrate/schema`, exclusive, transaction-owned, 30 s), which throws when it returns a negative status. Direct and script runs serialise on it.
4. One `@cN` flag per change. Every change's state guard is evaluated before any step runs, because one change can emit several steps (a column type change drops, alters and re-adds its default) and the first of them would change what a later guard sees.
5. A consistency check for each group of interdependent changes (see below).
6. Each step as `IF @cN = 1 EXEC sys.sp_executesql N'<step>'`, then `COMMIT TRANSACTION`.
7. The runtime session options restored from the saved `@@OPTIONS` bits, so later scripts in the same session see their own settings again. `QUOTED_IDENTIFIER` is the exception: SQL Server applies it while parsing the batch, so a conditional `SET` cannot restore it, and it stays `ON`. A failed run aborts the batch before this part, leaving the script's options in place.

The script contains no `GO` line, and a step that would introduce one is a render error. `oipa-deploy.jar`-style runners split on `^\s*GO\s*$`, and a transaction must not span batches. Module DDL is nested in `sp_executesql`, so one batch is enough.

Each change ends in one of three outcomes. It applies when the database is at its baseline, or when an added object is absent. It is skipped when the database already holds exactly its desired state. Otherwise the script `THROW`s a drift error naming the change ID and the transaction rolls back.

| Change | Applies when | Skipped when |
| --- | --- | --- |
| add table | `OBJECT_ID(N'[s].[t]', N'U') IS NULL` | each column, index, foreign key and check matches its desired state, and the four member counts match |
| add column | `COL_LENGTH(N'[s].[t]', N'c') IS NULL` | the column matches its desired state |
| add index, unique constraint | no `sys.indexes` row has its name | the index matches its desired state |
| add primary key | the table has no primary key | the key matches its desired state; a key under another name is drift |
| add foreign key, check | no `sys.foreign_keys` / `sys.check_constraints` row on the parent has its name | the constraint matches its desired state |
| add table type | no `sys.table_types` row has its schema and name | its columns, keys and checks match by shape, with matching counts |
| add sequence | `OBJECT_ID(…, N'SO') IS NULL` | type, start, increment, bounds, cycling and cache match as the inspector reads them |
| drop table, column, index, unique constraint, foreign key, check, table type, sequence | the object or member exists by name | it is absent |
| drop primary key | the named key exists | the table has no primary key; a key under another name is drift |
| drop view, procedure, trigger, function | `OBJECT_ID(…, N'V' / N'P' / N'TR')`, untyped for functions, is not null | it is absent |
| add / modify view, procedure, function, trigger; modify sequence | always: each step must be `CREATE OR ALTER`, `ENABLE`/`DISABLE TRIGGER` or `ALTER SEQUENCE` | never |
| modify column, index, primary key, foreign key, check | the member matches its baseline state | the member matches its desired state |

The member states compare:

- **Column:** canonical type (`FormatType` spelling), nullability, ROWGUIDCOL, identity, collation (the database default when empty) and default definition. The default constraint's name is never compared.
- **Index, unique constraint, primary key:** name, primary/unique-constraint/unique flags, clustering, fill factor, ordered key columns with `DESC`, sorted included columns and filter.
- **Foreign key:** name, referenced table, column pairs and referential actions.
- **Check:** name and definition.

Default, filter and check expressions compare as exact text against `OBJECT_DEFINITION`, `sys.indexes.filter_definition` and `sys.check_constraints.definition`. SQL Server stores its own rewrite of every expression (`([total]>=(0))` for `total >= 0`), and the inspector reads that text verbatim, so the baseline and the desired state must both be inspected snapshots. A desired snapshot written by hand, or taken from a server version that rewrites an expression differently, never matches after the change is applied, and the next run `THROW`s drift instead of skipping it.

**Default constraints.** A column drop or type, default or collation change drops the column's default first. The plan names the staging target's constraint, but the same default has another name in other databases, and always when SQL Server named it. The script therefore drops whatever default the column has when it runs: it reads `sys.default_constraints` for the column into `@df` and executes `ALTER TABLE … DROP CONSTRAINT ' + QUOTENAME(@df)` only when one exists. Any other constraint drop in a column change is a render error.

**System-named constraints.** SQL Server names an unnamed primary key, unique, foreign key, check or default constraint itself (`PK__orders__3213E83F…`, `FK__items__id__5EBF139D`), so the name differs in every database. An add, drop or modify of such an index, key or check constraint is refused at render time with the change ID. Give the constraint an explicit name with `sp_rename` on both sides and compare again. Resolving such a constraint by its shape at run time would need a structural lookup and dynamic DDL for every constraint kind, which is why rendering refuses it instead. A new table's system-named members are matched by shape, without their names. Defaults are never matched by name, so a system-named default needs no rename.

**Interdependent changes.** The planner splits an index, key or check rebuild around an `ALTER COLUMN` on the same table and keeps the rebuild's change ID on both halves. When the rebuilt member references the altered column (a key, included or foreign key column, or a mention in its filter or check expression), the two changes form a group whose flags must agree. If only some of them are already in their desired state, for example the index is already rebuilt but the column is still at its baseline, the script `THROW`s naming every change of the group before any step runs. Without this check, the `ALTER COLUMN` would fail halfway through the transaction with a dependency error.

Any other action or kind, such as attributes, schema changes or a modified table type, has no derivable guard and fails rendering with the change ID. No unguarded statement is ever emitted.

Run `go test ./migrate/... -count=1`. UIR's `go run ./examples/sql-migration` demonstrates compiling a selected member into an artifact without a database.
