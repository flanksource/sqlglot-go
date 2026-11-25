package migrate

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing/fstest"

	"github.com/flanksource/sqlglot-go/database/target"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Characterization of the unreviewed SQLite bundle apply (Apply with an HCL
// source): what the database holds after each apply, as a later refactor onto a
// shared planner must reproduce it.

type sqliteObject struct{ Type, Name, Table string }

func schemaObjects(ctx context.Context, database *sql.DB) []sqliteObject {
	rows, err := database.QueryContext(ctx, `SELECT type, name, tbl_name FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	Expect(err).NotTo(HaveOccurred())
	defer func() { Expect(rows.Close()).To(Succeed()) }()
	var objects []sqliteObject
	for rows.Next() {
		var o sqliteObject
		Expect(rows.Scan(&o.Type, &o.Name, &o.Table)).To(Succeed())
		objects = append(objects, o)
	}
	Expect(rows.Err()).NotTo(HaveOccurred())
	return objects
}

func schemaSQL(ctx context.Context, database *sql.DB) map[string]string {
	rows, err := database.QueryContext(ctx, `SELECT name, sql FROM sqlite_schema WHERE sql IS NOT NULL ORDER BY name`)
	Expect(err).NotTo(HaveOccurred())
	defer func() { Expect(rows.Close()).To(Succeed()) }()
	statements := map[string]string{}
	for rows.Next() {
		var name, statement string
		Expect(rows.Scan(&name, &statement)).To(Succeed())
		statements[name] = statement
	}
	Expect(rows.Err()).NotTo(HaveOccurred())
	return statements
}

func columnNames(ctx context.Context, database *sql.DB, table string) []string {
	rows, err := database.QueryContext(ctx, `SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	Expect(err).NotTo(HaveOccurred())
	defer func() { Expect(rows.Close()).To(Succeed()) }()
	var names []string
	for rows.Next() {
		var name string
		Expect(rows.Scan(&name)).To(Succeed())
		names = append(names, name)
	}
	Expect(rows.Err()).NotTo(HaveOccurred())
	return names
}

func openCharacterizedSQLite(ctx context.Context, dsn string) *sql.DB {
	parsed, err := dbtarget.Parse(dsn)
	Expect(err).NotTo(HaveOccurred())
	database, err := sql.Open("sqlite", parsed.DSN)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(database.Close)
	Expect(database.PingContext(ctx)).To(Succeed())
	return database
}

var _ = Describe("SQLite bundle apply characterization", func() {
	var (
		dsn   string
		files fstest.MapFS
	)
	BeforeEach(func() {
		dsn = filepath.Join(GinkgoT().TempDir(), "characterization.db")
		files = fstest.MapFS{"schema.hcl": &fstest.MapFile{Data: []byte(portableSchema)}}
	})

	It("creates every declared table and index, and a second apply leaves the schema byte for byte unchanged", func(ctx context.Context) {
		Expect(Apply(ctx, dsn, files)).To(Succeed())
		database := openCharacterizedSQLite(ctx, dsn)
		Expect(schemaObjects(ctx, database)).To(Equal([]sqliteObject{
			{"index", "events_active_key", "events"},
			{"index", "projects_external_key_key", "projects"},
			{"table", "events", "events"},
			{"table", "projects", "projects"},
		}))
		first := schemaSQL(ctx, database)
		Expect(Apply(ctx, dsn, files)).To(Succeed())
		Expect(schemaSQL(ctx, database)).To(Equal(first))
	})

	It("adds a declared column in place, keeping existing rows and column order", func(ctx context.Context) {
		Expect(Apply(ctx, dsn, files)).To(Succeed())
		database := openCharacterizedSQLite(ctx, dsn)
		_, err := database.ExecContext(ctx, `INSERT INTO projects (id, external_key, payload, created_at) VALUES ('p1', 'kept', '{}', CURRENT_TIMESTAMP)`)
		Expect(err).NotTo(HaveOccurred())
		files["schema.hcl"] = &fstest.MapFile{Data: []byte(strings.Replace(portableSchema, `  column "created_at" {`, "  column \"description\" {\n    type = text\n    null = true\n  }\n  column \"created_at\" {", 1))}
		Expect(Apply(ctx, dsn, files)).To(Succeed())

		Expect(columnNames(ctx, database, "projects")).To(Equal([]string{"id", "external_key", "payload", "created_at", "description"}))
		var key string
		var description sql.NullString
		Expect(database.QueryRowContext(ctx, `SELECT external_key, description FROM projects WHERE id = 'p1'`).Scan(&key, &description)).To(Succeed())
		Expect(key).To(Equal("kept"))
		Expect(description.Valid).To(BeFalse())
	})

	It("rebuilds a table for a changed default, keeping rows, indexes and foreign keys, then settles", func(ctx context.Context) {
		Expect(Apply(ctx, dsn, files)).To(Succeed())
		database := openCharacterizedSQLite(ctx, dsn)
		for _, statement := range []string{
			`INSERT INTO projects (id, external_key, payload, created_at) VALUES ('p1', 'kept', '{}', CURRENT_TIMESTAMP)`,
			`INSERT INTO events (id, project_id, event_key, active) VALUES ('e1', 'p1', 'build', true)`,
		} {
			_, err := database.ExecContext(ctx, statement)
			Expect(err).NotTo(HaveOccurred())
		}
		before := schemaObjects(ctx, database)
		files["schema.hcl"] = &fstest.MapFile{Data: []byte(strings.Replace(portableSchema, `default = sql("'{}'")`, `default = sql("'[]'")`, 1))}
		Expect(Apply(ctx, dsn, files)).To(Succeed())

		Expect(schemaObjects(ctx, database)).To(Equal(before))
		var payload string
		Expect(database.QueryRowContext(ctx, `SELECT payload FROM projects WHERE id = 'p1'`).Scan(&payload)).To(Succeed())
		Expect(payload).To(Equal("{}"))
		_, err := database.ExecContext(ctx, `INSERT INTO projects (id, external_key, created_at) VALUES ('p2', 'defaulted', CURRENT_TIMESTAMP)`)
		Expect(err).NotTo(HaveOccurred())
		Expect(database.QueryRowContext(ctx, `SELECT payload FROM projects WHERE id = 'p2'`).Scan(&payload)).To(Succeed())
		Expect(payload).To(Equal("[]"))
		var events int
		Expect(database.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE project_id = 'p1'`).Scan(&events)).To(Succeed())
		Expect(events).To(Equal(1))
		_, err = database.ExecContext(ctx, `INSERT INTO events (id, project_id, event_key, active) VALUES ('e2', 'missing', 'build', false)`)
		Expect(err).To(MatchError(ContainSubstring("FOREIGN KEY constraint failed")))

		settled := schemaSQL(ctx, database)
		Expect(Apply(ctx, dsn, files)).To(Succeed())
		Expect(schemaSQL(ctx, database)).To(Equal(settled))
	})

	It("keeps an undeclared table and an undeclared column, with their data, through a rebuild", func(ctx context.Context) {
		Expect(Apply(ctx, dsn, files)).To(Succeed())
		database := openCharacterizedSQLite(ctx, dsn)
		for _, statement := range []string{
			`ALTER TABLE projects ADD COLUMN notes TEXT`,
			`INSERT INTO projects (id, external_key, payload, created_at, notes) VALUES ('p1', 'kept', '{}', CURRENT_TIMESTAMP, 'dba note')`,
			`CREATE TABLE scratch (value TEXT)`,
			`INSERT INTO scratch VALUES ('dba table')`,
		} {
			_, err := database.ExecContext(ctx, statement)
			Expect(err).NotTo(HaveOccurred())
		}
		files["schema.hcl"] = &fstest.MapFile{Data: []byte(strings.Replace(portableSchema, `default = sql("'{}'")`, `default = sql("'[]'")`, 1))}
		Expect(Apply(ctx, dsn, files)).To(Succeed())

		Expect(columnNames(ctx, database, "projects")).To(ContainElement("notes"))
		var note, scratch string
		Expect(database.QueryRowContext(ctx, `SELECT notes FROM projects WHERE id = 'p1'`).Scan(&note)).To(Succeed())
		Expect(database.QueryRowContext(ctx, `SELECT value FROM scratch`).Scan(&scratch)).To(Succeed())
		Expect([]string{note, scratch}).To(Equal([]string{"dba note", "dba table"}))
	})
})
