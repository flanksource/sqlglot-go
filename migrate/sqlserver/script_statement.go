package sqlserver

import (
	"fmt"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate"
)

// step is one statement of a unit and the change it implements.
type step struct {
	change migrate.Change
	sql    string
}

// requireIdempotent checks that every statement of an unguarded change
// converges when it runs again.
func requireIdempotent(steps []step, change migrate.Change) error {
	for _, s := range steps {
		if s.change.ID != change.ID {
			continue
		}
		upper := strings.ToUpper(s.sql)
		if !strings.Contains(upper, "CREATE OR ALTER ") && !strings.HasPrefix(upper, "ENABLE TRIGGER ") &&
			!strings.HasPrefix(upper, "DISABLE TRIGGER ") && !strings.HasPrefix(upper, "ALTER SEQUENCE ") {
			return fmt.Errorf("sqlserver: change %s: step %q is not re-runnable and has no state guard", change.ID, s.sql)
		}
	}
	return nil
}

// stepStatement runs one statement under its unit's flag. A statement that
// drops a column's baseline default resolves the constraint's name when the
// script runs, because a default's name differs between databases.
func stepStatement(snaps snapshots, s step, flag string) (string, error) {
	column, err := droppedDefault(snaps, s)
	if err != nil || column == "" {
		return "IF " + flag + " = 1 EXEC sys.sp_executesql " + literal(s.sql) + ";\n", err
	}
	table := qualified(s.change.Object)
	return "IF " + flag + " = 1\nBEGIN\nSET @df = NULL;\n" +
		"SELECT @df = dc.name FROM sys.default_constraints AS dc JOIN sys.columns AS c ON c.object_id = dc.parent_object_id AND c.column_id = dc.parent_column_id" +
		" WHERE c.object_id = OBJECT_ID(" + literal(table) + ") AND c.name = " + literal(column) + ";\n" +
		"IF @df IS NOT NULL\nBEGIN\nSET @sql = " + literal("ALTER TABLE "+table+" DROP CONSTRAINT ") + " + QUOTENAME(@df);\nEXEC sys.sp_executesql @sql;\nEND\nEND\n", nil
}

// droppedDefault names the column whose baseline default constraint a column
// change's statement drops, and refuses any other constraint drop in such a change.
func droppedDefault(snaps snapshots, s step) (string, error) {
	change := s.change
	if change.Kind != "column" || !strings.Contains(strings.ToUpper(s.sql), " DROP CONSTRAINT ") {
		return "", nil
	}
	table, _ := findTable(snaps.current, change.Object)
	column, err := member(table.Columns, change, func(c migrate.Column) bool { return c.Name == change.Member })
	if err != nil {
		return "", err
	}
	if column.Default == nil || s.sql != "ALTER TABLE "+qualified(change.Object)+" DROP CONSTRAINT "+quote(column.Default.Name) {
		return "", fmt.Errorf("sqlserver: change %s: step %q drops a constraint that is not the baseline default of column %q", change.ID, s.sql, change.Member)
	}
	return column.Name, nil
}
