---
title: Plan files, scopes and acknowledgements
description: What a reviewed schema plan holds, how its scope decides drops, what a forced rebuild and a precondition are, and why an older plan file must be planned again.
---

# Plan files, scopes and acknowledgements

`sqlglot migrate plan` writes a plan file and `sqlglot migrate apply` runs it. For a schema, the plan file is one artifact in one format for SQL Server, PostgreSQL and SQLite. The file holds the SQL, the schemas it was planned from and a hash of both. Apply plans the file's own inputs again and refuses it when anything differs, so a hand-edited statement, unit or change is refused.

## What the plan shows

`sqlglot migrate plan` prints a summary to stderr and the plan file to stdout. `sqlglot migrate review FILE` prints the same summary from the file without opening a database. The HTTP API and the web UI return the review as JSON.

| Field | Meaning |
| --- | --- |
| `dialect`, `schema` | What the plan is for. `schema` lists every schema of the scope. |
| `scope` | `complete` or `declared`, the schemas and, for `declared`, the managed objects. |
| `changes` | The planned changes. Each is `id`, `action`, `kind`, `object`, `member`, `destructive`, `reason` and `requires`. A forced rebuild is listed under the change that forced it, in `rebuilds`. |
| `preserved` | The drops the plan leaves out: those of a table declared `partial = true`, and, in a `declared` scope, those of an object nothing declares. Each says which rule applied. Nothing here runs. |
| `units` | The transactions of the plan: `id`, the `changes` and `objects` it covers, whether it is `transactional`, its `preconditions` and the IDs of its `steps`. |
| `steps` | Every statement in execution order: `id`, `sql`, `destructive`, the `change` and `unit` it belongs to, its `phase` (`drop`, `alter` or `create`) and `nonTransactional` when it cannot share a transaction. |
| `acks` | The change IDs `apply` requires acknowledged. |
| `refusals`, `warnings` | The objects inspection could not read faithfully that no change touches, and definitions compared as written. |
| `applicable` | `false` for an advisory plan, which can be reviewed and never applied. |
| `sql` | The statements as one script, with `GO` between SQL Server batches. This is not the script that guards and checks; use the plan file. |

## Scope

A `complete` scope manages every object of the schema. Whatever the desired state omits is dropped, and each drop is a destructive change. A `declared` scope manages only the objects the desired state names: an undeclared table or other object is never dropped, and its drop appears under `preserved`.

Inside a table the desired state names, the rule is the same in both scopes. A table declaration is complete unless its `table` block says `partial = true`. A complete table loses every column, index, key, foreign key and check it omits, each as a destructive change you acknowledge. A partial table loses none: those drops appear under `preserved`, and a changed column still rebuilds the dependents it forces and re-creates the members the declaration does not name. A partial table that does not exist is an error, because a partial declaration cannot create a table. A column drop that would leave a view or an inbound foreign key without what it needs fails the plan, naming it, unless the same plan drops or declares it away.

| Source | Scope |
| --- | --- |
| `--desired` HCL, pasted HCL, an inspected snapshot | `complete` |
| a bundle or project | `declared` |
| a bundle or project with `--allow-drops` | `complete` |
| `schema compare` toward a reference | `complete`, narrowed by the selected changes |

## Acknowledgements

Repeat `--ack` once per destructive change in `acks`. The value is the change ID, a JSON array of action, kind, schema, name and member: `["drop","column","main","items","omitted"]`. Quote it as one shell argument. The flag and the API field are still named `ack`, but the values are no longer the hash of a statement's position and text. A change ID names what changes, so it does not move when the planner orders or splits statements differently. A forced rebuild is never acknowledged on its own; its cause is.

## Forced rebuilds

Changing a column can force what depends on it to be dropped and created again: on SQL Server the indexes, keys, foreign keys in both directions, checks, defaults and statistics; on PostgreSQL the views; on SQLite the table. The plan shows each as a rebuild under the change that forces it, and re-creates it from its live definition so fill factor, compression and constraint trust survive. Planning fails, naming the object, when a dependent cannot be reproduced faithfully or when a foreign key's other column would not change with it.

## Preconditions

Each unit lists the data checks it makes before it changes anything. A check is a query that must return no row, such as a text column that holds a value which is not a GUID before it becomes `uniqueidentifier`, a `NULL` before `NOT NULL`, or a duplicate before a new unique index. A row fails the unit with the check's message and rolls it back. Planning does not run the checks, because each is a table scan.

## Units and resuming

Apply runs the units in order and each unit in its own transaction where the database has transactional DDL. When a unit fails, the units before it stay committed and apply reports each unit's outcome. Run the same plan file again: the units already applied report `already-applied` and the run resumes at the failed one. If the database changed in a way that matches neither a unit's baseline nor its desired state, apply fails with `drifted from the artifact baseline` and runs nothing of that unit.

## Plan files from an older version

Plan files written before this format are refused: a SQL Server artifact of version 1, a PostgreSQL or SQLite artifact with HCL snapshots, and the previews that wrapped them. The error wraps `workflow.ErrOlderVersion`, says which format it found, and asks for the plan to be made again with `sqlglot migrate plan`. They are not converted, because a plan binds its SQL to the exact state of the database it saw.

## Changes to the JSON from the earlier format

- `changes[]` now lists only the changes the diff names, each with `rebuilds[]` of the changes it forced. Each change gains `cause` (on a rebuild) and the artifact's `requires` as before.
- `steps[]` gains `change`, `unit`, `phase` and `nonTransactional`. `id` is the ID of the op and the statement's position in it; it was a hash for PostgreSQL and SQLite and a change ID for SQL Server. `destructive` is true for every statement of a destructive change.
- `acks[]` holds change IDs for every dialect.
- `scope`, `units`, `preserved` and `refusals` are new.
- The apply result keeps `hash`, `outcome` and `steps` (the statements run) and gains `units`, each with its `id`, `outcome` and statement count.
