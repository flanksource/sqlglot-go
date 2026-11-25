# Schema migrations

`migrate/engine` plans and applies schema migrations for every dialect. A dialect (`database/migrate/dialect/sqlserver`, `postgres` or `sqlite`, registered by importing it) supplies inspection, diffing and lowering; the engine adds the scope policy, forced rebuilds of dependent objects, global ordering, units, preconditions and the reviewed artifact once. `migrate/sqlserver` is the pure SQL Server half: it diffs two schemas and lowers the changes to ordered T-SQL without opening a database, and `migrate/atlas/sqlserver` exposes the same differ and planner to Atlas consumers. Credentials, application locks, transactions, execution and deployment records belong to the dialect's session and the caller.

```go
import (
    _ "github.com/flanksource/sqlglot-go/database/migrate/dialect/sqlserver"
    "github.com/flanksource/sqlglot-go/migrate"
    "github.com/flanksource/sqlglot-go/migrate/engine"
)

artifact, err := engine.Plan(engine.PlanRequest{
    Dialect: "sqlserver",
    Scope:   engine.Scope{Mode: engine.ScopeComplete, Schemas: []string{"dbo"}},
    Desired: migrate.Schema{Tables: []migrate.Table{{
        Name:    migrate.Name{Schema: "dbo", Name: "orders"},
        Columns: []migrate.Column{{Name: "id", Type: "int"}},
    }}},
})
```

Run the complete example with `go run ./examples/sqlserver-migration` from the module root. It prints generated SQL and never connects to a database. `engine.PlanLive` is the same plan against a database: it reads the capabilities, the current schema and the live dependents first. See [ARTIFACTS.md](ARTIFACTS.md) for the artifact, its scopes, forced rebuilds, preconditions and units, and how an artifact is applied, checked and rendered as a script.

`PlanRequest.Current` is the target baseline and `PlanRequest.Desired` the desired state. In `complete` scope an omitted current object or member is dropped, and each drop is a destructive change that must be acknowledged. In `declared` scope only the named objects are managed: an undeclared object is never dropped, and an omitted member of a declared table is dropped like in complete scope. A table declaration is complete unless it sets `Table.Partial` (`partial = true` in HCL): the drop of any member of a partial table (column, index, key, foreign key, check, default) is preserved in both scopes, so a partial declaration names only what it wants to manage. A partial table the target does not have is an error, because a partial declaration cannot create a table. A dependent that cannot be re-created over a dropped column, or an inbound foreign key from another table on it, fails the plan by name unless the same plan drops or declares it away. A partially selected schema must not be passed as the desired state in complete scope: it would drop the unselected members. Selection is a list of change IDs on the plan, not an edit of the schema.

Every object requires an explicit schema and catalog name. Schemas must already exist. Columns retain SQL Server types, nullability, identity seed/increment, named defaults and collation. Indexes retain ordered key columns, descending flags, included columns, filters, clustering, fill factor and primary-key and UNIQUE-constraint identity; an index read from a live database also keeps its padding, lock flags, compression and filegroup so a rebuild re-creates it faithfully. Foreign keys retain ordered referenced columns and referential actions. Checks retain their names and expressions. A foreign key may reference a table the schema does not hold, because the engine plans schemas narrowed to the objects a plan touches; the diff never reports the missing table. Duplicate objects, missing referenced columns of a table the schema does hold, invalid types and unsupported changes fail planning.

`Definition` carries a full qualified `CREATE`, `CREATE OR ALTER` or `ALTER` procedure, function, view or DML-trigger definition in `SQL`. `Kind` is `PROCEDURE`, `FUNCTION`, `VIEW` or `TRIGGER`. Triggers also require `Parent` and preserve `Disabled`. `Dependencies` names the catalog objects required by the definition; a dependency the schema does not hold is left unbound and neither created nor dropped by the diff. Capture must supply complete dependency metadata. The planner orders creates and drops by dependencies, detaches cyclic foreign keys and rejects remaining dependency cycles. Module definitions run in separate batches, with only their leading verb rewritten to `CREATE OR ALTER`; their remaining source bytes are preserved.

For native T-SQL module analysis, use `sqlglot.ParseScript` and inspect its diagnostics, or use `CanonicalSQL` for semantic comparison. These entrypoints support structured SQL Server triggers. `ParseOne` retains the upstream reference's single-statement tree contract, including opaque trigger commands. `ParseScript.Complete` describes scan completion; a successful analysis additionally requires no diagnostics and a non-nil tree.

`Artifact.Changes` contains stable IDs and destructive classifications. Each unit lists its ops and statements in execution order and the change responsible for each. Several statements can implement one change, such as replacing a named default or setting a trigger's disabled state. Drops and potentially lossy column type, collation or nullability changes are marked destructive. Callers must enforce selection, review and acknowledgments before execution; `engine.Apply` requires them.

The native model describes supported logical schema features. Inspection refuses, per object, what it cannot reproduce faithfully: encrypted definitions and unsupported physical features such as partitioning, computed columns and temporal tables. A refusal blocks a plan only when a change or a forced rebuild touches that object. There are no guessed renames, physical column reordering, data backfills or execution fallbacks.

Commons-db's SQL Server driver consumes `migrate/atlas/sqlserver` for diffing, type conversion and planning. Sqlglot imports neither commons-db nor UIR. To release a consumer of the extracted packages, publish sqlglot first, then update the consumer's pinned module version and verify with `GOWORK=off`; a workspace-only import is insufficient for release.
