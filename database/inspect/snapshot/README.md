# Complete migration snapshots

`snapshot.Inspect(ctx, db, snapshot.Options{Schemas: []string{"dbo"}})` reads full SQL Server migration definitions without the row or definition limits used by the interactive explorer. Schemas are explicit. The connection must have database `VIEW DEFINITION` permission so hidden metadata cannot look like absent objects.

Inspection reuses the shared SQL Server catalogue reader for tables, columns, indexes, keys, checks and routines. It also reads column collations, view and trigger definitions, trigger state and module dependencies. The result uses sqlglot's native migration model; this package does not generate or execute migrations.

The current contract targets SQL Server 2016 or newer, including databases at compatibility level 100. Unsupported computed or user-defined columns, storage settings, indexed views, disabled or untrusted constraints, unsupported module session settings, and external or unresolved dependencies cause explicit errors. This prevents a partial catalogue from becoming a destructive desired state.

Default, check or column expressions that depend on other database objects are also blocked: the native table model does not yet carry those dependencies for ordering and selection validation.

`Options.Objects` narrows an inspection to named tables, views, routines, triggers, sequences and table types, with the name filter pushed into each catalogue query. `Options.CollectRefusals` turns what the model cannot hold into `Result.Refusals` instead of an error: a whole-object refusal (an unsupported table, module or object type) leaves the object out of the schema, and a member refusal (a disabled index, an untrusted key or check) leaves it in and names the member. In that mode the result is checked by diffing the schema against itself, so a foreign key may reference a table that was not read.

Trigger schemas come from [sys.objects](https://learn.microsoft.com/en-us/sql/relational-databases/system-catalog-views/sys-objects-transact-sql), while parent and disabled state come from [sys.triggers](https://learn.microsoft.com/en-us/sql/relational-databases/system-catalog-views/sys-triggers-transact-sql).

```sh
go test ./inspect/sql/snapshot -count=1
```

The tests separate pure catalogue projection from database-driver boundary checks. Live SQL Server execution remains a separate integration gate.
