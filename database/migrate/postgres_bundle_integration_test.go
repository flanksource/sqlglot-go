//go:build integration

package migrate

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing/fstest"
	"time"

	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/zclconf/go-cty/cty"
)

// migrationAdvisoryLock is the lock every PostgreSQL migration unit holds; a
// spec holds it from another session to force lock contention.
const migrationAdvisoryLock = 7561364747649

func hclBundle(hcl string, extra fstest.MapFS) fstest.MapFS {
	bundle := fstest.MapFS{"db/schema.hcl": bundleFile(hcl)}
	for name, f := range extra {
		bundle[name] = f
	}
	return bundle
}

func jobsHCL(values, extraColumns string) string {
	return strings.NewReplacer("VALUES", values, "EXTRA", extraColumns).Replace(`schema "public" {}
enum "state" {
  schema = schema.public
  values = [VALUES]
}
table "jobs" {
  schema = schema.public
  column "id" { type = integer }
  column "state" { type = enum.state }
  EXTRA
  primary_key { columns = [column.id] }
}
`)
}

var _ = Describe("PostgreSQL bundle planned through the engine", Label("integration"), func() {
	var (
		dsn string
		db  *sql.DB
	)
	BeforeEach(func(ctx SpecContext) { dsn, db = newPostgresDatabase(ctx) })

	apply := func(ctx context.Context, bundle fstest.MapFS, opts ...Option) error {
		return Apply(ctx, dsn, bundle, append([]Option{WithDir("db"), WithName("acme/planner")}, opts...)...)
	}
	pending := func(ctx context.Context, bundle fstest.MapFS, opts ...Option) []engine.Unit {
		preview, err := PreviewBundle(ctx, db, "postgres", bundle, append([]Option{WithDir("db"), WithName("acme/planner")}, opts...)...)
		Expect(err).NotTo(HaveOccurred())
		return preview.Artifact.Units
	}

	It("adds an enum value, then a column typed by it, keeping rows, and settles", func(ctx SpecContext) {
		Expect(apply(ctx, hclBundle(jobsHCL(`"new", "done"`, ""), nil))).To(Succeed())
		mustExecAll(ctx, db, "INSERT INTO jobs VALUES (1, 'done')")

		grown := hclBundle(jobsHCL(`"new", "in_progress", "done"`, "column \"previous\" {\n    type = enum.state\n    null = true\n  }"), nil)
		Expect(apply(ctx, grown)).To(Succeed())

		Expect(scalarOf(ctx, db, "SELECT array_to_string(enum_range(NULL::state), ',')")).To(Equal("new,in_progress,done"))
		Expect(scalarOf(ctx, db, "SELECT state::text FROM jobs WHERE id = 1")).To(Equal("done"))
		mustExecAll(ctx, db, "INSERT INTO jobs VALUES (2, 'in_progress', 'new')")
		Expect(pending(ctx, grown)).To(BeEmpty())
	})

	It("re-runs a view script that changed in the same apply as the retype it sits under", func(ctx SpecContext) {
		Expect(apply(ctx, hclBundle(strings.NewReplacer("LABEL_TYPE", "text", "EXTRA_COLUMNS", "").Replace(characterizedItemsHCL),
			fstest.MapFS{"db/item_labels.sql": bundleFile(characterizedOwnedView)}))).To(Succeed())
		mustExecAll(ctx, db, "INSERT INTO items (id, label) VALUES (1, 'first')")
		before := scalarOf(ctx, db, "SELECT oid::text FROM pg_class WHERE relname = 'item_labels'")

		edited := "CREATE OR REPLACE VIEW public.item_labels AS SELECT id, label, id AS again FROM public.items;\n"
		retyped := strings.NewReplacer("LABEL_TYPE", "varchar(100)", "EXTRA_COLUMNS", "").Replace(characterizedItemsHCL)
		Expect(apply(ctx, hclBundle(retyped, fstest.MapFS{"db/item_labels.sql": bundleFile(edited)}))).To(Succeed())

		Expect(scalarOf(ctx, db, "SELECT oid::text FROM pg_class WHERE relname = 'item_labels'")).NotTo(Equal(before))
		Expect(scalarOf(ctx, db, "SELECT again::text FROM item_labels")).To(Equal("1"), "the edited script ran after the rebuild")
		Expect(scalarOf(ctx, db, "SELECT count(*) FROM schema_migration_scripts")).To(Equal("1"))
	})

	It("previews a retype with the views it forces to be rebuilt, grouped under their cause", func(ctx SpecContext) {
		mustExecAll(ctx, db,
			"CREATE TABLE items (id integer NOT NULL, label text NOT NULL, PRIMARY KEY (id))",
			"CREATE VIEW item_labels AS SELECT id, label FROM items",
			"CREATE VIEW adhoc_items AS SELECT label FROM item_labels")
		retyped := hclBundle(strings.NewReplacer("LABEL_TYPE", "varchar(100)", "EXTRA_COLUMNS", "").Replace(characterizedItemsHCL), nil)

		preview, err := PreviewBundle(ctx, db, "postgres", retyped, WithDir("db"))
		Expect(err).NotTo(HaveOccurred())
		var rebuilt []string
		for _, c := range preview.Artifact.Changes {
			if c.Action == "rebuild" {
				rebuilt = append(rebuilt, c.Object.Name+" <- "+c.Cause)
			}
		}
		cause := changeIDOfSchema("modify", "column", "items", "label")
		Expect(rebuilt).To(ConsistOf("adhoc_items <- "+cause, "item_labels <- "+cause))
		Expect(preview.Artifact.Dependents).To(HaveLen(2))
		Expect(preview.Advisory).To(BeEmpty())
		Expect(engine.Validate(*preview.Artifact)).To(Succeed())
	})

	It("retries a unit that hits lock contention and applies once the lock is free", func(ctx SpecContext) {
		previous := lockTimeout
		lockTimeout = 300 * time.Millisecond
		DeferCleanup(func() { lockTimeout = previous })
		holder, err := db.Conn(ctx)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(holder.Close)
		_, err = holder.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationAdvisoryLock)
		Expect(err).NotTo(HaveOccurred())
		var released sync.WaitGroup
		released.Add(1)
		go func() {
			defer GinkgoRecover()
			defer released.Done()
			time.Sleep(1200 * time.Millisecond)
			_, e := holder.ExecContext(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationAdvisoryLock)
			Expect(e).NotTo(HaveOccurred())
		}()

		started := time.Now()
		Expect(apply(ctx, hclBundle(jobsHCL(`"new", "done"`, ""), nil))).To(Succeed())
		released.Wait()
		Expect(time.Since(started)).To(BeNumerically(">=", 1200*time.Millisecond), "the apply waited for the lock through retries")
		Expect(scalarOf(ctx, db, "SELECT to_regclass('jobs')::text")).To(Equal("jobs"))
	})

	It("settles a default and a check the database spells differently, so a second apply plans nothing", func(ctx SpecContext) {
		const hcl = `schema "public" {}
table "orders" {
  schema = schema.public
  column "id" { type = integer }
  column "qty" {
    type = integer
    default = sql("1+1")
  }
  column "status" { type = text }
  check "orders_status_known" { expr = "status IN ('open', 'shipped')" }
  primary_key { columns = [column.id] }
}
`
		bundle := hclBundle(hcl, nil)
		Expect(apply(ctx, bundle)).To(Succeed())
		checkOID := func() string {
			return scalarOf(ctx, db, "SELECT oid::text FROM pg_constraint WHERE conname = 'orders_status_known'")
		}
		created := checkOID()

		Expect(pending(ctx, bundle)).To(BeEmpty())
		Expect(apply(ctx, bundle)).To(Succeed())
		Expect(checkOID()).To(Equal(created), "the equivalent check was not dropped and re-added")

		widened := hclBundle(strings.Replace(hcl, `'shipped')`, `'shipped', 'lost')`, 1), nil)
		Expect(apply(ctx, widened)).To(Succeed())
		Expect(checkOID()).NotTo(Equal(created))
		mustExecAll(ctx, db, "INSERT INTO orders (id, status) VALUES (1, 'lost')")
		Expect(pending(ctx, widened)).To(BeEmpty())
	})

	It("moves the portable public schema to the target schema, with its metadata tables", func(ctx SpecContext) {
		bundle := hclBundle(jobsHCL(`"new", "done"`, ""), fstest.MapFS{"db/seed.sql": bundleFile("-- phase: post\nSELECT 1")})
		Expect(apply(ctx, bundle, WithSchema("tenant_a"))).To(Succeed())
		Expect(scalarOf(ctx, db, "SELECT coalesce(to_regclass('tenant_a.jobs')::text, '') || '|' || coalesce(to_regclass('public.jobs')::text, '')")).To(Equal("tenant_a.jobs|"))
		Expect(scalarOf(ctx, db, "SELECT to_regclass('tenant_a.schema_migration_scripts')::text")).To(Equal("tenant_a.schema_migration_scripts"))
		Expect(pending(ctx, bundle, WithSchema("tenant_a"))).To(BeEmpty())
	})

	It("runs on the caller's handle for the default schema and leaves it open", func(ctx SpecContext) {
		bundle := hclBundle(jobsHCL(`"new", "done"`, ""), nil)
		Expect(Apply(ctx, "", bundle, WithDir("db"), WithName("acme/planner"), WithDB(db))).To(Succeed())
		Expect(Apply(ctx, "", bundle, WithDir("db"), WithName("acme/planner"), WithDB(db))).To(Succeed())
		Expect(scalarOf(ctx, db, "SELECT to_regclass('jobs')::text")).To(Equal("jobs"))
		Expect(db.PingContext(ctx)).To(Succeed())
	})

	It("keeps an undeclared table and type in the declared scope, runs the drop of an undeclared index but reports an undeclared column, drops it with WithDrops, and never drops its own metadata", func(ctx SpecContext) {
		bundle := hclBundle(jobsHCL(`"new", "done"`, ""), fstest.MapFS{"db/seed.sql": bundleFile("SELECT 1")})
		Expect(apply(ctx, bundle)).To(Succeed())
		mustExecAll(ctx, db,
			"ALTER TABLE jobs ADD COLUMN notes text", "CREATE INDEX jobs_notes ON jobs (notes)", "CREATE INDEX jobs_state ON jobs (state)",
			"CREATE TABLE other (value text)", "CREATE TYPE mood AS ENUM ('ok')")

		preview, err := PreviewBundle(ctx, db, "postgres", bundle, WithDir("db"), WithName("acme/planner"))
		Expect(err).NotTo(HaveOccurred())
		Expect(preview.Warnings).To(ContainElement(`column "notes" of table public.jobs is in the database but not declared, and was not dropped: declare the column, mark the table ` + "`partial = true`" + `, or apply with WithDrops`))
		Expect(preview.Artifact.Units).NotTo(BeEmpty(), "the index drops are still planned")

		Expect(apply(ctx, bundle)).To(Succeed())
		Expect(scalarOf(ctx, db, "SELECT count(*) FROM information_schema.columns WHERE table_name = 'jobs' AND column_name = 'notes'")).To(Equal("1"))
		Expect(scalarOf(ctx, db, "SELECT count(*) FROM pg_indexes WHERE indexname = 'jobs_state'")).To(Equal("0"))
		Expect(scalarOf(ctx, db, "SELECT to_regclass('other')::text")).To(Equal("other"))
		Expect(scalarOf(ctx, db, "SELECT count(*) FROM pg_type WHERE typname = 'mood'")).To(Equal("1"))

		again, err := PreviewBundle(ctx, db, "postgres", bundle, WithDir("db"), WithName("acme/planner"))
		Expect(err).NotTo(HaveOccurred())
		Expect(again.Warnings).To(ContainElement(ContainSubstring(`column "notes" of table public.jobs`)), "the column is reported every time")
		Expect(again.Artifact.Units).To(BeEmpty())

		Expect(apply(ctx, bundle, WithDrops())).To(Succeed())
		Expect(scalarOf(ctx, db, "SELECT count(*) FROM information_schema.columns WHERE table_name = 'jobs' AND column_name = 'notes'")).To(Equal("0"))
		Expect(scalarOf(ctx, db, "SELECT coalesce(to_regclass('other')::text, '')")).To(BeEmpty())
		Expect(scalarOf(ctx, db, "SELECT count(*) FROM pg_type WHERE typname = 'mood'")).To(Equal("0"))
		Expect(scalarOf(ctx, db, "SELECT to_regclass('schema_migration_scripts')::text")).To(Equal("schema_migration_scripts"))
		Expect(apply(ctx, bundle, WithDrops())).To(Succeed())
	})

	It("keeps every member a partial table does not name, with nothing reported, even under WithDrops", func(ctx SpecContext) {
		partial := hclBundle(strings.Replace(jobsHCL(`"new", "done"`, ""), "table \"jobs\" {\n  schema = schema.public\n", "table \"jobs\" {\n  schema = schema.public\n  partial = true\n", 1), nil)
		Expect(apply(ctx, hclBundle(jobsHCL(`"new", "done"`, ""), nil))).To(Succeed())
		mustExecAll(ctx, db, "ALTER TABLE jobs ADD COLUMN notes text", "CREATE INDEX jobs_notes ON jobs (notes)", "CREATE INDEX jobs_state ON jobs (state)")

		preview, err := PreviewBundle(ctx, db, "postgres", partial, WithDir("db"), WithName("acme/planner"))
		Expect(err).NotTo(HaveOccurred())
		Expect(preview.Warnings).To(BeEmpty())
		Expect(preview.Artifact.Units).To(BeEmpty())

		Expect(apply(ctx, partial)).To(Succeed())
		Expect(apply(ctx, partial, WithDrops())).To(Succeed())
		Expect(scalarOf(ctx, db, "SELECT count(*) FROM information_schema.columns WHERE table_name = 'jobs' AND column_name = 'notes'")).To(Equal("1"))
		Expect(scalarOf(ctx, db, "SELECT count(*) FROM pg_indexes WHERE indexname IN ('jobs_notes', 'jobs_state')")).To(Equal("2"))
	})

	It("refuses a partial table the database does not have", func(ctx SpecContext) {
		partial := hclBundle(strings.Replace(jobsHCL(`"new", "done"`, ""), "table \"jobs\" {\n  schema = schema.public\n", "table \"jobs\" {\n  schema = schema.public\n  partial = true\n", 1), nil)
		Expect(apply(ctx, partial)).To(MatchError(ContainSubstring("table public.jobs is declared partial and does not exist: a partial declaration cannot create a table")))
	})

	It("hides an excluded table from both sides, even under WithDrops", func(ctx SpecContext) {
		const hcl = `schema "public" {}
table "scratch" {
  schema = schema.public
  column "id" { type = integer }
}
table "kept" {
  schema = schema.public
  column "id" { type = integer }
}
`
		mustExecAll(ctx, db, "CREATE TABLE scratch (junk text)", "CREATE TABLE ledger (entry text)", "CREATE TABLE other (value text)")
		Expect(apply(ctx, hclBundle(hcl, nil), WithExclude("scratch", "ledger"), WithDrops())).To(Succeed())

		Expect(scalarOf(ctx, db, "SELECT string_agg(column_name, ',') FROM information_schema.columns WHERE table_name = 'scratch'")).To(Equal("junk"))
		Expect(scalarOf(ctx, db, "SELECT string_agg(table_name, ',' ORDER BY table_name) FROM information_schema.tables WHERE table_schema = 'public' AND table_name NOT LIKE 'schema_migration%'")).
			To(Equal("kept,ledger,scratch"))
	})

	It("evaluates HCL variables before planning", func(ctx SpecContext) {
		const hcl = `variable "state" { type = string }
schema "public" {}
table "docs" {
  schema = schema.public
  column "id" { type = integer }
  column "state" {
    type = text
    default = var.state
  }
}
`
		bundle := hclBundle(hcl, nil)
		draft := map[string]cty.Value{"state": cty.StringVal("draft")}
		Expect(apply(ctx, bundle, WithVariables(draft))).To(Succeed())
		mustExecAll(ctx, db, "INSERT INTO docs (id) VALUES (1)")
		Expect(scalarOf(ctx, db, "SELECT state FROM docs")).To(Equal("draft"))
		Expect(pending(ctx, bundle, WithVariables(draft))).To(BeEmpty())
		Expect(apply(ctx, bundle, WithVariables(map[string]cty.Value{"state": cty.StringVal("final")}))).To(Succeed())
		Expect(scalarOf(ctx, db, "SELECT column_default FROM information_schema.columns WHERE table_name = 'docs' AND column_name = 'state'")).To(ContainSubstring("final"))
	})
})
