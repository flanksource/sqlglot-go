# Reviewed migration artifacts

`sqlserver.BuildArtifact(migrate.ArtifactRequest{Schemas, Request})` binds complete current and desired snapshots, their explicit schema scope, and the exact generated plan to a SHA-256 hash. The artifact owns its snapshot data; later edits to the input cannot alter it. Named collections are sorted for deterministic output while column and key order is retained.

```go
artifact, err := sqlserver.BuildArtifact(migrate.ArtifactRequest{
    Schemas: []string{"dbo"},
    Request: request,
})
```

`ValidateArtifact` checks the version, content hash and consistency with the planner. It rejects edited SQL even when somebody recomputes the content hash. A planner upgrade that changes emitted SQL requires a new preview. The hash provides integrity and review identity; authorization and trusted storage remain the caller's responsibility.

`CheckArtifact(artifact, actual)` uses native SQL semantics to compare a complete live snapshot. It returns `ArtifactApplied` when the entire desired state already exists, or `ArtifactPending` when the baseline still matches. Any other state fails with drift details. This includes changes to unselected target members captured in the baseline. Catalog expression formatting is compared through sqlglot rather than raw text hashes.

These guards run in Go and do not open a database. They do not make the plan's individual SQL strings a self-guarding deployment script. A database executor must inspect under the same transaction and application lock used to execute the exact reviewed steps, check the complete postcondition, and commit only after it matches. Commons-db's `migrate/sqlserver` supplies this execution path.

Run `go test ./migrate/... -count=1`. UIR's `go run ./examples/sql-migration` demonstrates compiling a selected member into an artifact without a database.
