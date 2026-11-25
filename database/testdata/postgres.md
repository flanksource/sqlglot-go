---
cwd: ../..
timeout: 10m
---

Provide SQLGLOT_TEST_POSTGRES_URL for a disposable PostgreSQL admin connection. The suite creates and removes isolated databases.

| Name | Command | CEL Validation |
| --- | --- | --- |
| PostgreSQL schema engine dialect | env GOWORK=off go test -tags integration ./database/migrate/dialect/postgres -count=1 | exitCode == 0 |
