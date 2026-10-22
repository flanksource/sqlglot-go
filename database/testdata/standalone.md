---
cwd: ../..
timeout: 10m
---

| Name | Command | CEL Validation |
| --- | --- | --- |
| Standalone database and CLI behavior | env GOWORK=off go test ./database/... ./cmd/sqlglot -count=1 | exitCode == 0 |
| Pure planner regressions | env GOWORK=off go test ./migrate/... -count=1 | exitCode == 0 |
| No commons-db runtime dependency | env GOWORK=off go list -deps ./database/... | exitCode == 0 && !stdout.contains("github.com/flanksource/commons-db") |
| CLI build | env GOWORK=off make build | exitCode == 0 |
| Disposable SQLite example | env GOWORK=off go run ./examples/database | exitCode == 0 && stdout.contains("applied") && stdout.contains("already-applied") |
