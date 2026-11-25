Read a saved plan file offline and print its summary. Pass the file as the only argument.

```sh
sqlglot migrate review .sqlglot-go/plan.json
```

Check planned changes, the rebuilds they force, the checks each unit runs first, destructive IDs, and warnings before applying. See `sqlglot migrate --help` for the plan file fields.
