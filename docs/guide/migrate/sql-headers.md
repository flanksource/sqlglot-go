---
title: SQL script headers reference
description: Reference for supported SQL migration directives, dependency ordering, batching, and execution.
---

# SQL script headers reference

These directives apply to SQL migration scripts selected through a bundle, positional source paths, or [project YAML](./project-yaml). SQL Server can apply script plans. PostgreSQL can preview them as advisory plans. SQL Server declarative routine files selected by `--routines` are handled separately from deployment scripts.

## Header syntax

Put directives in leading `--` line comments. Blank lines and ordinary `--` comments are allowed. The first nonblank line that is not a `--` comment ends the header, including a `/* … */` comment. Names and values are case-sensitive. Write the colon immediately after the directive name, as shown below. Other line comments are treated as comments, so check the resulting phase and policy in the plan if you suspect a typo.

```sql
-- Apply this view after the referenced view is available.
-- phase: post
-- dependsOn: sql/010_item_labels.sql
-- transaction: true
CREATE OR ALTER VIEW dbo.item_report AS
SELECT ID, Label FROM dbo.item_labels;
GO
```

| Header | Accepted values | Default and effect |
| --- | --- | --- |
| `-- phase:` | `pre`, `post` | `post`. Pre scripts run before the schema reconcile, then post scripts run after it. A mapped phase must agree with an explicit header. |
| `-- dependsOn:` | Comma-separated script IDs | No explicit dependencies by default. For ordinary bundles, IDs are bundle-relative SQL file paths. For mapped SQL, use the assigned ID. |
| `-- runs:` | `always` | Omit the header for normal change-based selection. `always` makes an otherwise eligible script pending every time. |
| `-- transaction:` | `true`, `false` | `true`. All batches in a transactional SQL Server script run in one transaction. A failure with `false` can leave partial effects that require reconciliation. |

## Dependency ordering and reruns

For `dependsOn`, write full IDs from the bundle root, even when two files share a subdirectory. Whitespace around entries is ignored. Missing IDs, duplicate dependencies, cycles, and a `pre` script depending on a `post` script are errors. Among ready scripts in the same phase, order is by name and then ID. Ordinary scripts use their paths for both. Mapped scripts can set a name in the transform result.

A changed declared prerequisite can make its dependents pending, including indirect dependents. An unchanged prerequisite selected only by `runs: always` does not make its dependents pending for that reason alone. SQL Server can also rerun a script when an observed read or function result changed since its last successful run. Explicit `dependsOn` entries set ordering even where an effect cannot be inferred from the SQL.

The planner uses the current database without running the script bodies. Changes made by apply can select additional readers on the next plan. Review `scriptPlan.actions` or the CLI summary for `+ run`, `~ re-run`, `! blocked`, and `? effects unknown`. Inspect warnings before applying. After a script with unknown effects, SQL Server rechecks the plan and can stop if a later action becomes blocked.

## SQL Server batches

Use a standalone `GO` line between batches. A `GO` inside a comment or string does not split the script. `GO` repeat counts, batch separators combined with SQL on the same line, SQLCMD directives such as `:setvar` and `:r`, and `$(...)` scripting variables are rejected. Resolve those directives or variables before planning. A script with no executable statements is skipped and reported in the plan.

For a working pair of dependent scripts and the plan commands, see [SQL script migrations](./sql).
