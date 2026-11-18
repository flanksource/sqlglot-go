# Contributing and verification

## Verify the Go implementation

From the repository root:

```sh
make test
make lint
make build
```

The tests compare token streams, expression trees, and generated SQL with committed expectations from the pinned Python sqlglot reference. The tests do not need Python to read those expectations. `make lint` also requires golangci-lint v2.13.1.

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

The check validates the TypeScript configuration. The production build checks internal links and writes the static site to `doc_build/`. Preview serves that output locally. CI runs the same frozen install, configuration check, and build.

The default deployment base is `/`. For a host that serves the site under a subpath, set Rspress's `base` in `rspress.config.ts` to that path before building. Serve `doc_build/` with a static host; deployment is separate from the build.
