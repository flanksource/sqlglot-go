---
title: SQL script migrations
description: Plan phased SQL scripts, review dependency and rerun decisions, and apply a SQL Server script plan.
---

# SQL script migrations

Use SQL scripts for changes you want to express as executable statements, such as adding a view or backfilling data. SQL Server supports executable script plans. PostgreSQL script bundles produce advisory previews. SQLite supports schema migrations through [HCL](./hcl).

## Create a script bundle

Use a disposable SQL Server target with an existing `dbo.ItemLabel` table containing `ID` and `Label` columns. See [saved connections](../connections) to register it as `inventory-sqlserver`. Keep the database preparation separate from the migration bundle. For example, run the following once on an empty test database.

```sql
CREATE TABLE dbo.ItemLabel (
    ID int NOT NULL PRIMARY KEY,
    Label nvarchar(100) NOT NULL
);
INSERT INTO dbo.ItemLabel (ID, Label) VALUES (1, N'Notebook');
```

Save the first script as `db/010_item_labels.sql`.

```sql
-- phase: post
CREATE OR ALTER VIEW dbo.item_labels AS
SELECT ID, Label FROM dbo.ItemLabel;
GO
```

Save the second script as `db/020_item_report.sql`.

```sql
-- dependsOn: 010_item_labels.sql
CREATE OR ALTER VIEW dbo.item_report AS
SELECT ID, Label FROM dbo.item_labels;
GO
```

The paths in a dependency header are relative to the bundle directory. SQL Server accepts `GO` batch separators. Resolve SQLCMD commands and `$(...)` references before passing the script to sqlglot.

## Set order and execution policy

Put case-sensitive directives in leading `--` comments before the first SQL statement or block comment.

| Directive | Behavior |
| --- | --- |
| `-- phase: pre` or `-- phase: post` | Run before or after schema reconciliation. The default is `post`. |
| `-- dependsOn: 010_first.sql, 020_second.sql` | Order scripts by their prerequisites. Changes to a prerequisite can make its transitive dependents pending. A `pre` script cannot depend on a `post` script. Ready scripts in the same phase are ordered by path. |
| `-- runs: always` | Select this script every time. An unchanged prerequisite selected by this rule alone leaves its dependents up to date. |
| `-- transaction: true` or `-- transaction: false` | Run transactionally by default. A failure outside a transaction can leave partial effects that require reconciliation. |

An HCL bundle can include scripts. Apply runs selected pre scripts, reconciles the declared schema, then runs selected post scripts. See the [SQL script headers reference](./sql-headers) for ordering, batch syntax, and accepted values. Use [project YAML](./project-yaml) for source mappings and connection aliases.

## Lint scripts before planning

sqlglot does not lint scripts itself. Use [`uir lint`](https://github.com/flanksource/uir/blob/main/docs/lint.md), which parses each script with sqlglot and reports findings at their source location. Save a `.uir.yaml` in the project root that maps the bundle to its dialect.

```yaml
version: 1
sources:
  - match: db/**/*.sql
    dialect: tsql
lint:
  compatibility_level: 130
```

Run `uir lint` from the project root. A statement sqlglot cannot parse is a `sql-parse` error. With `lint.compatibility_level` set, a SQL Server feature that needs a higher level is a `sql-compatibility-level` error. For example, `GENERATE_SERIES` needs level 160. Error findings make the command exit nonzero. The `dialect` field accepts `tsql`, `postgres`, or `sqlite`. Add `lint.rules` for project checks, such as rejecting writes to a table. To use Gavel baselines, snapshots, and the results UI, run `gavel lint uir` instead.

## Plan, review, and apply

```sh
sqlglot migrate plan --dsn connection://inventory-sqlserver --bundle ./db --out ./script-plan.json
sqlglot migrate review ./script-plan.json
```

For the example, confirm both scripts are new and that `010_item_labels.sql` comes before `020_item_report.sql`. In a SQL Server plan, look for these statuses in the summary or `scriptPlan.actions` in JSON.

| Status | What to review |
| --- | --- |
| `+ run` | A new script and its SQL. |
| `~ re-run` | The reason, such as changed source, an explicit dependency, or a recorded query whose result changed. For a tracked read, review both recorded and current results. |
| `! blocked` | An item requiring attention, such as a partially applied script. Reconcile it and plan again before proceeding. |
| `? effects unknown` | Statements whose effects the planner could not determine, such as some dynamic SQL. Review those statements directly. |

Planning reads the current target and executes no script bodies. A row or routine result changed by a script during apply can cause its readers to be selected on the next plan. Apply checks the ledger and observed inputs before running and refuses a stale plan.

```sh
sqlglot migrate apply --dsn connection://inventory-sqlserver ./script-plan.json
```

Verify the two views by running `SELECT ID, Label FROM dbo.item_report;` on the target. For the disposable example, it returns `1, Notebook`.
