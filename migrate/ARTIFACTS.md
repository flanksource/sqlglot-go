# Reviewed migration artifacts

Every dialect plans through `migrate/engine` and writes the same reviewed artifact, version 2. `engine.PlanLive(ctx, db, engine.PlanLiveRequest{Dialect, Scope, Desired, Selection, Atomic})` reads the database's capabilities, the current schema and the live dependents of the columns it changes, then plans. `engine.Plan` does the same offline from two schemas and the dependents the caller supplies. The artifact binds the ordered, grouped SQL to the schemas it was planned from with a SHA-256 hash, and owns its snapshot data. Dialects register themselves by name (`database/migrate/dialect/sqlserver`, `postgres` and `sqlite` each register one when imported), so no caller switches on the dialect.

```go
artifact, err := engine.PlanLive(ctx, db, engine.PlanLiveRequest{
    Dialect: "sqlite",
    Scope:   engine.Scope{Mode: engine.ScopeComplete, Schemas: []string{"main"}},
    Desired: desired,
})
```

## The artifact

| Field | Meaning |
| --- | --- |
| `version`, `dialect` | The format (2) and the registered dialect that planned it. |
| `capabilities` | A fingerprint of what the database could do when planned (transactional DDL, `TRY_CONVERT`, compression and so on). Apply refuses a database whose fingerprint changed. An empty fingerprint marks an offline plan, which carries the dialect's static capabilities (`Dialect.Capabilities` with no database): transactional DDL as the dialect defines it, and only the features every server of the dialect has, so an offline SQL Server artifact keeps each unit in one transaction and plans the compatibility-level-100-safe forms. |
| `scope` | `declared` or `complete`, the schemas, and in declared mode the managed objects. |
| `baseline`, `desired` | The schemas planned from: what the database held, and what it should hold. In declared scope they hold the managed objects and the dependents the plan touches, not the whole schema. |
| `context` | Objects read only because a dependent sits on them. They are never planned or preserved. |
| `selection` | The change IDs the plan was limited to; empty plans every change. |
| `changes` | Every planned change: `id`, `action`, `kind`, `object`, `member`, `destructive`, `reason`, `requires` and, for a forced rebuild, `cause`. |
| `preserved` | The drops left unplanned because the table is declared partial (`partial = true`) or, for a whole object, because the scope is declared. Each carries the rule that applied in its `reason`. |
| `selectNone` | The plan was limited to no change at all: it records what it left out and runs nothing. |
| `dependents` | The definitions of the dependents the plan rebuilds, which each unit re-creates and then verifies. |
| `units` | The transactions of the plan, each with its ops, statements and preconditions. |
| `refusals`, `warnings` | The objects inspection refused that no change touches, and the definitions compared as written because their bodies are not understood. |
| `atomic` | The plan was forced into one unit. |
| `hash` | SHA-256 of every other field. |

A change ID is the JSON array `[action, kind, schema, name, member]`. A forced rebuild uses the action `rebuild`, so the ID of an index rebuild is `["rebuild","index","dbo","accounts","IX_accounts_ref"]`.

## Scope

`complete` manages every object of the named schemas: whatever the desired schema omits is dropped, and each drop is destructive and must be acknowledged. `declared` manages only the named objects: an undeclared table or other object is never dropped, and its drop is listed under `preserved` and never run. What a table declares is a separate rule, the same in both scopes. A table declaration is complete unless it says `partial = true` inside the `table` block (`migrate.Table.Partial`): the column, index, key, foreign key, check or default of a complete table that the declaration omits is dropped, as a destructive change that must be acknowledged, while every such drop of a partial table is preserved. A partial table the target does not have fails the plan ("table X is declared partial and does not exist: a partial declaration cannot create a table"). A dependent that cannot be re-created over a dropped column, and an inbound foreign key from another table on it, refuse the plan by name unless the same plan drops that key or declares it away. A partial table is still rebuilt where a changed column forces it, and the undeclared members it holds are re-created. One rule replaces the earlier per-dialect "preserve omitted" options.

The workflow maps its sources as follows. A pasted or `--desired` HCL file and an inspected snapshot plan in complete scope, because they describe the whole schema. A bundle plans in declared scope, and in complete scope with `--allow-drops`. A database comparison (`database/compare`) plans in complete scope toward its reference, and its selection limits what is carried into the patch.

## Changes, selection and acknowledgements

`Selection` limits the plan to a subset of change IDs, and `changes` then lists only the selected ones. Plan without a selection to see every candidate with the `requires` IDs that must be selected with it: a new foreign key needs its new referenced table, a new index its new column, a dropped column the drops of what references it, and on SQLite a column drop carries every other change of the table it rebuilds. An unknown ID, a change on a preserved drop, or a missing requirement fails before anything is planned. `SelectNone` plans no change at all and cannot be combined with a selection.

`engine.Acknowledgements(artifact)` lists the destructive changes of the plan, sorted. Apply requires each exactly once and refuses anything else. A forced rebuild is never acknowledged on its own: its cause is. The IDs are the changes' own, so they are stable between plans of the same difference; they are no longer a hash of a statement's position and text.

## Forced rebuilds

A change to a column can force the objects that depend on it to be dropped and created again: on SQL Server indexes, keys, foreign keys in both directions, checks, defaults and statistics; on PostgreSQL dependent views; on SQLite the table itself. The engine asks the dialect which changes force a rebuild, reads the live dependents of their columns, and adds a `rebuild` change with a drop op before and a create op after the change, built from the live definition so fill factor, compression, filegroup and constraint trust survive. A rebuild whose dependent cannot be reproduced faithfully, or an inbound foreign key whose child column is not retyped in the same plan, fails planning and names the object.

## Preconditions

A precondition is a query that must return no row. The engine runs them inside the unit's transaction, before its first statement, and a row fails the unit with the precondition's message and rolls it back. A dialect generates them from the change: text to `uniqueidentifier` (a canonical-GUID pattern, or `TRY_CONVERT` when the capability exists), `NULL` to `NOT NULL`, a narrower length, a new unique index or key over existing rows. Each is a table scan, so planning never evaluates them.

## Units

A unit is a connected set of ops: a change, its rebuilds, and the changes that share its tables or requirements. Apply runs the units in order, each on its own connection under the migration lock, in one transaction where the database has transactional DDL. A unit with a non-transactional statement runs without one and says so. The first failure stops the run: earlier units stay committed and are reported, and applying the same artifact again skips them. `engine.Check(ctx, db, artifact)` reports each unit `applied` or `pending`, and fails a unit that matches neither its baseline nor its desired state with `DriftError`, or one whose changes landed but whose rebuilt dependents are missing with `PartialError`.

## Validation and the re-plan rule

`engine.Validate` checks the version and the hash, then plans again from the artifact's own baseline, desired schema, selection and dependents and requires the same hash, so edited SQL, units or changes are refused even when somebody recomputes the hash. A planner upgrade that changes emitted SQL requires a new plan. The hash provides integrity and review identity; authorization and trusted storage remain the caller's responsibility.

Artifacts of the earlier formats are never converted. `workflow.Review`, `Validate` and `Apply` refuse a SQL Server artifact of version 1 (`request` and `plan`), a PostgreSQL or SQLite artifact with HCL snapshots (`baseline.hcl` and `steps`), and the previews that wrapped them (`sqlServerArtifact`, `sqlServerPlan`) with an error that wraps `workflow.ErrOlderVersion` and says to plan again.

## Post-deployment scripts

`sqlserver.RenderScript(artifact, sqlserver.ScriptOptions{Acknowledgements: ids})` renders a SQL Server artifact as one T-SQL script for tools that run scripts instead of calling the executor, such as a `PostDeployment_*` script. It runs `engine.Validate` and requires each destructive change acknowledged exactly once, so the dialect must be registered by importing `database/migrate/dialect/sqlserver`. The script is one batch with one section per unit, in the artifact's order:

1. A header with the artifact hash, scope, capabilities fingerprint, changes (a rebuild with the change that forced it) and the acknowledged destructive IDs. Nothing checks the hash when the script runs: a hand-edited script is no longer the reviewed artifact, so re-render it.
2. `DECLARE @options int = @@OPTIONS`, then `SessionOptions` (`SET XACT_ABORT ON` and the options modules and filtered indexes need).
3. For each unit: `BEGIN TRANSACTION`, then `sp_getapplock` on `LockResource` (exclusive, owned by the transaction, 30 s), which throws on a negative status. A unit with a non-transactional statement has no transaction and a session-owned lock that the script releases at the end of the unit.
4. The state guard of the unit's own changes (below).
5. Each of the unit's preconditions as `IF @uN = 1 AND EXISTS (<query>) THROW 50000, N'<message>', 1`.
6. Each statement as `IF @uN = 1 EXEC sys.sp_executesql N'<statement>'`, so module DDL starts its own batch, then `COMMIT TRANSACTION`.
7. After the last unit, the runtime session options restored from the saved `@@OPTIONS` bits. `QUOTED_IDENTIFIER` is the exception: SQL Server applies it while parsing the batch, so it stays `ON`.

The script has no `GO` line (a statement that would introduce one is a render error), so an error ends the whole run and the units before it stay committed, as `engine.Apply` does. It uses only `THROW`, `sp_getapplock` and `sp_executesql`, which exist at database compatibility level 100, and emits no `DROP … IF EXISTS` or `CREATE OR ALTER` of its own: the statements are the artifact's.

**State guard.** For each unit the script tests the unit's own changes (not its rebuilds, which sit inside the same transaction) against the catalog. If every change still matches its baseline, the unit runs. If every change already matches its desired state, the unit is skipped, which makes running the script again a no-op. Otherwise the script throws that the unit matches neither its baseline nor its desired state, and its transaction rolls back. A unit whose changes have no state to test, such as `CREATE OR ALTER` module changes, always runs and is refused unless its statements are re-runnable. The guard does not re-verify the rebuilt dependents the way `engine.Check` does; they are dropped and created in the unit's transaction, under the names recorded when the artifact was planned, so a database that names one differently fails the unit and rolls it back.

| Change | Runs when | Skipped when |
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
| add / modify view, procedure, function, trigger; modify sequence | always, and each statement must be `CREATE OR ALTER`, `ENABLE`/`DISABLE TRIGGER` or `ALTER SEQUENCE` | never |
| modify column, index, primary key, foreign key, check | the member matches its baseline state | the member matches its desired state |

The member states compare:

- **Column:** canonical type (`FormatType` spelling), nullability, ROWGUIDCOL, identity, collation (the database default when empty) and default definition. The default constraint's name is never compared.
- **Index, unique constraint, primary key:** name, primary/unique-constraint/unique flags, clustering, fill factor, ordered key columns with `DESC`, sorted included columns and filter.
- **Foreign key:** name, referenced table, column pairs and referential actions.
- **Check:** name and definition.

Default, filter and check expressions compare as exact text against `OBJECT_DEFINITION`, `sys.indexes.filter_definition` and `sys.check_constraints.definition`, so the baseline and the desired state must both be inspected schemas. A desired schema written by hand, or taken from a server version that rewrites an expression differently, never matches after the change is applied, and the next run throws drift instead of skipping the unit. Atlas HCL is reconciled against the live schema before planning, which brings its spelling in line.

**Default constraints.** A column drop or a type, default or collation change drops the column's default first. The script drops whatever default the column has when it runs: it reads `sys.default_constraints` for the column into `@df` and executes `ALTER TABLE … DROP CONSTRAINT ' + QUOTENAME(@df)` only when one exists. Any other constraint drop in a column change is a render error.

**System-named constraints.** SQL Server names an unnamed primary key, unique, foreign key, check or default constraint itself (`PK__orders__3213E83F…`), so the name differs in every database. A change of such an index, key or check as one of the unit's own changes is refused at render time with the change ID: give the constraint an explicit name on both sides and plan again. A system-named dependent that a unit rebuilds is dropped and created under the name read when planning, so the script suits the database it was planned against. A new table's system-named members are matched by shape, without their names.

Any other action or kind, such as attributes, schema changes or a modified table type, has no derivable guard and fails rendering with the change ID. No unguarded statement is ever emitted.

Run `go test ./migrate/... -count=1`. `go run ./examples/sqlserver-migration` plans a table offline with the SQL Server dialect and prints its statements.
