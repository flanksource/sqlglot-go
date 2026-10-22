---
cwd: ../..
timeout: 10m
---

Provide SQLGLOT_TEST_POSTGRES_URL for a disposable PostgreSQL admin connection. The suite creates and removes isolated databases.

| Name | Command | CEL Validation |
| --- | --- | --- |
| Reviewed PostgreSQL schema execution | env GOWORK=off go test -tags integration ./database/migrate/review -count=1 | exitCode == 0 |
