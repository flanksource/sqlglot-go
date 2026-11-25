# Contributing and verification

## Verify the Go implementation

From the repository root:

```sh
make test
make lint
make build
```

The tests compare token streams, expression trees, and generated SQL with committed expectations from the pinned Python sqlglot reference. The tests do not need Python to read those expectations. `make lint` also requires golangci-lint v2.15.0.

`make oracle` regenerates expectations and generated tables and requires Python plus the reference checkout pinned in `NOTICE`. `make oracle-exec` compares actual query results using the Python execution harness and its database dependencies. Agreement with the reference alone does not prove query semantics; the execution oracle supplies a separate check.

See the [repository README](https://github.com/flanksource/sqlglot-go/blob/main/README.md) for coverage measurements, reference setup, and the verification model. Known reference divergences are recorded in [Upstream issues](../upstream-issues).

## Run the documentation site

The site uses [Rspress](https://www.rspress.dev/guide/start/getting-started), requires Node.js 22.12 or newer, and uses the pnpm package at the repository root. Install pnpm 12.15.0, the version pinned by `package.json`.

```sh
pnpm install --frozen-lockfile
make docs-dev
```

Open the URL printed by Rspress. Markdown edits in `docs/` appear automatically in the browser through live reload. Navigation lives in `rspress.config.ts` at the repository root. The existing `docs/upstream-issues.md` is served directly. Keep dependencies and generated output outside `docs/` so the development server watches only documentation content.

## Build and preview

```sh
make docs-check
make docs-build
make docs-preview
```

The check validates the TypeScript configuration. The production build checks internal links and writes the static site to `doc_build/`. Preview serves that output locally.

The default deployment base is `/`. Build for the GitHub Pages project path with `pnpm docs:build --base /sqlglot-go/` and publish the contents of `doc_build/` from the root of the `gh-pages` branch.

## Render the migration diagrams

The editable Facet templates are in `docs/diagrams/`. Regenerate the static figures with the [Facet CLI](https://github.com/flanksource/facet) from the repository root.

```sh
mkdir -p .tmp/migration-diagrams
facet png docs/diagrams/reviewed-lifecycle.tsx docs/diagrams/selected-changes.tsx \
  --selector '#migration-diagram' --viewport 800x480 --width 1488 \
  -o .tmp/migration-diagrams
cp .tmp/migration-diagrams/reviewed-lifecycle.png .tmp/migration-diagrams/selected-changes.png docs/public/images/migrations/
```
