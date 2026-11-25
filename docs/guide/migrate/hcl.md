---
title: HCL schema migrations
description: Reconcile an Atlas HCL declaration with a PostgreSQL, SQLite, or SQL Server database.
---

# HCL schema migrations

Use Atlas HCL to describe the schema you want. `migrate plan` compares the declaration with the target catalog and generates SQL to reconcile the differences.

See the [supported HCL reference](./hcl-reference) for blocks, types, and restrictions by target.

## Choose the input

| Input | Scope and target |
| --- | --- |
| `--bundle DIR` | Loads `.hcl` files recursively as one declaration. Omitted tables and columns are preserved by default. Use `--allow-drops` to consider omissions for removal. |
| `--desired FILE` | Treats one file as the complete desired scope. Omissions can generate drops. PostgreSQL and SQLite accept HCL in the target dialect or a matching JSON snapshot. SQL Server requires a snapshot for this flag. |

For a bundle, declare `schema "public" {}` on PostgreSQL and SQLite. PostgreSQL can remap it to an existing schema selected with `--schema`. SQLite projects supported types and constraints onto `main`. SQL Server bundles use SQL Server HCL types and declare `schema "dbo" {}`. For a single HCL file passed to `--desired`, declare the target schema directly.

## Add a column in SQLite

From a new directory, create a disposable database, a table, and a row.

```sh
sqlite3 ./inventory.db "CREATE TABLE items (id INTEGER NOT NULL PRIMARY KEY, label TEXT NOT NULL); INSERT INTO items VALUES (1, 'Notebook');"
mkdir ./schema
```

Save this as `schema/items.hcl`.

```hcl
schema "public" {}

table "items" {
  schema = schema.public

  column "id" {
    type = integer
    null = false
  }
  column "label" {
    type = text
    null = false
  }
  column "notes" {
    type = text
    null = true
  }

  primary_key {
    columns = [column.id]
  }
}
```

Plan the bundle and review the saved file.

```sh
sqlglot migrate plan --dsn ./inventory.db --bundle ./schema --out ./schema-plan.json
sqlglot migrate review ./schema-plan.json
```

Check that the plan adds the nullable `notes` column and contains zero destructive steps. For a PostgreSQL or SQLite plan with destructive steps, find the `steps` entries marked `destructive` and pass each full ID as a separate `--ack` argument. Review SQL Server schema statements in the plan summary before applying, including any destructive ones.

```sh
sqlglot migrate apply --dsn ./inventory.db ./schema-plan.json
sqlite3 ./inventory.db 'SELECT id, label, notes FROM items ORDER BY id;'
```

The query returns `1|Notebook|`. The existing row remains and its new `notes` value is `NULL`.

## Extend the bundle

- Pass values for HCL `variable` blocks using repeatable `--var 'NAME=HCL_LITERAL'` arguments, such as `--var 'label="Inventory"'`.
- On SQL Server, pass `--routines routines` to load declarative routine definitions from that subdirectory. Use one procedure or function per file and name each file after its routine. The planner compares the parsed definition with the live database.
- Keep deployment SQL alongside the declarations when you need phased scripts. PostgreSQL script bundles and PostgreSQL bundles that declare roles or permissions produce advisory previews. See [SQL script migrations](./sql) for deployment order and executable targets.
