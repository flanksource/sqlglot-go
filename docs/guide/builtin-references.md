# State-dependent built-in references

The `sqlglot` package maps selected SQL Server built-ins by the state they depend on. Consumers can record references or apply their own lint policy without maintaining a second list of SQL names.

Install the module with `go get github.com/flanksource/sqlglot-go` and import `github.com/flanksource/sqlglot-go/sqlglot`.

| Dependency | Built-ins |
| --- | --- |
| `environment` | `DB_NAME`, `DB_ID`, `SERVERPROPERTY`, `DATABASEPROPERTYEX`, `@@SERVERNAME` |
| `security` | `CURRENT_USER`, `SESSION_USER`, `SYSTEM_USER`, `USER_NAME`, `SUSER_SNAME`, `ORIGINAL_LOGIN`, `IS_MEMBER` |
| `session` | `APP_NAME`, `HOST_NAME`, `CONNECTIONPROPERTY`, `CONTEXT_INFO`, `SESSION_CONTEXT` |
| `time` | `GETDATE`, `GETUTCDATE`, `SYSDATETIME`, `SYSUTCDATETIME`, `SYSDATETIMEOFFSET`, `CURRENT_TIMESTAMP` |
| `randomness` | `NEWID`, `NEWSEQUENTIALID`, `RAND` |

`sqlglot.LookupBuiltin(name, dialect)` looks up a SQL name without parentheses. Names are case-insensitive; the dialect accepts `tsql`, `sqlserver`, or `mssql`. A match returns a `BuiltinFunction` with its uppercase `Name` and typed `Dependency`. A false result means the name or dialect is outside this mapping; it does not establish that a function is pure. Seeded `RAND` remains mapped because it changes the connection's random-generator state.

`expression.Builtin(dialect)` identifies a mapped call or system variable in an AST node. Call it while walking a tree parsed with `ParseOne` or `ParseScript`. It recognizes anonymous calls, typed function nodes, and the double-at system-variable form. The parser preserves the original identity of calls such as `GETDATE` and `SYSDATETIME` in metadata even when both use the same AST class. Copies preserve that metadata. String literals, comments, ordinary identifiers, single-at parameters, and schema-qualified user functions do not match.

Execution-state, general metadata, and locale functions are outside this mapping. The API does not assign lint severities or execute SQL.

Run the example from this checkout:

```bash
go run ./examples/builtin-references
```

Expected output:

```text
DB_NAME: environment
CURRENT_USER: security
SESSION_CONTEXT: session
GETDATE: time
NEWID: randomness
```
