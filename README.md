# sqlglot-go

A Go port of [sqlglot](https://github.com/tobymao/sqlglot), verified
statement-by-statement against the Python reference.

**What it is:** sqlglot's architecture — tokenizer, data-driven parser,
expression tree, dialects as override sets — in pure Go, built outward from
what a read-only SQL guard needs. Every construct in a parsed statement is a
visible node; a construct the port does not know is a parse error, never a
silent pass.

**What it covers:** the tokenizer, parser, and generator for twelve named
dialects plus neutral; the first consumer's corpus; and simplify, annotate,
diff, anonymize, and JSONPath. **What it does not cover yet:** 40 statements
whose comments generate drops, 2 annotate cases that cannot be written back,
7 JSONPath selectors, one known diff exclusion, and 21
named dialects with no consumer. A transpiler is not a product goal. The
order is a measured gap, not the library's table of contents.
`docs/17-sqlglot-go.md` in
[data-agent-service](https://github.com/calvinchengx/data-agent-service),
whose Go executor is the first consumer, is stale.

## Command line app

Build the CLI with `make build` and run `.tmp/sqlglot`, install the current checkout with `make install` into Go's `GOBIN` (or `GOPATH/bin`), or install a tagged release with `go install github.com/flanksource/sqlglot-go/cmd/sqlglot@latest`. Releases attach Linux, macOS, and Windows archives for amd64 and arm64 to the GitHub release.

```sh
sqlglot dialects
sqlglot parse --format json --dialect tsql 'SELECT TOP 2 name FROM users'
sqlglot validate --file query.sql
sqlglot format --file query.sql
printf 'SELECT a FROM t' | sqlglot tokens --format json
```

`parse` shows formatted SQL and its expression class; `--format json` includes the reference-shaped tree. `validate` exits nonzero unless the input parses as one `SELECT`, `UNION`, `INTERSECT`, or `EXCEPT` query, and reports the class on success. It does not check database execution, schema names, or read-only policy; a parsed query may still have side effects. `format` only changes whitespace and also works on SQL that the parser does not support. SQL can come from one positional argument, `--file`, or stdin. `neutral` is the name for the default dialect; `dialects` lists the other supported names. SQL command errors begin with the selected dialect, or `neutral` when no dialect is selected.

For T-SQL scripts, `parse` scans every `GO` batch and reports each unsupported statement with a dialect prefix, file, line, column, and a bounded highlighted SQL excerpt. It exits nonzero if any statement is unsupported. `--format json` emits structured diagnostics for tooling. `validate` still requires one query.

```sh
sqlglot parse -d tsql --file testdata/tsql-routines/if_begin_else.sql
sqlglot parse -d tsql --format json --file testdata/tsql-routines/refuse/unknown_statement.sql
```

## Coverage against data agent service

The SQL the first consumer is actually held to -- the gold answers in its
evaluation suites, the statements its guard permits, and the ones its
adversarial corpus requires it to refuse for a named reason. **This is the
number that decides when the Go guard can be rewritten over a parse tree**, and
it is deliberately a different question from the one below.

<!-- service:start -->
| Category | Parsed | Of |
|---|---|---|
| `must_name_the_statement` | 13 | 13 |
| `must_parse` | 105 | 105 |
| `must_parse_to_refuse` | 29 | 29 |
<!-- service:end -->

**Every category is complete**, which is the condition for rewriting the Go
guard over a parse tree rather than over a token scan.

`may_refuse_unparsed` no longer appears: the single statement in it is
`SELECT * FRO dbo.x`, which the reference rejects too, so there is nothing for
the port to be held to. The category is still extracted and counted -- it is
absent from this table, not from the corpus.

These figures are written from `testdata/service/coverage.json` by
`make readme`, and CI fails if they disagree with what the suite measured. They
were hand-copied until the corpus generator was found reading a file the
statements had moved out of -- reporting 96 where there were 148, while this
table went on showing the old numbers, because a copy nobody checks is a second
source that drifts.

`must_parse` is SQL the guard permits: a gap there is a question the agent
answers today and would stop answering. `must_parse_to_refuse` is SQL the guard
refuses for a reason that depends on the tree -- "table function", "not
queryable", "cross-database" -- where failing to parse still refuses the
statement, but for the wrong reason, and the conformance suite checks the
reason. `must_name_the_statement` is refused for what the statement IS, a write
or two statements, which takes naming rather than parsing. In
`may_refuse_unparsed` any refusal is right.

Regenerate with `make service`; nothing in it is authored here.

## Coverage against the reference

<!-- coverage:start -->
| Dialect | Tokens | Trees matched | Of | Unparsed | Mismatched |
|---|---|---|---|---|---|
| neutral | 997/997 | 997 | 997 | 0 | 0 |
| tsql | 613/613 | 613 | 613 | 0 | 0 |
| postgres | 857/857 | 857 | 857 | 0 | 0 |
| duckdb | 1631/1631 | 1631 | 1631 | 0 | 0 |
| databricks | 426/426 | 426 | 426 | 0 | 0 |
| redshift | 307/307 | 307 | 307 | 0 | 0 |
| materialize | 51/51 | 51 | 51 | 0 | 0 |
| risingwave | 14/14 | 14 | 14 | 0 | 0 |
| fabric | 47/47 | 47 | 47 | 0 | 0 |
| presto | 596/596 | 596 | 596 | 0 | 0 |
| trino | 253/253 | 253 | 253 | 0 | 0 |
| dremio | 61/61 | 61 | 61 | 0 | 0 |
| mysql | 638/638 | 638 | 638 | 0 | 0 |
| sqlite | 256/256 | 232 | 256 | 24 | 0 |
| clickhouse | 538/538 | 355 | 538 | 183 | 0 |
<!-- coverage:end -->

Reference: sqlglot `ceb5111421e9` (v30.17.0-64). **Mismatched is the number
that matters** and must be zero: it counts statements the port parsed into a
*different* tree than the reference. Unparsed is the honest size of the gap.

The port also writes SQL back out, and is held to the reference's own output
string for string: **6,451 of the statements it parses are written back
identically, none is written wrongly, and none is refused**, and the guard's own rewrite -- inject a row ceiling, emit --
lands as `TOP 500` in T-SQL and `LIMIT 500` in DuckDB from the same edit to the
same node. Where a dialect would transform a statement in a way the port does
not perform, the generator **refuses** rather than emit something close: an
executor that quietly wrote a different statement from its counterpart would be
worse than one that declined.

The tokenizer is complete and has no gap tier: every statement the reference
lexes, the port lexes into the same tokens — same types, same text, same line,
column and offsets, same attached comments. A tokenizer has nowhere to
legitimately give up, because the parser above it cannot see what it drops.
The configured reference corpus parses in full: **6,491 of 6,491**, with
nothing unparsed. A construct outside that grammar is still an
`ErrUnsupported`, never a tree that merely looks plausible. That is why
**mismatched is zero** and is the number to watch.

## How it is verified

`testdata/expected/` holds 6,491 statements — sqlglot's own `identity.sql`, its
**whole** dialect suite, and a set chosen to reach the lexical corners those
miss (plus the T-SQL routine forms of `testdata/tsql-routines/` the reference
reads) — each with the token stream and the tree the reference produced.

"Whole" is the part worth stating. sqlglot pins a dialect's behaviour two ways:
`validate_identity("…")`, a statement that round-trips, and
`validate_all(…, read={"duckdb": "…"}, write={"tsql": "…"})`, one concept and
how each dialect spells it. Reading only `validate_identity` out of
`test_<dialect>.py` — which is what this harness did at first — missed both the
second form and every file not named after one of our dialects. That was most
of it: the largest single source of DuckDB statements is
`tests/dialects/test_snowflake.py`, and the largest overall is
`tests/dialects/test_dialect.py`, 5,448 lines organised by CONCEPT rather than
by dialect. That harvest took the corpus from 2,171 to 4,506 and found
**31 statements the port parsed into a different tree**, including two —
`IS [NOT] DISTINCT FROM` and typed division — that had been found the
expensive way, by fuzzing, while sitting in the reference's own tests all
along.

The function catalogue is probed, not transcribed: each builder is run and the
node it returns is read back. That probe is only as good as the arguments it
runs the builder with, and a builder that INSPECTS what it was handed yields a
spec that is right for a plain column and wrong for real SQL — DuckDB's
`DATE_TRUNC` builds a `DateTrunc` over a DATE cast and a `TimestampTrunc`
otherwise, and `LOWER(HEX(x))` is a `LowerHex`, not a `Lower`. So every spec is
put to a cast, a nested call, a string, a number and a subquery, and kept only
if it survives all of them. A name whose spec does not survive is **refused**,
because a plausible tree the reference never builds is the one thing this port
must not produce. `go
test ./...` runs every one through the port and diffs both. The expectations
are committed, so the Go tests need no Python; regenerating them (`make
oracle`) refuses to run against any commit other than the one `NOTICE` pins.

The keyword and dialect tables the tokenizer reads are generated from the same
pin rather than transcribed, and CI regenerates and diffs them, because a
hand-edited table is a divergence the port has no logic to catch.

### The optimizer

`sqlglot.Simplify` is the first thing in the port that CHANGES a tree rather
than reproducing one, and it is held to the reference's own contract —
`testdata/simplify.json`, 486 pairs pinning what each statement becomes.
**468 are folded exactly**; the rest the port declines to fold that far,
which costs nothing: the statement still means what it meant.

Every rewrite must also **survive being written down**: the port writes the
simplified tree, reads the SQL back, and requires the same tree — up to the
associativity of AND/OR, which reshapes nesting without changing meaning. That
invariant needs no engine, which matters because most of this contract is bare
predicates over undefined columns that can never reach the execution oracle.
It caught `A AND (A OR B)` being flattened to `A AND A OR B`, which
re-associates to `(A AND A) OR B` and asks a different question, and it caught
folded negatives being built as `Literal(-1)` where the reference builds
`Neg(Literal(1))`.

The gate deliberately does *not* try to judge whether a non-exact result is
*wrong*. A first cut guessed — "if the output differs from the input, the fold
must be wrong" — and reported 35 failures, of which nearly all were partial
folds and the one real bug was buried among them. Whether a rewrite is wrong
is a semantic question, so simplify's output is fed to the execution oracle
below and judged there, by running it.

Every rule is conservative: where a precondition cannot be established the node
is left alone. The reference runs `annotate_types` before simplifying and
several of its rules decide on what that leaves behind; those rules are not
ported, and the nodes they would touch are not guessed at.

### What fidelity cannot prove

Everything above compares the port against sqlglot: same tree, same string.
That proves the two agree; it cannot prove either is *right*. The distinction
does not matter while the port only reads and writes — a wrong tree shows up
as a different tree — but it stops holding the moment anything **rewrites** a
tree. A rewrite that is wrong but plausible still parses, still round-trips,
and still runs. It just returns different rows.

So there is one more harness, and it is the only one here whose failure means
"these two queries ask different questions" rather than "these two strings
differ". `make oracle-exec` takes each statement as it was written, what
the port writes back, and what the port *simplifies* it to, runs them **all**
on a real engine, and compares the
results. The build fails if comparable DuckDB statements fall below **385**,
or Postgres below **83**. Fifteen known divergences are recorded and are not
worked around. PostgreSQL is skipped without `PGDSN`; CI supplies it as a
service container and `make postgres` starts one locally. An engine it cannot
reach is skipped with a note, not a failure.

Most of the corpus is a transpiler's test suite rather than a workload: it
says `SELECT x FROM t` and never creates `t`. `testdata/fixtures/schema.sql`
supplies those tables, and their names are not invented — they are harvested
by parsing every statement with sqlglot and keeping the ones that name
exactly one table, so a column is only attributed to a table that
unambiguously owns it. The fixtures have **rows**, deliberately: an empty
table would be worse than no table, because two different queries over
nothing both return nothing and would "agree". The harness also refuses to
count an agreement where both sides returned no rows.

What is left over is non-deterministic, which the harness *detects* rather
than being handed a list of keywords like `RANDOM` to avoid: it runs **both**
sides several times and requires each to agree with itself. Checking only the
input was not enough — `USING SAMPLE 10%` returned the same thing twice and
something different on the sixth run, and an unstable rewrite was reported as
a divergence that was not one.

Neither Databricks nor the neutral dialect has an engine here: Spark is not a
service container, and the neutral dialect rewrites 8 statements out of 991,
so there would be almost nothing to compare.

The reference's own output is checked alongside the port's, for a reason worth
stating: the port reproduces sqlglot byte for byte on most of the corpus, so a
semantic bug in sqlglot's round trip is one the port inherits *silently*, and
no differential against sqlglot can ever see it. The first runs found **six**, all recorded in
`docs/upstream-issues.md` and none worked around — reproducing the reference
is the point of the port. Two are worth naming, because they run, return a
value, and the value is wrong:

- sqlglot rewrites DuckDB's reversing slice `[:-:-1]` into `[:-1:-1]`, turning
  a reversed list into its last element.
- sqlglot rewrites PostgreSQL's binary integer literal `0b1010` into the
  bit-string literal `b'1010'` — `10` becomes `'1010'`.

The rest change a result's *type* (`DATE_PART` is `double precision`,
`EXTRACT` is `numeric`; `date_add` is `timestamptz`, `+` is `timestamp`) or
produce SQL that does not run at all.

The engines live on the Python side because DuckDB in Go means cgo, and the
differential above proves the port on five platforms with a Go toolchain and
nothing else — including Windows on arm64, where no such library exists. The
Go side emits the pairs; Python runs them.

A divergence fails the build. A coverage regression below `testdata/floor.json`
fails the build. An unexplained execution divergence, or a drop below the
comparable floor in `testdata/execution.json`, fails the build. Test coverage below 95% fails the build; it currently stands
at 100% of statements. The harness proves it can fail
(`TestTheHarnessCanTellRightFromWrong`).

```sh
make test       # the differential run
make coverage   # both coverage numbers
make service    # re-extract the corpus of SQL data agent service is held to
make gaps       # why the port refuses what it refuses, most common first
make cover      # test coverage of the port
make oracle     # regenerate expectations and generated tables from the pinned reference
make oracle-exec # run the port's SQL through an engine and check it MEANS the same
make test       # includes the optimizer against sqlglot's simplify contract
make postgres   # start a PostgreSQL for it, and print the DSN to export
```

## Schema migration planning

The [migration packages](migrate/README.md) compare typed SQL Server snapshots and generate ordered DDL without a database connection. They also expose an Atlas adapter used by commons-db. Run `go run ./examples/sqlserver-migration` for a planning example that prints SQL without executing it.

## T-SQL Extended Events sessions

`CREATE`, `ALTER` and `DROP EVENT SESSION` have no grammar in the reference, which warns and keeps each as a `Command` (the port refused CREATE and DROP outright). Under `tsql` the port reads them into nodes of its own, so a caller can build the statement as a tree and let `Generate` do the quoting: a session name is an `Identifier` (`[a]]b]` when quoted), a file path is a `National` or `Literal` (`N'it''s.xel'`), and a predicate is an ordinary condition. No statement in the reference corpus has this shape, so no compared tree changes; ALTER EVENT SESSION is the one place the port now returns a tree where the reference returns a `Command`, which is the same choice the T-SQL routine bodies made. Any other form is still refused.

| Class | Arg | Holds |
|---|---|---|
| `CreateEventSession` | `this` | `Identifier`, the session name |
| | `scope` | `"SERVER"` or `"DATABASE"` |
| | `events` | `[]EventSessionEvent`, at least one |
| | `targets` | `[]EventSessionTarget` |
| | `options` | `[]EventSessionOption`, the `WITH (...)` list |
| `AlterEventSession` | `this`, `scope` | as above |
| | `state` | `"START"` or `"STOP"`, alone; or else |
| | `events`, `targets`, `options` | as above, where an event or target may carry `drop` |
| `DropEventSession` | `this`, `scope` | as above |
| `EventSessionEvent` | `this`, `package` | `Identifier`s: `sqlserver.rpc_completed` |
| | `set` | `[]EventSessionOption`, the event's `SET` |
| | `actions` | `[]Column`, `table` the package: `sqlserver.sql_text` |
| | `where` | any condition, written inside `WHERE (...)` |
| | `drop` | `true` for `DROP EVENT`, which takes nothing else |
| `EventSessionTarget` | `this`, `package` | `Identifier`s: `package0.event_file` |
| | `set` | `[]EventSessionOption`, written `(SET ...)` |
| | `drop` | `true` for `DROP TARGET` |
| `EventSessionOption` | `this` | `Var`, the option's name |
| | `value` | a `Var` for a word (`OFF`), else any expression (`Literal`, `National`, `Paren`) |
| | `unit` | `"SECONDS"`, `"KB"` or `"MB"`, optional |

An XE predicate function such as `sqlserver.equal_i_sql_unicode_string(sqlserver.database_name, N'x')` is a `Dot` over an `Anonymous`, as anywhere else, and is written as it stands rather than compared with zero. A `Var` is written verbatim, so the writer refuses one that is not a single word.

```go
arg := func(k string, v any) sqlglot.Arg { return sqlglot.Arg{Key: k, Value: v} }
ident := func(s string) *sqlglot.Expression { return sqlglot.New("Identifier", arg("this", s)) }
nodes := func(n ...*sqlglot.Expression) []*sqlglot.Expression { return n }
session := sqlglot.New("CreateEventSession",
	arg("this", sqlglot.New("Identifier", arg("this", name), arg("quoted", true))),
	arg("scope", "SERVER"),
	arg("events", nodes(sqlglot.New("EventSessionEvent",
		arg("this", ident("rpc_completed")), arg("package", ident("sqlserver")),
		arg("actions", nodes(sqlglot.New("Column", arg("this", ident("sql_text")), arg("table", ident("sqlserver"))))),
		arg("where", sqlglot.New("GTE",
			arg("this", sqlglot.New("Column", arg("this", ident("duration")))),
			arg("expression", sqlglot.New("Literal", arg("this", "1000"), arg("is_string", false)))))))),
	arg("targets", nodes(sqlglot.New("EventSessionTarget",
		arg("this", ident("event_file")), arg("package", ident("package0")),
		arg("set", nodes(sqlglot.New("EventSessionOption",
			arg("this", sqlglot.New("Var", arg("this", "filename"))),
			arg("value", sqlglot.New("National", arg("this", path))))))))),
	arg("options", nodes(sqlglot.New("EventSessionOption",
		arg("this", sqlglot.New("Var", arg("this", "MAX_DISPATCH_LATENCY"))),
		arg("value", sqlglot.New("Literal", arg("this", "1"), arg("is_string", false))),
		arg("unit", "SECONDS")))))
ddl, err := sqlglot.Generate(session, "tsql")
// with name "trace]x" and path `C:\xe\it's.xel`, on one line:
// CREATE EVENT SESSION [trace]]x] ON SERVER ADD EVENT sqlserver.rpc_completed
//   (ACTION (sqlserver.sql_text) WHERE (duration >= 1000))
//   ADD TARGET package0.event_file (SET filename = N'C:\xe\it''s.xel')
//   WITH (MAX_DISPATCH_LATENCY = 1 SECONDS)
```

## Working on Windows

The port is pure Go with no dependencies, and the tests need nothing but the Go
toolchain -- the expectations are committed, so there is no Python to install,
no native library, nothing to fetch. CI runs the full differential on **Linux,
macOS and Windows, amd64 and arm64**, and it invokes `go` directly rather than
`make`, so all five platforms are proven by the same commands you would type:

```powershell
go test ./...
go vet ./...
go test ./... -coverpkg=./sqlglot/ -coverprofile=cover.out
go test ./harness/ -run TestGapReport -v
```

**The `Makefile` is convenience, not the build.** Its targets assume GNU make
and a POSIX shell, so on Windows they want WSL or Git Bash. Nothing is lost by
skipping it: `make test` is `go test ./...`, and `make gaps` is the last command
above with the file-and-line prefix stripped.

The remaining targets -- `oracle`, `service`, `lint` -- regenerate the committed
expectations and the generated tables, or lint in a container. Those need
Python, a checkout of the pinned sqlglot, and Docker. The asymmetry is
deliberate: **contributing to the port needs a Go toolchain; maintaining the
oracle needs more.** A contributor on Windows is held to exactly the same
differential as everyone else without installing any of it.

## License

Apache 2.0. The tokenizer design, expression model, parser architecture and
dialect pattern derive from sqlglot, © Toby Mao, MIT — reproduced in
`LICENSE.sqlglot` and credited in `NOTICE`.
