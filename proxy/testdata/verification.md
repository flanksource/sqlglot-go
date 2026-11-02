---
cwd: $GIT_ROOT_DIR
---

| Name | Command | Exit Code | CEL Validation |
|------|---------|-----------|----------------|
| Proxy tests | env GOWORK=off go test -race ./proxy/... -timeout 90s | 0 | stdout.contains("sqlglot-go/proxy/tds") && stdout.contains("sqlglot-go/proxy/postgres") |
| CLI commands | env GOWORK=off go test ./cmd/sqlglot -ginkgo.focus='proxy commands' -timeout 90s | 0 | stdout.contains("sqlglot-go/cmd/sqlglot") |
| Fixture client | env GOWORK=off go test ./proxy/testdata/client | 0 | stdout.contains("sqlglot-go/proxy/testdata/client") |
| CLI validation | gavel fixtures proxy/testdata/cli.fixture.md --json | 0 | json.stats.passed == 6 && json.stats.total == 6 |
| Real database behavior | gavel fixtures --max-concurrent 1 'proxy/testdata/*.fixture.yaml' --json | 0 | json.stats.passed == 48 && json.stats.total == 48 |
