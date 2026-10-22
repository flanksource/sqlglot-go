# SQL Server migrations

`migrate/sqlserver.Plan` compares typed current and desired schemas and generates ordered SQL without opening a database. `migrate/atlas/sqlserver` exposes the same differ and planner to Atlas consumers. Sqlglot also compiles and checks immutable migration artifacts. Inspection, credentials, application locks, transactions, execution and deployment records belong to the caller.

```go
import (
    "github.com/flanksource/sqlglot-go/migrate"
    "github.com/flanksource/sqlglot-go/migrate/sqlserver"
)

plan, err := sqlserver.Plan(migrate.Request{
    Desired: migrate.Schema{Tables: []migrate.Table{{
        Name: migrate.Name{Schema: "dbo", Name: "orders"},
        Columns: []migrate.Column{{Name: "id", Type: "int"}},
    }}},
})
```

Run the complete example with `go run ./examples/sqlserver-migration` from the module root. It prints generated SQL and never connects to a database.

`Request.Current` is the target baseline. `Request.Desired` is the complete desired state, including target members the caller intends to preserve. An omitted current object or member is dropped. Selection overlays belong above the planner; passing a partially selected schema as the desired state would drop unselected members.

Every object requires an explicit schema and catalog name. Schemas must already exist. Columns retain SQL Server types, nullability, identity seed/increment, named defaults and collation. Indexes retain ordered key columns, descending flags, included columns, filters, clustering, primary-key and UNIQUE-constraint identity. Foreign keys retain ordered referenced columns and referential actions. Checks retain their names and expressions. Duplicate objects, missing referenced tables or columns, invalid types and unsupported changes fail planning.

`Definition` carries a full qualified `CREATE`, `CREATE OR ALTER` or `ALTER` procedure, function, view or DML-trigger definition in `SQL`. `Kind` is `PROCEDURE`, `FUNCTION`, `VIEW` or `TRIGGER`. Triggers also require `Parent` and preserve `Disabled`. `Dependencies` names the catalog objects required by the definition; these must be present in the snapshot. Capture must supply complete dependency metadata. The planner orders creates and drops by dependencies, detaches cyclic foreign keys and rejects remaining dependency cycles. Module definitions run in separate batches, with only their leading verb rewritten to `CREATE OR ALTER`; their remaining source bytes are preserved.

For native T-SQL module analysis, use `sqlglot.ParseScript` and inspect its diagnostics, or use `CanonicalSQL` for semantic comparison. These entrypoints support structured SQL Server triggers. `ParseOne` retains the upstream reference's single-statement tree contract, including opaque trigger commands. `ParseScript.Complete` describes scan completion; a successful analysis additionally requires no diagnostics and a non-nil tree.

`Plan.Changes` contains stable IDs and destructive classifications. `Plan.Steps` contains SQL in execution order and the change responsible for each statement. Several statements can implement one change, such as replacing a named default or setting a trigger's disabled state. Drops and potentially lossy column type, collation or nullability changes are marked destructive. Callers must enforce selection, review and acknowledgments before execution.

The native model describes supported logical schema features. Snapshot capture must reject incomplete visibility, encrypted definitions and unsupported physical features such as partitioning, computed columns and temporal tables before constructing a request. There are no guessed renames, table rebuilds, physical column reordering, data backfills or execution fallbacks. The returned SQL is a plan; a deployment artifact must add its own baseline checks, locking, transaction, idempotence and postconditions before applying it.

Commons-db's SQL Server driver consumes `migrate/atlas/sqlserver` for diffing, type conversion and planning. Sqlglot imports neither commons-db nor UIR. To release a consumer of the extracted packages, publish sqlglot first, then update the consumer's pinned module version and verify with `GOWORK=off`; a workspace-only import is insufficient for release.
