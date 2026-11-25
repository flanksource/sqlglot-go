# Getting started

sqlglot-go is a Go port of [sqlglot](https://github.com/tobymao/sqlglot). Use it as a command line tool or import its parser, generator, and migration packages into a Go application.

## Install the CLI

Install the Go toolchain version declared in [go.mod](https://github.com/flanksource/sqlglot-go/blob/main/go.mod), then run:

```sh
go install github.com/flanksource/sqlglot-go/cmd/sqlglot@latest
sqlglot dialects
sqlglot parse --dialect tsql 'SELECT TOP 2 name FROM users'
```

Ensure Go's `GOBIN` (or `GOPATH/bin` when `GOBIN` is unset) is on your `PATH`. Prebuilt archives for Linux, macOS, and Windows are available on the [releases page](https://github.com/flanksource/sqlglot-go/releases).

## Build a checkout

```sh
git clone https://github.com/flanksource/sqlglot-go.git
cd sqlglot-go
make install
sqlglot parse 'SELECT id FROM users'
```

`make install` installs the current checkout into Go's binary directory. Make targets assume a POSIX shell; Windows contributors can use WSL or Git Bash.

## Install the browser app from a checkout

The browser application needs a build that includes its web assets. `make install` builds them (Node.js and pnpm required) and embeds them, so from the checkout:

```sh
make install
sqlglot ui
```

A plain `go install ./cmd/sqlglot` leaves the web assets out, and its `sqlglot ui` refuses to start.

Open the address printed by `sqlglot ui`. This installs the browser-enabled command on your normal Go binary path; subsequent sessions start with just `sqlglot ui`.

## Pick a workflow

- [Command line](./cli): parse, tokenize, format, and validate queries from an argument, file, or stdin.
- [Go API](./go-api): parse a query and generate SQL from its expression tree.
- [Set up database connections](./connections): configure a target in the browser or from a YAML file, and verify it from the CLI.
- [Add connections and inspect a database](./inspection): create and inspect two disposable SQLite databases for the comparison walkthrough.
- [Migrations](./migrate/index.md): plan and apply changes from HCL, SQL, CSV, or a comparison between saved databases.
- [Contributing](./contributing): run verification and work on this documentation site.

Parsing proves that the parser understands the statement. It does not establish read-only policy, validate schema names, or prove that a database will execute it.
