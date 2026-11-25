---
title: Add connections and inspect a database
description: Save your database connections, test them, inspect their schemas, and return to them later.
---

# Add connections and inspect a database

Save a connection once, then choose it whenever you want to inspect a database or compare its schema. This guide creates two connections, checks that they work, and shows how to return to them in your next session.

## 1. Open sqlglot

With the [browser-enabled sqlglot app installed](./getting-started#install-the-browser-app-from-a-checkout), start it:

```sh
sqlglot ui
```

Open the address printed in your terminal. Keep that terminal running while you use the app. The navigation contains **Connections**, **Compare**, and **History**.

## 2. Choose your databases

For a comparison, you need two connections to the same kind of database:

| Connection | Purpose |
| --- | --- |
| `inventory-reference` | The database with the schema you want |
| `inventory-target` | The database you want to update |

You can use existing development databases. To follow the whole guide without a database server, create the two SQLite files below instead.

### Try it with SQLite

Download <a href="/examples/migrations/reference.sql" download>reference.sql</a> and <a href="/examples/migrations/baseline.sql" download>baseline.sql</a> into a new working folder. With `sqlite3` installed, run these commands in that folder:

```sh
sqlite3 inventory-reference.db '.read reference.sql'
sqlite3 inventory-target.db '.read baseline.sql'
pwd
```

Both databases contain an `items` table with two rows, `Notebook` and `Pencil`. The reference also has a `notes` column; the target does not. The migration guide will carry that column across without changing the rows.

Use the folder printed by `pwd` to form each database's absolute path, such as `/Users/you/inventory-guide/inventory-reference.db`. The path must be accessible to the computer running `sqlglot ui`. Creating a saved connection does not create the database itself.

## 3. Add the reference connection

1. Open **Connections**.
2. Click **Add Connection**.
3. Choose your database **Type**. For the local example, choose **sqlite**.
4. Enter `inventory-reference` as the **Name**.
5. For SQLite, enter the absolute path to `inventory-reference.db` under **Database path**, using the **Value** source. For a remote database, enter its connection URL.
6. For SQLite, leave username and password empty. For a remote database, select the credential references provided for that database.
7. For a remote database, click **Test** and read the result. Correct any connection or credential errors before continuing. For the local SQLite example, verify the file with the inspection command in step 5 below.
8. Click **Add Connection** to save it.

The connection now appears in the Connections list. Testing checks the current form; saving is the separate step that makes it available next time. Remote credentials are selected through references rather than saved as literal passwords. The current **Test** action probes network reachability and cannot test a local SQLite file; SQLite inspection actually opens and reads the file.

## 4. Add the target connection

Repeat **Add Connection**, using the name `inventory-target` and the target database's URL. For the local example, choose **sqlite** and the absolute path to `inventory-target.db` under **Database path**. Save it, then inspect both connections below.

You should now see both saved connections. Check their names and destinations before comparing them: the reference is the schema to copy from, and the target is the database to change.

## 5. Inspect a saved connection

You can use the same saved names from the terminal:

```sh
sqlglot connections list
sqlglot schema inspect --dsn connection://inventory-reference
sqlglot schema inspect --dsn connection://inventory-target
```

The inspection result lists schemas, tables, columns, and other available database objects. In the SQLite example, find `items`: the reference has `id`, `label`, and `notes`; the target has `id` and `label`.

Inspection reads the database without changing its schema. If the result says it was truncated, it is a partial catalog. The comparison workflow checks the complete supported migration schema before building a patch.

If a connection is saved in a namespace, use the namespace shown in Connections, for example `connection://default/inventory-reference`.

### Write SQL Server tables as HCL

To declare tables a SQL Server database already has, write their HCL from the live catalog instead of typing it:

```sh
sqlglot schema inspect --dsn connection://erp --hcl --table AsPolicy --table AsClient > tables.hcl
sqlglot schema inspect --dsn connection://erp --hcl --out db/tables
sqlglot schema inspect --dsn connection://erp --hcl --table AsPolicy --columns AsPolicy=PolicyGUID,Status --out db/partial
```

The HCL reads back, through a bundle, to the same tables: column types, nullability, defaults and their constraint names (a name SQL Server generated is left out, because it differs in every database), identity, collations that differ from the database default, the primary key with its name and clustered flag, unique constraints, indexes with key direction, included columns, filter and clustered flag, foreign keys with their actions, and checks. Planning the export against the database it came from, in declared scope, has no changes.

A table declaration is complete unless its `table` block says `partial = true`: a bundle drops the columns, indexes, keys and checks a complete declaration omits, and keeps every member a partial one omits. A full export is complete and never writes the attribute. An export made with `--columns` writes `partial = true` in the block of each table it declares in part, so adopting a few columns of a table you do not own can never drop the rest of it. A partial declaration cannot create a table.

| Flag | Meaning |
| --- | --- |
| `--hcl` | Write HCL instead of the catalog (SQL Server only; not with `--snapshot`). |
| `--table NAME` | A table to write, repeatable. Without it every table of the schema is written. |
| `--out DIR` | Write one file per table into `DIR`, each starting with its own `schema "dbo" {}` block (a bundle reads the repeated block as one schema). Without it one document goes to stdout. CLI only. |
| `--document` | With `--out`, write a single `schema.hcl` instead of one file per table. |
| `--columns TABLE=COLUMN,COLUMN` | Declare only those columns of a table, with no keys, indexes, foreign keys or checks, and write `partial = true` in its block. Repeatable; for a table another team owns. |
| `--omit-unsupported` | Leave out what the HCL cannot declare, with one `# omitted:` comment line each, instead of failing. |

A foreign key is written only when the table it references is in the same export, so select related tables together. ROWGUIDCOL, index options (fill factor, `PAD_INDEX`, `IGNORE_DUP_KEY`, row and page locks, `STATISTICS_NORECOMPUTE`, data compression, filegroup), `NOT FOR REPLICATION`, and what the inspector refuses to read (partitioned tables, computed columns, disabled indexes) fail the export for that table, naming the table, the member and the attribute. Only tables are exported.

## 6. Reuse a saved connection

You do not need to add the connection again when you return:

1. Start `sqlglot ui` again if you stopped it.
2. Open **Connections** and find `inventory-reference` or `inventory-target`.
3. Click the connection to reopen its saved settings.
4. For a remote database, click **Test** to check reachability. For SQLite, run the saved connection's inspection command again.
5. If you changed a setting, click **Save**. Otherwise, close the editor and use the existing connection.

Saved connections persist across browser and application restarts on the same computer and user account. They describe how to connect; they do not keep a database session open after you stop the app. Inspection opens a connection when needed.

You can bookmark the connection's page to reopen its editor directly. If the database moved, update the saved connection and test it again rather than adding a duplicate. A connection marked read-only can be viewed and tested here, but must be edited at its source.

## 7. Return to an earlier inspection

Open **History**, choose **Inspection**, and click the time of the run you want to read. The saved result is available even when the original database is offline.

Opening history shows what was inspected at that time. To see the database's current schema, run `sqlglot schema inspect` again using the saved connection name.

## Continue with a migration

Follow [Compare, review, and apply a migration](./migrations) to choose these two saved connections, review their differences, create a patch, leave and resume the work, and apply the result.
