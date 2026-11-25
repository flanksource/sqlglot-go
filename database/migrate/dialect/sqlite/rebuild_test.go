package sqlite_test

import (
	"database/sql"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const projectsHCL = `schema "main" {}
table "projects" {
  schema = schema.main
  column "id" {
    type = text
    null = false
  }
  column "external_key" {
    type = text
    null = false
  }
  column "payload" {
    type = text
    null = false
    default = sql("'{}'")
  }
  primary_key {
    columns = [column.id]
  }
  index "projects_external_key_key" {
    unique = true
    columns = [column.external_key]
  }
}
table "events" {
  schema = schema.main
  column "id" {
    type = text
    null = false
  }
  column "project_id" {
    type = text
    null = false
  }
  column "active" {
    type = boolean
    null = false
    default = false
  }
  primary_key {
    columns = [column.id]
  }
  foreign_key "events_project_id_fkey" {
    columns = [column.project_id]
    ref_columns = [table.projects.column.id]
    on_update = NO_ACTION
    on_delete = CASCADE
  }
  index "events_active_key" {
    unique = true
    columns = [column.project_id, column.id]
    where = "active"
  }
}`

type schemaObject struct{ Type, Name, Table string }

func schemaObjects(db *sql.DB) []schemaObject {
	rows, err := db.Query(`SELECT type, name, tbl_name FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	Expect(err).NotTo(HaveOccurred())
	defer func() { Expect(rows.Close()).To(Succeed()) }()
	var out []schemaObject
	for rows.Next() {
		var o schemaObject
		Expect(rows.Scan(&o.Type, &o.Name, &o.Table)).To(Succeed())
		out = append(out, o)
	}
	Expect(rows.Err()).NotTo(HaveOccurred())
	return out
}

func tableSQL(db *sql.DB, table string) string {
	var s string
	Expect(db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name = ?`, table).Scan(&s)).To(Succeed())
	return s
}

var _ = Describe("SQLite table rebuilds through the engine", func() {
	var db *sql.DB
	BeforeEach(func() { db = openDB("") })

	seed := func(ctx SpecContext) {
		Expect(mustApply(ctx, db, mustPlan(ctx, db, completeScope(), projectsHCL)).Outcome).To(Equal(engine.Applied))
		mustExec(db,
			`INSERT INTO projects (id, external_key, payload) VALUES ('p1', 'kept', '{}')`,
			`INSERT INTO events (id, project_id, active) VALUES ('e1', 'p1', true)`)
	}
	changedDefault := func() string {
		return strings.Replace(projectsHCL, `default = sql("'{}'")`, `default = sql("'[]'")`, 1)
	}
	// A declared scope manages only projects, so the desired schema declares only projects.
	projectsOnly := func() string {
		hcl := changedDefault()
		return hcl[:strings.Index(hcl, `table "events"`)]
	}

	It("creates the tables and indexes, and a second plan is empty", func(ctx SpecContext) {
		seed(ctx)
		Expect(schemaObjects(db)).To(Equal([]schemaObject{
			{"index", "events_active_key", "events"},
			{"index", "projects_external_key_key", "projects"},
			{"table", "events", "events"},
			{"table", "projects", "projects"},
		}))
		Expect(mustPlan(ctx, db, completeScope(), projectsHCL).Units).To(BeEmpty())
	})

	It("adds a declared column in place, keeping rows and column order", func(ctx SpecContext) {
		seed(ctx)
		withNotes := strings.Replace(projectsHCL, "  primary_key {\n    columns = [column.id]\n  }\n  index \"projects_external", "  column \"description\" {\n    type = text\n    null = true\n  }\n  primary_key {\n    columns = [column.id]\n  }\n  index \"projects_external", 1)
		a := mustPlan(ctx, db, completeScope(), withNotes)
		Expect(statements(a)).To(Equal([]string{"ALTER TABLE `projects` ADD COLUMN `description` text NULL"}))
		mustApply(ctx, db, a)
		Expect(columnNames(db, "projects")).To(Equal([]string{"id", "external_key", "payload", "description"}))
		var key string
		Expect(db.QueryRow(`SELECT external_key FROM projects WHERE id = 'p1'`).Scan(&key)).To(Succeed())
		Expect(key).To(Equal("kept"))
	})

	It("rebuilds for a changed default keeping rows, indexes and foreign keys, then settles", func(ctx SpecContext) {
		seed(ctx)
		before := schemaObjects(db)
		a := mustPlan(ctx, db, completeScope(), changedDefault())
		Expect(changeIDs(a)).To(Equal([]string{changeID("modify", "column", "projects", "payload")}))
		Expect(a.Units).To(HaveLen(1))
		mustApply(ctx, db, a, engine.Acknowledgements(a)...)

		Expect(schemaObjects(db)).To(Equal(before))
		Expect(tableSQL(db, "projects")).To(ContainSubstring("'[]'"))
		Expect(tableSQL(db, "events")).To(ContainSubstring("REFERENCES `projects`"))
		var payload string
		Expect(db.QueryRow(`SELECT payload FROM projects WHERE id = 'p1'`).Scan(&payload)).To(Succeed())
		Expect(payload).To(Equal("{}"))
		Expect(mustPlan(ctx, db, completeScope(), changedDefault()).Units).To(BeEmpty())
	})

	It("drops and re-creates a view over a rebuilt table, even one outside the declared scope", func(ctx SpecContext) {
		seed(ctx)
		mustExec(db, `CREATE VIEW project_keys AS SELECT id, external_key FROM projects`, `CREATE VIEW key_count AS SELECT count(*) AS n FROM project_keys`)
		viewSQL := tableSQL(db, "project_keys")
		a := mustPlan(ctx, db, declaredScope("projects"), projectsOnly())
		rebuilt := map[string]string{}
		for _, c := range a.Changes {
			if c.Action == "rebuild" {
				rebuilt[c.Object.Name] = c.Cause
			}
		}
		cause := changeID("modify", "column", "projects", "payload")
		Expect(rebuilt).To(Equal(map[string]string{"project_keys": cause, "key_count": cause}))

		mustApply(ctx, db, a, engine.Acknowledgements(a)...)
		Expect(tableSQL(db, "project_keys")).To(Equal(viewSQL))
		var n int
		Expect(db.QueryRow(`SELECT n FROM key_count`).Scan(&n)).To(Succeed())
		Expect(n).To(Equal(1))
		Expect(mustPlan(ctx, db, declaredScope("projects"), projectsOnly()).Units).To(BeEmpty())
	})

	It("drops an undeclared column and index of a complete declaration through a declared-scope rebuild, keeping the rows and an undeclared table", func(ctx SpecContext) {
		seed(ctx)
		mustExec(db, `ALTER TABLE projects ADD COLUMN notes TEXT`, `UPDATE projects SET notes = 'dba note'`,
			`CREATE INDEX projects_notes_idx ON projects (notes)`, `CREATE TABLE scratch (value TEXT)`, `INSERT INTO scratch VALUES ('dba table')`)
		a := mustPlan(ctx, db, declaredScope("projects"), projectsOnly())
		Expect(a.Preserved).To(BeEmpty())
		Expect(changeIDs(a)).To(ConsistOf(
			changeID("modify", "column", "projects", "payload"),
			changeID("drop", "column", "projects", "notes"),
			changeID("drop", "index", "projects", "projects_notes_idx")))
		Expect(engine.Acknowledgements(a)).To(ConsistOf(changeID("drop", "column", "projects", "notes"), changeID("drop", "index", "projects", "projects_notes_idx")))

		mustApply(ctx, db, a, engine.Acknowledgements(a)...)
		var key, scratch string
		Expect(db.QueryRow(`SELECT external_key FROM projects WHERE id = 'p1'`).Scan(&key)).To(Succeed())
		Expect(db.QueryRow(`SELECT value FROM scratch`).Scan(&scratch)).To(Succeed())
		Expect(columnNames(db, "projects")).To(Equal([]string{"id", "external_key", "payload"}))
		Expect([]string{key, scratch}).To(Equal([]string{"kept", "dba table"}))
		Expect(mustPlan(ctx, db, declaredScope("projects"), projectsOnly()).Units).To(BeEmpty())
	})

	It("keeps an undeclared column, index and table with their data through a declared-scope rebuild of a partial table", func(ctx SpecContext) {
		seed(ctx)
		mustExec(db, `ALTER TABLE projects ADD COLUMN notes TEXT`, `UPDATE projects SET notes = 'dba note'`,
			`CREATE INDEX projects_notes_idx ON projects (notes)`, `CREATE TABLE scratch (value TEXT)`, `INSERT INTO scratch VALUES ('dba table')`)
		partialProjects := func() string {
			return strings.Replace(projectsOnly(), "  schema = schema.main\n", "  schema = schema.main\n  partial = true\n", 1)
		}
		a := mustPlan(ctx, db, declaredScope("projects"), partialProjects())
		Expect(changeIDs(a)).To(Equal([]string{changeID("modify", "column", "projects", "payload")}))
		preserved := []string{}
		for _, c := range a.Preserved {
			preserved = append(preserved, c.ID)
		}
		Expect(preserved).To(ConsistOf(changeID("drop", "column", "projects", "notes"), changeID("drop", "index", "projects", "projects_notes_idx")))

		mustApply(ctx, db, a, engine.Acknowledgements(a)...)
		var note, scratch, indexed string
		Expect(db.QueryRow(`SELECT notes FROM projects WHERE id = 'p1'`).Scan(&note)).To(Succeed())
		Expect(db.QueryRow(`SELECT value FROM scratch`).Scan(&scratch)).To(Succeed())
		Expect(db.QueryRow(`SELECT name FROM sqlite_schema WHERE type = 'index' AND name = 'projects_notes_idx'`).Scan(&indexed)).To(Succeed())
		Expect(tableSQL(db, "projects")).To(ContainSubstring("'[]'"))
		Expect([]string{note, scratch}).To(Equal([]string{"dba note", "dba table"}))
		Expect(mustPlan(ctx, db, declaredScope("projects"), partialProjects()).Units).To(BeEmpty())
	})

	It("keeps a column whose drop is left out of the selection in complete scope, and leaves that drop pending", func(ctx SpecContext) {
		seed(ctx)
		mustExec(db, `ALTER TABLE projects ADD COLUMN notes TEXT`, `UPDATE projects SET notes = 'dba note'`)
		modify, drop := changeID("modify", "column", "projects", "payload"), changeID("drop", "column", "projects", "notes")
		a := mustPlan(ctx, db, completeScope(), changedDefault(), modify)
		Expect(changeIDs(a)).To(Equal([]string{modify}))
		mustApply(ctx, db, a, engine.Acknowledgements(a)...)
		var note string
		Expect(db.QueryRow(`SELECT notes FROM projects WHERE id = 'p1'`).Scan(&note)).To(Succeed())
		Expect(note).To(Equal("dba note"))
		Expect(changeIDs(mustPlan(ctx, db, completeScope(), changedDefault()))).To(Equal([]string{drop}))
	})

	It("refuses to drop a column while an index on it is kept", func(ctx SpecContext) {
		seed(ctx)
		mustExec(db, `ALTER TABLE projects ADD COLUMN notes TEXT`, `CREATE INDEX projects_notes_idx ON projects (notes)`)
		_, err := planLive(ctx, db, completeScope(), desiredOf(changedDefault()), changeID("modify", "column", "projects", "payload"), changeID("drop", "column", "projects", "notes"))
		Expect(err).To(MatchError(ContainSubstring("requires unselected changes " + changeID("drop", "index", "projects", "projects_notes_idx"))))
	})

	It("rebuilds on a database opened with foreign key enforcement on, and leaves enforcement and the busy timeout as they were", func(ctx SpecContext) {
		db = openDB("?_pragma=foreign_keys(1)")
		db.SetMaxOpenConns(1)
		seed(ctx)
		var before, after, enforced int
		Expect(db.QueryRow(`PRAGMA busy_timeout`).Scan(&before)).To(Succeed())

		a := mustPlan(ctx, db, completeScope(), changedDefault())
		mustApply(ctx, db, a, engine.Acknowledgements(a)...)

		Expect(tableSQL(db, "projects")).To(ContainSubstring("'[]'"))
		var events int
		Expect(db.QueryRow(`SELECT count(*) FROM events`).Scan(&events)).To(Succeed())
		Expect(events).To(Equal(1), "the rebuild must not run ON DELETE CASCADE on the children")
		Expect(db.QueryRow(`PRAGMA foreign_keys`).Scan(&enforced)).To(Succeed())
		Expect(db.QueryRow(`PRAGMA busy_timeout`).Scan(&after)).To(Succeed())
		Expect([]int{enforced, after}).To(Equal([]int{1, before}))
	})

	It("rolls a rebuild back when it would leave a dangling reference, and restores enforcement", func(ctx SpecContext) {
		db.SetMaxOpenConns(1)
		mustExec(db, `CREATE TABLE parent (id integer NOT NULL, PRIMARY KEY (id))`, `CREATE TABLE child (id integer NOT NULL, parent_id integer NOT NULL)`,
			`INSERT INTO child VALUES (1, 99)`, `PRAGMA foreign_keys = 1`)
		a := mustPlan(ctx, db, completeScope(), `schema "main" {}
table "parent" {
  schema = schema.main
  column "id" { type = integer }
  primary_key {
    columns = [column.id]
  }
}
table "child" {
  schema = schema.main
  column "id" { type = integer }
  column "parent_id" { type = integer }
  foreign_key "child_parent_fk" {
    columns = [column.parent_id]
    ref_columns = [table.parent.column.id]
  }
}`)
		_, err := apply(ctx, db, a, engine.Acknowledgements(a)...)
		Expect(err).To(MatchError(ContainSubstring("foreign key check failed")))
		Expect(tableSQL(db, "child")).NotTo(ContainSubstring("REFERENCES"))
		var rows, enforced int
		Expect(db.QueryRow(`SELECT count(*) FROM child`).Scan(&rows)).To(Succeed())
		Expect(db.QueryRow(`PRAGMA foreign_keys`).Scan(&enforced)).To(Succeed())
		Expect([]int{rows, enforced}).To(Equal([]int{1, 1}))
	})
})
