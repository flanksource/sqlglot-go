# Web UI

`make build` builds the frontend (when its sources changed) and embeds it in `.tmp/sqlglot`; `make install` does the same for the installed `sqlglot`. Start `.tmp/sqlglot ui` and open the printed loopback address. Development uses `.tmp/sqlglot ui --dev` to serve the same typed command API with Vite.

The navigation provides Compare, Inspect, Migrations, SQL Tools, History and Connections. About shows the version of the running server.

## SQL Tools

Paste SQL or upload a `.sql` file, select a dialect, then choose Parse, Validate, Format or Tokens. Parse shows the expression tree and generated SQL. Validation errors identify source positions and mark the editor. Format presents its output separately; Replace editor content is an explicit action. Uploads and SQL drafts stay in memory and disappear on reload.

## Inspection

Choose a saved connection or enter a connection string. Catalog mode provides a bounded object catalog; Migration snapshot captures complete input for the existing migration planner. The result can be browsed, copied, exported or downloaded without inspecting the database again. Saved connections also offer Fields and capabilities, including target discovery, sampling and metadata refresh. Connection rows link directly to that screen.

## Migration review and application

Choose the target and provide pasted/uploaded HCL, snapshot JSON, a bundle folder, or reviewed artifact JSON. A bundle upload preserves paths relative to the selected root; variables use one `name=HCL-literal` per line. Routine directory and omitted-object removal controls retain the CLI planner's meaning and support limits.

Generate reviewed plan opens the existing planner without applying changes. Review displays scope, hash, ordered SQL, warnings and destructive acknowledgements. Download artifact preserves the reviewed JSON. Advisory plans remain visible and cannot be applied.

Select each required acknowledgement, set a positive lock timeout, then choose Review application. The confirmation names the selected target, schema and reviewed hash. Confirming submits the exact artifact, hash and acknowledgements to the existing migration engine. Target drift, invalid hashes and missing acknowledgements fail. The outcome distinguishes applied, already applied, failed and commit unknown. Inspect the database after commit unknown before starting another application; neither reload nor error recovery automatically retries application.

Compare patches use this same review/application flow through Review application. A new application is recorded as a child of the reviewed run.

## History and exports

History filters by operation kind and saved connection, with a maximum-record limit. Opening a recorded inspection or migration uses its dedicated screen and reads stored results. It does not repeat the database action. Run progress and errors remain available through the task history.

Result export selects a renderer and optionally filters object rows with CEL, such as `count > 0`. Rendering operates on the existing result and never repeats inspection, comparison or application. The unfiltered result and original migration artifact remain downloadable.

## Connection strings and browser inputs

Raw connection strings remain in transient browser memory and are excluded from navigation URLs and storage. Recorded connection metadata and payload fields redact credentials. Reopening an unsaved connection requires re-entry before another database action. Saved connection references can be bookmarked.

Browser inputs contain file contents and relative bundle paths; they do not read or write server paths. The API limits requests to 16 MiB, rejects traversal and duplicate bundle paths, and refuses CLI-only file/output flags over HTTP. Existing CLI file flags remain available.

Shared integration changes live in Clicky's entity command generation and Clicky UI's optional `renderConnectionActions` slot. Published consumers require releases containing those changes; local development uses the existing sibling checkout wiring. No local dependency override is needed for publication.
