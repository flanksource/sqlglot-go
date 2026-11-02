# Database wire proxies

## Gavel behavioral fixtures

The SQL Server service looks up `connection://lab` in commons-db's shared, `sqlglot-go`, and fixture working-directory connection layers. When that record exists it is reused, including its credentials; an unreachable or invalid saved connection fails the test. When absent, Gavel provisions SQL Server through the selected Docker context (`DOCKER_CONTEXT=moshe-home`). Both paths export `FIXTURE_DSN` and the same endpoint outputs. Borrowed servers are never stopped by fixture cleanup. Remove the service's `connection` setting to always provision. This requires a Gavel build with native service connection lookup support.

Run `GOWORK=off gavel fixtures proxy/testdata/cli.fixture.md` for CLI validation without databases. Run `DOCKER_CONTEXT=moshe-home GOWORK=off gavel fixtures --max-concurrent 1 'proxy/testdata/*.fixture.yaml'` for database behavior, or select `postgres.fixture.yaml` or `sqlserver.fixture.yaml` individually. Each fixture builds the CLI and its test client. PostgreSQL uses the native commons-db `dbtest` service; SQL Server uses an isolated SQL Server 2022 container through Gavel's `mssql` service. Use a Gavel build supporting inferred exec steps and remote Docker service endpoints. No production database URLs or fixed ports are used. Gavel owns reverse-order process and database cleanup.

CEL checks compare direct and proxied results, captured SQL and row counts, parameter opt-in, server errors, Unicode packet fragmentation, certificate-verified client TLS, plaintext rejection, invalid CLI settings, and interrupt shutdown. Each database service exports `FIXTURE_DSN` for subsequent queries and active-query services. Captures and ephemeral certificates live in the fixture's managed temporary directory. The suite clears ambient PostgreSQL URL overrides and uses `COMMONS_DB_CREATE=true` to acquire a scratch lease. Set `DOCKER_CONTEXT` in the Gavel process environment, since the container runtime runs in-process.

The aggregate definition of done is `gavel fixtures proxy/testdata/verification.md`; use the same Docker context when running it. Missing database or Docker prerequisites fail explicitly.

`proxy/postgres` and `proxy/tds` forward traffic to one upstream and observe queries without rewriting application SQL. The PostgreSQL implementation is derived from telltale's `pkg/tracing/pgproxy` at `507c01f9be71`. TDS framing and RPC layouts are derived from Mori at `7e7223c1a5a3164d7145a608e5220d6991d53426`; its MIT license is included in `tds/LICENSE.mori`.

## CLI

Build with `make build`. Start either proxy, then point your application's database connection at the reported listener address:

```sh
.tmp/sqlglot proxy postgres --upstream localhost:5432 --listen 127.0.0.1:15432 --json
.tmp/sqlglot proxy tds --upstream localhost:1433 --listen 127.0.0.1:11433 --json
```

The default listener is `127.0.0.1:0`, so the OS selects a free port. Readiness and connection diagnostics go to stderr; statements stream to stdout. `--json` emits one JSON record per line. `--params` includes bind values; by default arguments are omitted. Queries themselves retain SQL literals. Interrupt stops the listener and active connections. Commands are local-only and excluded from generated HTTP routes.

Upstream addresses are `host:port`, without credentials. The original client's authentication is forwarded. Both legs default to plaintext. Configure TLS independently:

```sh
.tmp/sqlglot proxy postgres --upstream db.example:5432 --client-tls allow --tls-cert proxy.pem --tls-key proxy-key.pem --upstream-tls require --upstream-ca upstream-ca.pem
.tmp/sqlglot proxy tds --upstream db.example:1433 --upstream-tls require --upstream-ca upstream-ca.pem --upstream-server-name db.example
```

`--client-tls` accepts `disable`, `allow` (TLS and plaintext clients), or `require`. Enabled modes require both `--tls-cert` and `--tls-key` PEM files. `--upstream-tls require` verifies upstream certificates against system roots or `--upstream-ca`; `--upstream-server-name` overrides the certificate hostname. TLS uses version 1.2 or later and never retries in plaintext after a TLS failure. Supplied certificate files must already exist; the proxy does not generate production certificates.

## Go API

```go
server, err := postgres.Start(ctx, proxy.Options{
    Upstream: "localhost:5432",
    OnStatement: func(statement proxy.Statement) error {
        return json.NewEncoder(output).Encode(statement)
    },
})
if err != nil {
    return err
}
defer server.Close()
return server.Wait()
```

Import `github.com/flanksource/sqlglot-go/proxy` and either protocol subpackage. `Start` binds before returning; `Addr` reports the actual listener. Cancel the supplied context or call `Close` to stop. `Wait` reports fatal listener or callback errors. Callbacks are serialized, apply backpressure, and must not call `Close` or `Wait`. There is no retained capture database or event-drop queue.

Set `ClientTLSMode` and `ClientTLS` for incoming TLS. A non-nil `UpstreamTLS` requires verified upstream TLS; nil means plaintext. TLS configurations are cloned before use. Protocol errors are emitted as incomplete observations and close the affected connection. Callback errors stop the proxy.

## Compatibility and capture limits

PostgreSQL supports password and SCRAM authentication with `channel_binding=disable`, simple and extended queries, pipelining, COPY, and cancellation. TDS supports version 7.4 SQL authentication without MARS; TDS 8, Windows/federated authentication, and channel-bound authentication are rejected. TDS captures SQL batches and RPC calls, including `sp_executesql`, prepare/execute/unprepare handles, nullable integers, floats, booleans, strings, binary values, and PLP strings. Other RPC parameter types fail explicitly.

Rows are `-1` when unavailable. `incomplete` identifies missing completion metadata or bounded observations. TDS row counts come from the final DONE token; server failures without decoded details have a generic error. SQL parsing is not required, so unsupported SQL syntax still forwards. PostgreSQL observations above 1 MiB are reported and skipped while bytes continue forwarding; TDS messages above 1 MiB fail explicitly. Result rows and authentication messages are never included in capture output.

Run focused tests with `GOWORK=off go test -race ./proxy/...`. Real database tests are separate: provide disposable `SQLGLOT_PROXY_POSTGRES_URL` and `SQLGLOT_PROXY_SQLSERVER_URL` URLs and run `GOWORK=off go test -tags integration ./proxy -ginkgo.focus='Disposable database integration'`.
