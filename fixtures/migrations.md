---
cwd: ..
timeout: 600s
env:
  GOWORK: "off"
---

# SQL Server planner ownership

These checks verify the database-independent planner and its public contracts. Execution safety and deployment integration have separate consumer checks.

| Name | Command | Exit Code | CEL Validation |
| --- | --- | --- | --- |
| Native and Atlas planner regressions | go test ./migrate/... -count=1 | 0 | stdout.contains('migrate/sqlserver') && stdout.contains('migrate/atlas/sqlserver') |
| Parser and dialect regressions | go test ./sqlglot -count=1 | 0 | exitCode == 0 |
| Runnable planning example | go run ./examples/sqlserver-migration | 0 | stdout.contains('CREATE TABLE [dbo].[orders]') && stdout.contains('CONSTRAINT [PK_orders] PRIMARY KEY CLUSTERED') |
| Planner dependency direction | go list -deps ./migrate/sqlserver | 0 | !stdout.contains('github.com/flanksource/commons-db') && !stdout.contains('github.com/flanksource/uir') |
| Standalone build | make build | 0 | exitCode == 0 |
