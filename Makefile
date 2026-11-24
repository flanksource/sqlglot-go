# sqlglot-go — a Go port of sqlglot, verified against the Python reference.
#
#   make doctor     # toolchain
#   make build      # build the CLI with the web UI embedded (node + pnpm)
#   make ui         # rebuild the web UI into www/dist
#   make install    # install the CLI, with the web UI, using Go's GOBIN
#   make test       # unit tests + the differential run against the reference
#   make oracle     # regenerate testdata/expected from the pinned sqlglot commit
#   make coverage   # print per-dialect coverage against the reference
#   make lint       # vet + golangci-lint
#
# These targets are convenience, and they assume GNU make and a POSIX shell:
# Linux, macOS, or WSL / Git Bash on Windows. They are NOT the build.
#
# The differential tests use committed expectations and run with the Go
# toolchain. CI runs them with Gavel on Ubuntu. See "Working on Windows"
# in the README for the native Go commands.
SQLGLOT ?= $(HOME)/opensource/sqlglot
SERVICE ?= $(HOME)/calvinchengx/emulators/data-agent-service
GOLANGCI ?= golangci-lint
BINARY ?= .tmp/sqlglot
# Windows spells it `python`; POSIX distributions increasingly only ship
# `python3`. Override rather than edit.
PYTHON ?= python3
# Where the Go side hands the execution oracle its pairs; see harness/execute.py.
EXEC_PAIRS ?= $(shell echo $${TMPDIR:-/tmp})/sqlglot-go-exec-pairs.jsonl
# The oracle reaches PostgreSQL through $PGDSN; `make postgres` prints one.
# Override PGPORT if 5432 or this one is taken -- a developer machine running
# other stacks is the normal case, and a wedged bind is not worth debugging.
PGPORT ?= 55433

.PHONY: help doctor build ui install test oracle oracle-exec postgres postgres-stop service coverage cover gaps lint clean
.PHONY: docs-dev docs-check docs-build docs-preview

help: ## Show the available targets
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | sort | awk 'BEGIN{FS=":.*?## "}{printf "  %-12s %s\n", $$1, $$2}'

docs-dev: ## Start the Rspress documentation site (install docs dependencies first)
	pnpm docs:dev

docs-check: ## Type-check the documentation configuration
	pnpm docs:check

docs-build: ## Build the static documentation site into doc_build
	pnpm docs:build

docs-preview: ## Preview the built documentation site
	pnpm docs:preview

doctor: ## Check the toolchain
	@go version
	@$(PYTHON) --version || echo "$(PYTHON) NOT found (only needed for make oracle and make service)"
	@test -d $(SQLGLOT) && echo "reference: $(SQLGLOT) @ $$(git -C $(SQLGLOT) rev-parse --short HEAD)" || echo "reference NOT found at $(SQLGLOT) (only needed for make oracle)"
	@$(PYTHON) -c 'import duckdb; print("duckdb:", duckdb.__version__)' 2>/dev/null || echo "duckdb NOT found (only needed for make oracle-exec)"

# The CLI embeds www/dist behind the ui build tag, so plain `go build` and
# `go test ./...` never need node; these targets always embed it.
UI_SOURCES := $(shell find www/src www/mock -type f) $(wildcard www/index.html www/package.json www/pnpm-lock.yaml www/vite.config.ts www/vite.clicky-ui.ts www/tsconfig*.json)

build: www/dist/index.html ## Build the sqlglot CLI, with the web UI, into .tmp/
	@mkdir -p .tmp
	go build -tags ui -o $(BINARY) ./cmd/sqlglot

www/dist/index.html: $(UI_SOURCES)
	pnpm -C www install --frozen-lockfile
	pnpm -C www build

ui: ## Rebuild the web UI into www/dist (needs node and pnpm)
	@rm -f www/dist/index.html
	@$(MAKE) www/dist/index.html

.PHONY: build-proxy-fixtures
build-proxy-fixtures: build ## Build the proxy fixture client
	go build -o .tmp/proxy-fixture-client ./proxy/testdata/client

.PHONY: tds-fixtures
tds-fixtures: ## Recapture proxy/tds fixtures through the CLI proxy; needs TDS_CONNECTION and TDS_UPSTREAM
	@mkdir -p .tmp
	go build -o .tmp/sqlglot-proxy ./cmd/sqlglot
	go build -o .tmp/proxy-fixture-client ./proxy/testdata/client
	.tmp/proxy-fixture-client --mode tds-fixtures --sqlglot .tmp/sqlglot-proxy --connection "$(TDS_CONNECTION)" --upstream "$(TDS_UPSTREAM)" --dir proxy/tds/testdata/captures

build-database-tests: ## Build the disposable database integration suite
	@mkdir -p .tmp
	go test -c -tags integration -o .tmp/database-review.test ./database/migrate/review
	go test -c -tags integration -o .tmp/database-sqlserver.test ./database/migrate/sqlserver
	go test -c -o .tmp/database-bundle.test ./database/migrate

install: www/dist/index.html ## Install the sqlglot CLI, with the web UI, to GOBIN (or GOPATH/bin)
	go install -tags ui ./cmd/sqlglot

test: ## Unit tests and the differential run against the reference
	go test ./...

oracle: ## Regenerate expectations and generated tables from the PINNED reference (refuses any other)
	$(PYTHON) harness/oracle.py --sqlglot $(SQLGLOT) --out testdata/expected
	$(PYTHON) harness/gen_classes.py --sqlglot $(SQLGLOT) > sqlglot/classes_gen.go && gofmt -w sqlglot/classes_gen.go
	$(PYTHON) harness/gen_tokenizer.py --sqlglot $(SQLGLOT) --out sqlglot && gofmt -w sqlglot/tokentype_gen.go sqlglot/dialects_gen.go
	$(PYTHON) harness/gen_parser.py --sqlglot $(SQLGLOT) && gofmt -w sqlglot/parser_gen.go
	$(PYTHON) harness/gen_simplify.py --sqlglot $(SQLGLOT)
	$(PYTHON) harness/gen_annotate.py --sqlglot $(SQLGLOT)

oracle-exec: ## Run the port's SQL through an engine and check it MEANS the same
	@DAS_EXEC_EMIT=$(EXEC_PAIRS) go test ./harness/ -run TestEmitExecutionPairs
	@$(PYTHON) harness/execute.py --pairs $(EXEC_PAIRS)

postgres: ## Start a PostgreSQL for `make oracle-exec` and print the DSN to export
	@docker rm -f sqlglot-go-pg >/dev/null 2>&1 || true
	@docker run -d --name sqlglot-go-pg -e POSTGRES_HOST_AUTH_METHOD=trust \
		-p $(PGPORT):5432 postgres:17-alpine >/dev/null || { \
		echo "could not bind port $(PGPORT); pick another: make postgres PGPORT=5544x"; \
		docker rm -f sqlglot-go-pg >/dev/null 2>&1; exit 1; }
	@until docker exec sqlglot-go-pg pg_isready -q 2>/dev/null; do sleep 1; done
	@echo 'export PGDSN="host=127.0.0.1 port=$(PGPORT) user=postgres dbname=postgres"'

postgres-stop: ## Stop it again
	@docker rm -f sqlglot-go-pg >/dev/null 2>&1 || true

service: ## Re-extract the corpus of SQL data agent service is held to
	$(PYTHON) harness/gen_service_corpus.py --service $(SERVICE) --sqlglot $(SQLGLOT)

fuzz: ## Fuzz the generator and let the reference judge what it finds (SECONDS=45)
	@rm -f /tmp/sqlglot-go-candidates.txt
	@DAS_FUZZ_COLLECT=/tmp/sqlglot-go-candidates.txt \
		go test ./sqlglot/ -run=XXX -fuzz=FuzzGeneratedSQLCanBeReadBack \
		-fuzztime=$${SECONDS:-45}s
	@if [ -s /tmp/sqlglot-go-candidates.txt ]; then \
		sort -u /tmp/sqlglot-go-candidates.txt -o /tmp/sqlglot-go-candidates.txt; \
		$(PYTHON) harness/adjudicate.py --sqlglot $(SQLGLOT) \
			--candidates /tmp/sqlglot-go-candidates.txt; \
	else \
		echo "the generator wrote nothing the parser could not read back"; \
	fi

fuzz-differential: ## Fuzz for statements the port parses, and diff every tree against the reference
	@rm -f /tmp/sqlglot-go-parsed.txt
	@DAS_FUZZ_COLLECT=/tmp/sqlglot-go-parsed.txt \
		go test ./sqlglot/ -run=XXX -fuzz=FuzzParsedStatementsForTheDifferential \
		-fuzztime=$${SECONDS:-30}s
	@sort -u /tmp/sqlglot-go-parsed.txt -o /tmp/sqlglot-go-parsed.txt
	@$(PYTHON) harness/oracle.py --sqlglot $(SQLGLOT) \
		--candidates /tmp/sqlglot-go-parsed.txt --out /tmp/sqlglot-go-expected
	@SQLGLOT_GO_EXPECTED=/tmp/sqlglot-go-expected \
		go test ./harness/ -run TestAgainstReference

gaps: ## Why the port refuses what it refuses, most common first
	@go test ./harness/ -run TestGapReport -v 2>&1 | sed -n 's/^ *gaps_test.go:[0-9]*: //p'

readme: test ## Rewrite the README's tables from what the suite just measured
	$(PYTHON) harness/gen_readme.py

coverage: test ## Per-dialect coverage, against the reference and against the service
	@$(PYTHON) -c 'import json; c=json.load(open("testdata/service/coverage.json")); print("data agent service:"); [print("  %-24s %3d/%d" % (k, v["parsed"], v["total"])) for k, v in sorted(c["by_category"].items())]'
	@$(PYTHON) -c 'import json; c=json.load(open("testdata/coverage.json")); print("reference %s  %d/%d" % (c["reference"][:12], c["matched"], c["total"])); [print("  %-10s %4d/%-4d unparsed %4d mismatched %d" % (d, v["matched"], v["total"], v["unparsed"], v["mismatched"])) for d, v in sorted(c["by_dialect"].items())]'

lint: ## gofmt, vet and native golangci-lint (v2.15.0)
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
	  echo "not gofmt'd:"; echo "$$unformatted"; \
	  echo "CI regenerates and diffs, so it fails there instead; run gofmt -w"; \
	  exit 1; \
	fi
	go vet ./...
	$(GOLANGCI) run ./...

cover: ## Test coverage of the port
	@go test ./... -coverpkg=./sqlglot/ -coverprofile=cover.out >/dev/null
	@go tool cover -func=cover.out | grep -v " 100.0%$$" || echo "  every statement covered"

clean: ## Remove generated coverage
	rm -f cover.out testdata/coverage.json testdata/token_coverage.json testdata/service/coverage.json
