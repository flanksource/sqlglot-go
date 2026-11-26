---
title: Supported HCL reference
description: Supported Atlas HCL blocks, types, and restrictions for PostgreSQL, SQLite, and SQL Server.
---

# Supported HCL reference

sqlglot uses Atlas HCL for desired schemas. The bundle format depends on the target. PostgreSQL and SQLite share a portable PostgreSQL-shaped subset. SQL Server uses its own table and column declarations.

| Target | `--bundle DIR` | `--desired FILE` |
| --- | --- | --- |
| PostgreSQL | Declare `schema "public" {}`. Use `--schema` to remap the declaration onto an existing schema. | Use PostgreSQL Atlas HCL declaring the target schema, or a matching inspected snapshot. |
| SQLite | Declare PostgreSQL-shaped `schema "public" {}`. sqlglot projects supported tables and types onto `main`. | Use native SQLite Atlas HCL declaring `main`, or a matching inspected snapshot. |
| SQL Server | Declare `schema "dbo" {}` with SQL Server HCL types. | Use an inspected SQL Server JSON snapshot. To supply HCL, use a bundle. |

A bundle loads `.hcl` files recursively and evaluates them together. Cross-file references such as `table.accounts.column.id` are allowed. Omitted tables and columns are preserved by default. `--allow-drops` considers omissions on PostgreSQL and SQLite bundles. SQL Server bundles reject that switch. A `--desired` file represents the complete desired scope, so omissions can produce drops. Always review the resulting plan.

## Variables and expressions

Declare a variable once in a bundle and reference it with `var.NAME`. The CLI accepts repeatable `--var 'NAME=HCL_LITERAL'` values. Use HCL syntax for each literal, including inner quotes for strings, for example `--var 'label="Inventory"'`. Defaults can be declared in HCL.

```hcl
variable "label" {
  type    = string
  default = "Pending"
}

schema "public" {}

table "items" {
  schema = schema.public

  column "id" {
    type = integer
    null = false
  }

  column "label" {
    type    = text
    null    = false
    default = var.label
  }

  primary_key {
    columns = [column.id]
  }
}
```

`sql("…")` supplies a raw SQL expression where the dialect permits it. Default expressions, check expressions, and index predicates must work on the target engine. For cross-database bundles, choose expressions with the same meaning on both targets. sqlglot preserves their SQL text.

## PostgreSQL

Use the PostgreSQL Atlas table syntax for columns, defaults, keys, indexes, and checks. PostgreSQL-specific attributes are available when they round-trip through the supported catalog snapshot and schema differ. An executable reviewed schema plan covers one schema and its tables. Live views, materialized views, foreign tables, partitions, row-level security, standalone sequences, routines, user triggers, domains, table inheritance, and exclusion, deferrable, or unvalidated constraints fall outside the reviewed snapshot. Use `schema inspect --snapshot` to check whether the catalog fits that contract.

User triggers and exclusion, deferrable, or unvalidated constraints are usually created by a bundle's post scripts, so they do not stop a plan for their table. A plan fails only when it would break one: a retype or drop of a column that a trigger's `UPDATE OF` list or `WHEN` clause reads, or a change to the constraint itself. The error names the trigger with its definition, or names the constraint. Drop it in a pre script and re-create it in a post script.

| Table block | PostgreSQL syntax |
| --- | --- |
| `column "NAME"` | `type`, `null`, and `default`. PostgreSQL also has `identity` and stored generated `as` blocks. |
| `primary_key` | Primary key columns, usually `columns = [column.id]`. |
| `unique "NAME"` | Unique constraint over column references. |
| `index "NAME"` | `columns` or `on` parts, optional `unique`, `where`, `include`, and `type`, such as `BTREE` or `GIN`. |
| `foreign_key "NAME"` | `columns`, `ref_columns`, `on_update`, and `on_delete`. |
| `check "NAME"` | SQL predicate in `expr`. |

Common column types include `uuid`, `boolean`, `bytea`, `text`, `varchar(n)`, `integer`, `bigint`, `numeric(p,s)`, `double_precision`, `jsonb`, `date`, and `timestamptz(p)`. The Atlas references define the complete PostgreSQL syntax. Check availability against sqlglot's bundled Atlas version and reviewed table scope before using newer features. [Atlas PostgreSQL HCL reference](https://atlasgo.io/hcl/postgres) · [Atlas type syntax](https://atlasgo.io/atlas-schema/hcl-types) · [Bundled PostgreSQL type registry](https://github.com/ariga/atlas/blob/v0.38.0/sql/postgres/sqlspec_oss.go#L976)

### Roles and permissions

PostgreSQL bundles can declare roles and permissions for advisory review. A bundle containing either generates an advisory plan that `migrate apply` refuses.

| Block or field | Accepted form |
| --- | --- |
| `role "NAME"` | One role name. Boolean attributes are `external`, `login`, `inherit`, `superuser`, `create_db`, `create_role`, `replication`, and `bypass_rls`. `inherit` defaults to `true`; the others default to `false`. |
| `role.conn_limit` | Integer connection limit, default `-1`. |
| `role.comment` | Optional string. |
| `role.member_of` | List of role references, for example `[role.inventory_readers]`. |
| `permission.to` | Required grantee, such as `role.inventory_reader` or `PUBLIC`. |
| `permission.for` | Required schema, table, column, or sequence reference. |
| `permission.privileges` | Required nonempty privilege list. Use a privilege valid for the target or `ALL`. |
| `permission.grantable` | Optional boolean, default `false`. |
| `permission.for_each` | Optional collection. Use `each.value` in the permission body to expand one grant per entry. |

| Permission target | Privileges |
| --- | --- |
| Schema | `USAGE`, `CREATE` |
| Table | `SELECT`, `INSERT`, `UPDATE`, `DELETE`, `TRUNCATE`, `REFERENCES`, `TRIGGER`, `MAINTAIN` |
| Column | `SELECT`, `INSERT`, `UPDATE`, `REFERENCES` |
| Sequence | `USAGE`, `SELECT`, `UPDATE` |

## Portable bundles for SQLite

Use one `schema "public" {}` and at least one table. SQLite accepts the following PostgreSQL-shaped declarations in a bundle.

| Block | Supported form |
| --- | --- |
| `variable` | Values and defaults evaluated before projection. |
| `table "NAME"` | Declare `schema = schema.public`. Supported table attributes are named checks. |
| `column "NAME"` | `type`, `null`, and a literal or `sql(...)` default. |
| `primary_key` | Column references. |
| `unique "NAME"` | Column references. Becomes a named unique index in SQLite. |
| `index "NAME"` | Column references, optional `unique` and `where`, and default or explicit B-tree method. |
| `foreign_key "NAME"` | `columns`, `ref_columns`, `on_update`, and `on_delete`. Actions include `NO_ACTION`, `RESTRICT`, `CASCADE`, `SET_NULL`, and `SET_DEFAULT`. |
| `check "NAME"` | `expr`, preserved as SQL. |

| PostgreSQL HCL family | SQLite projection |
| --- | --- |
| `uuid` | `text` without UUID-format enforcement |
| `text`, `char`, `character`, `varchar`, `character_varying`, `bpchar`, `name` | `text`, without length enforcement |
| `json`, `jsonb` | `text` with a `json_valid` check named `<table>_<column>_json` |
| `date`, `time`, `timetz`, `timestamp`, `timestamptz` | `datetime`, without precision or timezone enforcement |
| `int2`, `int4`, `int8`, `int`, `integer`, `smallint`, `bigint`, `xid`, `xid8` | `integer` |
| `bool`, `boolean` | `bool` with SQLite numeric affinity |
| `bytea` | `blob` |
| `numeric`, `decimal` | `numeric`, without precision or scale enforcement |
| `real`, `float`, `float4`, `float8`, `double_precision` | `real` |

SQL spellings containing spaces can use `sql()`, for example `sql("timestamp with time zone")` or `sql("character varying(100)")`. SQLite rejects other type families, including arrays, enums, domains, ranges, serial types, and user-defined types. It also rejects generated and identity columns, expression indexes, per-index-column attributes, foreign-key attributes beyond the portable fields, triggers, schema objects such as views, `role` and `permission` blocks, and bundled `.sql` scripts. A check with a generated JSON-check name must use the matching JSON validation expression.

The projection rules above apply to `--bundle`. With `--desired`, use the native SQLite table syntax for `schema "main" {}` and ensure the plan can round-trip the target catalog. [Atlas SQLite HCL reference](https://atlasgo.io/hcl/sqlite)

## SQL Server bundles

Declare one `schema "dbo" {}`. SQL Server schema bundles manage declared tables and routines in `dbo`. Omitted tables, columns, routines, and extra indexes are preserved. Review any planned constraint changes.

| Block | Supported form |
| --- | --- |
| `table "NAME"` | `schema = schema.dbo`, with columns, keys, indexes, foreign keys, and checks. |
| `column "NAME"` | `type`, `null`, `default`, and optional `identity { seed, increment }`. Both identity attributes are integers. Defaults can use a literal or `sql("getdate()")`. |
| `primary_key` | Column references, optional name, and `clustered`. The key is clustered by default. |
| `index "NAME"` | `columns` or column `on` parts with optional `desc`, plus `unique`, `clustered`, `include = [column.x]`, and `where`. Indexes default to nonclustered. |
| `foreign_key "NAME"` | `columns`, `ref_columns`, `on_delete`, and `on_update`. Use SQL Server's supported actions `NO_ACTION`, `CASCADE`, `SET_NULL`, and `SET_DEFAULT`. |
| `check "NAME"` | `expr`. |

| Family | Types |
| --- | --- |
| Integers and bit | `bit`, `tinyint`, `smallint`, `int`, `bigint` |
| Exact numeric | `decimal(p,s)`, `numeric(p,s)`, `money`, `smallmoney` |
| Approximate numeric | `real`, `float(n)` |
| Character | `char(n)`, `varchar(n)`, `nchar(n)`, `nvarchar(n)`, `text`, `ntext` |
| Binary | `binary(n)`, `varbinary(n)`, `image`, `rowversion` |
| Temporal | `date`, `time(p)`, `datetime`, `datetime2(p)`, `smalldatetime`, `datetimeoffset(p)` |
| Other | `uniqueidentifier`, `xml`, `sql_variant` |

Use `sql("nvarchar(max)")`, `sql("varchar(max)")`, or `sql("varbinary(max)")` for `(max)`. `sql("timestamp")` is an alias for `rowversion`. `float` precision up to 24 bits uses `real` storage.

SQL Server rejects computed columns, user-defined and alias types such as `sysname`, and unsupported table or column attributes. It also rejects declared tables with unsupported partitioning or triggers and changes to an existing column's identity. HCL blocks for views, triggers, sequences, functions, and procedures are not supported. Use `--routines DIR` for one `CREATE [OR ALTER] PROCEDURE` or `FUNCTION` batch per `.sql` file named after the routine. Use [SQL deployment scripts](./sql-headers) for ordered executable statements, such as creating a view.

For a runnable schema migration, see [HCL schema migrations](./hcl).
