# Go API

Install the module in an existing Go project:

```sh
go get github.com/flanksource/sqlglot-go
```

## Parse and generate

```go
package main

import (
    "fmt"
    "log"

    "github.com/flanksource/sqlglot-go/sqlglot"
)

func main() {
    tree, err := sqlglot.ParseOne("SELECT id FROM users LIMIT 2", "postgres")
    if err != nil {
        log.Fatal(err)
    }
    sql, err := sqlglot.Generate(tree, "postgres")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(sql)
}
```

`ParseOne` accepts one statement, including a terminating semicolon, and rejects unexpected trailing input. `Generate` writes the expression tree in the requested dialect and returns an error for unsupported generation. Handle both errors before executing or storing generated SQL.

The expression model exposes SQL constructs as nodes and named arguments. For the exported types and functions, see the [package reference](https://pkg.go.dev/github.com/flanksource/sqlglot-go/sqlglot).

## T-SQL scripts

`sqlglot.ParseScript(source, "tsql")` scans scripts and provides statement trees and diagnostics. Check the returned error, require `Complete`, and reject diagnostics or missing trees before relying on an analysis. `Complete` describes scan completion alone.

Native script analysis supports structured SQL Server routines and triggers. `ParseOne` retains the upstream reference's single-statement contract, which can represent some commands opaquely. Choose the entrypoint to match the analysis you need.

## Formatting

`sqlglot.Format(source, dialect)` formats a statement; `FormatScript` handles scripts. These change whitespace without requiring the parser to understand every construct. Use parsing when your application needs semantic inspection.

## Schema planning

The `migrate` and `migrate/sqlserver` packages compare typed schemas and generate SQL without connecting to a database. The `database` packages provide live inspection and migration execution. See [Inspection and migrations](./migrations) for the boundary between planning and execution.
