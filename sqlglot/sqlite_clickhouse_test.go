package sqlglot

import (
	"slices"
	"testing"
)

// TestSQLiteAndClickHouseAreDialects: both are named dialects, not neutral
// SQL under another name.
func TestSQLiteAndClickHouseAreDialects(t *testing.T) {
	for _, d := range []string{"sqlite", "clickhouse"} {
		if !slices.Contains(Dialects(), d) {
			t.Errorf("Dialects() = %v, missing %q", Dialects(), d)
		}
	}
}

// TestProviderShapesRoundTrip holds the paged and lookup shapes commons-db's
// SQL provider builds for SQLite and ClickHouse connections to what the
// pinned reference writes for them. Where the reference does not write the
// input back as it was, the expectation is the reference's string, not the
// input: SQLite's `x IS NOT NULL` comes back as `NOT x IS NULL`, ClickHouse's
// `toStartOfHour(ts)` as `dateTrunc('HOUR', ts)` and its `?` as `{?: }`.
// ClickHouse's case-sensitive names -- count, arrayJoin, has -- must survive.
func TestProviderShapesRoundTrip(t *testing.T) {
	cases := []struct{ dialect, sql, want string }{
		{
			"sqlite",
			`WITH "__cdb_base" AS (SELECT * FROM orders WHERE x = ?) SELECT "region" AS value, COUNT(*) AS count, COUNT(*) OVER () AS total FROM "__cdb_base" WHERE "env" IN (?, ?) AND "region" IS NOT NULL GROUP BY "region" ORDER BY 2 DESC, 1 ASC LIMIT 20 OFFSET 5`,
			`WITH "__cdb_base" AS (SELECT * FROM orders WHERE x = ?) SELECT "region" AS value, COUNT(*) AS count, COUNT(*) OVER () AS total FROM "__cdb_base" WHERE "env" IN (?, ?) AND NOT "region" IS NULL GROUP BY "region" ORDER BY 2 DESC, 1 ASC LIMIT 20 OFFSET 5`,
		},
		{
			"clickhouse",
			`WITH "__cdb_base" AS (SELECT toStartOfHour(ts) AS h, count() AS c FROM events WHERE service = ?) SELECT h, c FROM "__cdb_base" WHERE c > ? ORDER BY h DESC LIMIT 20 OFFSET 5`,
			`WITH "__cdb_base" AS (SELECT dateTrunc('HOUR', ts) AS h, count() AS c FROM events WHERE service = {?: }) SELECT h, c FROM "__cdb_base" WHERE c > {?: } ORDER BY h DESC LIMIT 20 OFFSET 5`,
		},
		{
			"clickhouse",
			`SELECT arrayJoin(tags) AS tag FROM events FINAL WHERE has(tags, ?) LIMIT 10`,
			`SELECT arrayJoin(tags) AS tag FROM events FINAL WHERE has(tags, {?: }) LIMIT 10`,
		},
	}
	for _, c := range cases {
		assertRoundTrip(t, c.dialect, c.sql, c.want)
	}
}

// TestSQLiteGroupConcatOrderBy: SQLite 3.44 orders an aggregate's input inside
// the call, after its arguments, so GROUP_CONCAT keeps its ORDER BY -- with a
// DISTINCT and with a separator -- where the reference refuses it.
func TestSQLiteGroupConcatOrderBy(t *testing.T) {
	cases := []struct{ sql, want string }{
		{`SELECT group_concat(DISTINCT x ORDER BY x) FROM t`, `SELECT GROUP_CONCAT(DISTINCT x ORDER BY x) FROM t`},
		{`SELECT group_concat(x, ';' ORDER BY y DESC) FROM t`, `SELECT GROUP_CONCAT(x, ';' ORDER BY y DESC) FROM t`},
		{`SELECT group_concat(x ORDER BY y, z) FROM t`, `SELECT GROUP_CONCAT(x ORDER BY y, z) FROM t`},
		{`SELECT group_concat(DISTINCT x) FROM t`, `SELECT GROUP_CONCAT(DISTINCT x) FROM t`},
	}
	for _, c := range cases {
		assertRoundTrip(t, "sqlite", c.sql, c.want)
	}
}

func assertRoundTrip(t *testing.T, dialect, sql, want string) {
	t.Helper()
	tree, err := ParseOne(sql, dialect)
	if err != nil {
		t.Errorf("[%s] parse %s: %v", dialect, sql, err)
		return
	}
	got, err := Generate(tree, dialect)
	if err != nil {
		t.Errorf("[%s] generate %s: %v", dialect, sql, err)
		return
	}
	if got != want {
		t.Errorf("[%s] %s\n want %s\n got  %s", dialect, sql, want, got)
	}
}
