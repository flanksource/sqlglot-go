//go:build integration

package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing/fstest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Characterization of the unreviewed PostgreSQL bundle apply: a column change
// that PostgreSQL refuses while a view depends on it. Every view that reads the
// column, owned by a post script or not, is dropped and re-created from its live
// definition around the change (the script's recorded hash is left alone, so it
// does not re-run); a pure column addition touches neither.
// SQLGLOT_TEST_POSTGRES_URL names a disposable server, as for the reviewed
// PostgreSQL specs.

const characterizedItemsHCL = `schema "public" {}

table "items" {
  schema = schema.public
  column "id" {
    type = integer
  }
  column "label" {
    type = LABEL_TYPE
  }
  EXTRA_COLUMNS
  primary_key {
    columns = [column.id]
  }
}
`

const (
	characterizedOwnedView = "CREATE OR REPLACE VIEW public.item_labels AS SELECT id, label FROM public.items;\n"
	characterizedAdhocView = "CREATE VIEW public.adhoc_items AS SELECT label FROM public.items"
)

func characterizedItemsBundle(labelType, extraColumns string) fstest.MapFS {
	hcl := strings.NewReplacer("LABEL_TYPE", labelType, "EXTRA_COLUMNS", extraColumns).Replace(characterizedItemsHCL)
	return fstest.MapFS{
		"db/schema.hcl":      bundleFile(hcl),
		"db/item_labels.sql": bundleFile(characterizedOwnedView),
	}
}

var _ = Describe("PostgreSQL bundle apply characterization", Ordered, Label("integration"), func() {
	var (
		dsn string
		db  *sql.DB
	)
	apply := func(ctx context.Context, bundle fstest.MapFS) {
		Expect(Apply(ctx, dsn, bundle, WithDir("db"), WithName("acme/characterization"))).To(Succeed())
	}
	scalar := func(ctx context.Context, query string) string {
		var value string
		Expect(db.QueryRowContext(ctx, query).Scan(&value)).To(Succeed())
		return value
	}
	viewOID := func(ctx context.Context, name string) string {
		return scalar(ctx, fmt.Sprintf("SELECT oid::text FROM pg_class WHERE relname = '%s' AND relkind = 'v'", name))
	}

	BeforeAll(func(ctx SpecContext) { dsn, db = newPostgresDatabase(ctx) })

	It("creates the table and the view its post script owns", func(ctx SpecContext) {
		apply(ctx, characterizedItemsBundle("text", ""))
		_, err := db.ExecContext(ctx, "INSERT INTO items (id, label) VALUES (1, 'first'); "+characterizedAdhocView)
		Expect(err).NotTo(HaveOccurred())
		Expect(scalar(ctx, "SELECT count(*) FROM item_labels")).To(Equal("1"))
		Expect(scalar(ctx, "SELECT count(*) FROM schema_migration_scripts")).To(Equal("1"))
	})

	It("leaves both views untouched when only a column is added", func(ctx SpecContext) {
		owned, adhoc := viewOID(ctx, "item_labels"), viewOID(ctx, "adhoc_items")
		apply(ctx, characterizedItemsBundle("text", `column "note" {
    type = text
    null = true
  }`))
		Expect(scalar(ctx, "SELECT count(*) FROM information_schema.columns WHERE table_name = 'items' AND column_name = 'note'")).To(Equal("1"))
		Expect([]string{viewOID(ctx, "item_labels"), viewOID(ctx, "adhoc_items")}).To(Equal([]string{owned, adhoc}))
	})

	It("retypes a column under both views: rebuilds both from their live definitions and leaves the script's record alone", func(ctx SpecContext) {
		owned := viewOID(ctx, "item_labels")
		apply(ctx, characterizedItemsBundle("varchar(100)", `column "note" {
    type = text
    null = true
  }`))
		Expect(scalar(ctx, "SELECT data_type || ':' || character_maximum_length FROM information_schema.columns WHERE table_name = 'items' AND column_name = 'label'")).To(Equal("character varying:100"))
		Expect(viewOID(ctx, "item_labels")).NotTo(Equal(owned), "the owned view was dropped and re-created around the retype")
		Expect(scalar(ctx, "SELECT label FROM item_labels")).To(Equal("first"))
		Expect(scalar(ctx, "SELECT label FROM adhoc_items")).To(Equal("first"))
		Expect(scalar(ctx, "SELECT count(*) FROM schema_migration_scripts")).To(Equal("1"))
	})

	It("re-applies the same bundle without rebuilding any view", func(ctx SpecContext) {
		owned, adhoc := viewOID(ctx, "item_labels"), viewOID(ctx, "adhoc_items")
		apply(ctx, characterizedItemsBundle("varchar(100)", `column "note" {
    type = text
    null = true
  }`))
		Expect([]string{viewOID(ctx, "item_labels"), viewOID(ctx, "adhoc_items")}).To(Equal([]string{owned, adhoc}))
	})
})
