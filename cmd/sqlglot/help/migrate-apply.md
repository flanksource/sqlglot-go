Apply a plan file to the target `--dsn`. Pass the file `migrate plan` wrote as the only argument. Repeat `--ack` once per destructive change ID of a schema or CSV plan, as the plan lists them in `acks`. `--lock-timeout` defaults to `5s`.

```sh
sqlglot migrate apply --dsn connection://inventory .sqlglot-go/plan.json
```

The file's own hash guards only its integrity. Apply checks that the file is for this target, then re-reads the target. If anything the plan observed has changed since, apply refuses with `plan is stale` (a script plan) or `drifted from the artifact baseline` (a schema plan) and runs nothing; run `migrate plan` again. A plan file made by an older version is refused and must be re-planned. For CSV plans, pass the same source root, filters, and phase used to plan. See `sqlglot migrate --help` for acknowledgement IDs and CSV examples.
