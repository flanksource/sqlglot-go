package sqlglot

import "testing"

func TestStatementTerminators(t *testing.T) {
	statements := []struct{ sql, class string }{
		{"CREATE TABLE users (id INT PRIMARY KEY, email VARCHAR(255) NOT NULL UNIQUE)", "Create"},
		{"CREATE VIEW users_view AS SELECT id FROM users", "Create"},
		{"INSERT INTO users (id, email) VALUES (1, 'a;b')", "Insert"},
		{"UPDATE users SET email = 'a;b' WHERE id = 1", "Update"},
		{"DELETE FROM users WHERE id = 1", "Delete"},
		{"SELECT 'a;b' FROM users", "Select"},
	}
	for _, dialect := range []string{"postgres", "tsql", "mysql"} {
		for _, statement := range statements {
			t.Run(dialect+"/"+statement.sql, func(t *testing.T) {
				for _, suffix := range []string{";", " /* ; */ ; -- trailing ;\n"} {
					tree, err := ParseOne(statement.sql+suffix, dialect)
					if err != nil {
						t.Fatalf("suffix %q: %v", suffix, err)
					}
					if tree.Class != statement.class {
						t.Fatalf("class = %s, want %s", tree.Class, statement.class)
					}
					written, err := Generate(tree, dialect)
					want := statement.sql
					if dialect == "tsql" && statement.sql == statements[0].sql {
						want = "CREATE TABLE users (id INTEGER PRIMARY KEY, email VARCHAR(255) NOT NULL UNIQUE)"
					}
					if err != nil || written != want {
						t.Fatalf("wrote %q (%v), want %q", written, err, want)
					}
					batch, err := ParseOne(statement.sql+suffix+"SELECT 2;", dialect)
					if err != nil {
						t.Fatalf("batch: %v", err)
					}
					items, _ := batch.Args["expressions"].([]*Expression)
					if batch.Class != "Block" || len(items) != 2 || items[0].Class != statement.class || items[1].Class != "Select" {
						t.Fatalf("batch = %#v, want %s followed by Select", batch, statement.class)
					}
				}
			})
		}
	}
}

func TestStatementTerminatorDoesNotHideInvalidTokens(t *testing.T) {
	for _, sql := range []string{
		"CREATE TABLE users (id INT) unexpected;",
		"INSERT INTO users (id) VALUES (1) unexpected;",
		"UPDATE users SET id = 1 unexpected;",
		"DELETE FROM users WHERE id = 1 unexpected;",
		"CREATE TABLE users (id INT); SELECT FROM;",
	} {
		t.Run(sql, func(t *testing.T) {
			if tree, err := ParseOne(sql, "postgres"); err == nil {
				t.Fatalf("accepted invalid SQL as %s", tree.Class)
			}
		})
	}
}
