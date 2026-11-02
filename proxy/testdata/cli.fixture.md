---
cwd: $GIT_ROOT_DIR
build: make build-proxy-fixtures
exec: .tmp/sqlglot
env:
  GOWORK: 'off'
---

# Proxy CLI validation

| Name | Args | Exit Code | CEL |
|------|------|-----------|-----|
| PostgreSQL requires upstream | proxy postgres | 1 | stdout == '' && stderr.contains('upstream') |
| PostgreSQL requires certificate | proxy postgres --upstream localhost:1 --client-tls require | 1 | stdout == '' && stderr.contains('certificate') |
| PostgreSQL rejects invalid TLS | proxy postgres --upstream-tls invalid | 1 | stdout == '' && stderr.contains('upstream-tls must') |
| TDS requires upstream | proxy tds | 1 | stdout == '' && stderr.contains('upstream') |
| TDS requires certificate | proxy tds --upstream localhost:1 --client-tls require | 1 | stdout == '' && stderr.contains('certificate') |
| TDS rejects invalid TLS | proxy tds --upstream-tls invalid | 1 | stdout == '' && stderr.contains('upstream-tls must') |
