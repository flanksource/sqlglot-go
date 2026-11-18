# Command line

SQL commands accept one positional SQL argument, `--file`, or standard input. Use `--dialect` (or `-d`) to select a dialect; `neutral` names the default. List the available names with `sqlglot dialects`.

## Parse

```sh
sqlglot parse --dialect tsql 'SELECT TOP 2 name FROM users'
sqlglot parse --format json --dialect postgres 'SELECT id FROM users LIMIT 2'
```

The default output includes formatted SQL and the expression class. JSON output includes the reference-shaped expression tree.

For T-SQL scripts, the parser scans `GO` batches and reports unsupported statements with file, line, column, and a bounded SQL excerpt. It exits nonzero if any statement is unsupported. From a checkout:

```sh
sqlglot parse -d tsql --file testdata/tsql-routines/if_begin_else.sql
```

## Validate a query

```sh
sqlglot validate --dialect postgres 'SELECT id FROM users'
```

`validate` requires one `SELECT`, `UNION`, `INTERSECT`, or `EXCEPT` query and reports its class. It does not check execution, schema names, or read-only policy. Queries can have side effects.

## Format and tokenize

```sh
sqlglot format --dialect tsql 'select top 2 name from users'
printf 'SELECT id FROM users' | sqlglot tokens --format json
```

`format` changes whitespace and can handle SQL outside the parser's supported grammar. Formatting is not a parsing or validation check.

## Database commands

Use `sqlglot schema --help` and `sqlglot migrate --help` to explore the inspection and reviewed migration commands. Start with the read-only examples in [Inspection and migrations](./migrations).
