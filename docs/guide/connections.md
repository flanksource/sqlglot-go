---
title: Set up database connections
description: Configure SQLite, PostgreSQL, and SQL Server targets in the browser or in a Connection YAML file.
---

# Set up database connections

Use `--dsn` to pass a database address directly, or save it once and use `connection://NAME` in the browser and CLI. For a comparison, save distinct connections for the desired schema and the database that receives the patch. For a file-based migration, save the target connection.

| Database | DSN or path | Use with sqlglot |
| --- | --- | --- |
| SQLite | `./inventory.db` or `file:/absolute/path/inventory.db` | Inspect, compare, and apply schema plans. The file must already exist. |
| PostgreSQL | `postgres://db.example:5432/inventory?sslmode=verify-full` or a PostgreSQL keyword DSN | Inspect, compare, and apply schema plans. Phased SQL bundles are advisory in the CLI. |
| SQL Server | `sqlserver://db.example:1433?database=inventory` or a SQL Server ADO/ODBC connection string | Inspect, compare, and apply schema, SQL script, and CSV plans. |
| MySQL and ClickHouse | A native driver DSN or URL, with `--driver mysql` or `--driver clickhouse` when needed | Inspect the catalog. The reviewed migration workflows target PostgreSQL, SQLite, and SQL Server. |

For a one-off remote connection, supply the complete DSN through a shell variable, for example `sqlglot schema inspect --dsn "$DATABASE_URL"`. Keep passwords out of examples and project files.

## Save a connection in the browser

1. Start `sqlglot ui` on the machine that can reach the database and open the address it prints.
2. Open **Connections**, click **Add Connection**, and set the type and a name such as `inventory-sqlserver`.
3. For SQLite, select **Value** for **Database path**, enter an absolute path on the machine running `sqlglot ui`, and leave username and password empty. For a remote database, enter the URL for the host, database, and connection options. When username and password come from separate fields, place `$(username)` and `$(password)` in the URL where the credentials belong.
4. Select credential references in the username and password fields. For Kubernetes references, choose the context and namespace that contain the Secret or ConfigMap. The machine running the app needs access to that kubeconfig and permission to read those objects.
5. For a remote connection, click **Test** to check reachability. Click **Add Connection** to save. Run an inspection afterward to verify the database login and catalog access. SQLite is verified by inspection because there is no network endpoint to probe.

For a complete local comparison example, see [Add connections and inspect a database](./inspection#try-it-with-sqlite).

## Create a Connection YAML file

Save one `Connection` resource per `.yaml`, `.yml`, or `.json` file. These directories are read in order. A later directory overrides an earlier entry with the same namespace and name.

| Layer | Directory |
| --- | --- |
| Shared | `$XDG_CONFIG_HOME/flanksource/connections` |
| sqlglot-go | `$XDG_CONFIG_HOME/sqlglot-go/connections` |
| Working directory | `./.connections` |

The config root defaults to `~/.config` when `XDG_CONFIG_HOME` is unset. `--connections-dir DIR` replaces the sqlglot-go layer for that command. The browser writes new connections to the sqlglot-go layer. Start the browser and CLI from the same working directory when they use `./.connections`.

### SQLite

Create the database with `sqlite3 ./inventory.db 'CREATE TABLE items (id INTEGER PRIMARY KEY, label TEXT NOT NULL);'`. Save the resource below in `.connections/inventory-local.yaml` and replace the path with the file's absolute path.

```yaml
apiVersion: mission-control.flanksource.com/v1
kind: Connection
metadata:
  name: inventory-local
spec:
  type: sqlite
  url:
    value: /absolute/path/to/inventory.db
```

### PostgreSQL and SQL Server

Save this as `.connections/inventory-sqlserver.yaml` after replacing the host, namespace, and kubeconfig context with yours. Create or identify the Secret with the indicated username and password keys before inspecting the database.

```yaml
apiVersion: mission-control.flanksource.com/v1
kind: Connection
metadata:
  name: inventory-sqlserver
  namespace: inventory
spec:
  type: sql_server
  url:
    value: 'sqlserver://$(username):$(password)@db.example:1433?database=inventory&encrypt=true'
  username:
    valueFrom:
      secretKeyRef: {name: inventory-db, key: username}
  password:
    valueFrom:
      secretKeyRef: {name: inventory-db, key: password}
  properties:
    kubecontext: example-dev
```

For PostgreSQL, set `type: postgres` and use a URL such as `postgres://$(username):$(password)@db.example:5432/inventory?sslmode=verify-full`. Configure trusted certificates as required by the database driver.

### Connection fields

| Field | Meaning |
| --- | --- |
| `apiVersion`, `kind` | Required. Use `mission-control.flanksource.com/v1` and `Connection`. |
| `metadata.name` | Connection name. If omitted, the name comes from the file stem. |
| `metadata.namespace` | Namespace for the connection and Kubernetes references. Use `connection://NAMESPACE/NAME` when referring to a namespaced connection. |
| `spec.type` | `sqlite`, `postgres`, `sql_server`, `mysql`, or `clickhouse` for the generic resource format shown above. |
| `spec.url.value` | Full DSN, path, or DSN template. `$(username)` and `$(password)` interpolate escaped credentials. `{{.properties.KEY}}` interpolates a connection property. |
| `spec.username`, `spec.password` | A `valueFrom` reference, or a `value` in a hand-authored resource. Each supplied field needs its placeholder in the URL. The browser saves credential references. |
| `spec.properties` | String-valued properties for the URL template. Values can be literals or references such as `secret://inventory-db/host`. |
| `spec.properties.kubecontext` | Kubeconfig context for this connection. Defaults to the current context when omitted. |

For credential `valueFrom`, use one source per field. `secretKeyRef: {name, key}` and `configMapKeyRef: {name, key}` address objects in the connection namespace. `helmRef: {name, key}` reads a Helm release value with a JSONPath key. `onePassword: 'op://vault/item/field'` uses the local `op` CLI and its configured authentication.

## Verify and reuse

```sh
sqlglot connections list
sqlglot schema inspect --dsn connection://inventory-local
sqlglot schema inspect --dsn connection://inventory/inventory-sqlserver
```

`connections list` shows each source file, layer, and any configuration warnings without resolving secrets. Inspection opens the database and reads the catalog. Use the same reference in `migrate plan --dsn …` and `migrate apply --dsn … PLAN`. For a portable project configuration, point a [target alias](./migrate/project-yaml) at the saved reference.

| If you see | Check |
| --- | --- |
| Connection missing or resolving to the wrong database | Check the working directory, config root, and layer reported by `connections list`. Qualify the name with its namespace. |
| Missing placeholder | Include `$(username)` or `$(password)` in the URL for each credential defined separately. |
| Missing Secret, ConfigMap, or context | Check the namespace, context, object name, key, and read access from the machine running sqlglot. |
| Test succeeds and inspection fails | The network endpoint is reachable. Check the database name, authentication, TLS settings, and permission to read the catalog. |
