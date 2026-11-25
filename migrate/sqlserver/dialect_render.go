package sqlserver

import (
	"fmt"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

func quoteAll(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = quote(n)
	}
	return strings.Join(quoted, ", ")
}

// dropStatements drops a dependent. The statements stay valid at database
// compatibility level 100: no DROP ... IF EXISTS.
func dropStatements(dep engine.Dependent) ([]string, error) {
	if err := checkDependent(dep); err != nil {
		return nil, err
	}
	table := qualified(dep.Table)
	switch dep.Kind {
	case engine.DependentIndex:
		if dep.Index.Constraint {
			return []string{"ALTER TABLE " + table + " DROP CONSTRAINT " + quote(dep.Name)}, nil
		}
		return []string{"DROP INDEX " + quote(dep.Name) + " ON " + table}, nil
	case engine.DependentStatistic:
		return []string{"DROP STATISTICS " + table + "." + quote(dep.Name)}, nil
	}
	return []string{"ALTER TABLE " + table + " DROP CONSTRAINT " + quote(dep.Name)}, nil
}

// createStatements re-creates a dependent from its live definition, under its
// own name. Foreign keys and checks come back trusted.
func createStatements(dep engine.Dependent) ([]string, error) {
	if err := checkDependent(dep); err != nil {
		return nil, err
	}
	switch dep.Kind {
	case engine.DependentIndex, engine.DependentPrimaryKey:
		return indexCreate(dep.Table, *dep.Index)
	case engine.DependentForeignKey:
		return foreignKeyCreate(dep.Table, *dep.ForeignKey)
	case engine.DependentCheck:
		return checkCreate(dep.Table, *dep.Check), nil
	case engine.DependentDefault:
		return []string{"ALTER TABLE " + qualified(dep.Table) + " ADD CONSTRAINT " + quote(dep.Name) + " DEFAULT " + dep.Definition + " FOR " + quote(dep.On[0].Column)}, nil
	}
	return []string{dep.Definition}, nil
}

// checkDependent refuses a dependent the planner cannot render, naming it.
func checkDependent(dep engine.Dependent) error {
	label := fmt.Sprintf("%s %s on %s", dep.Kind, dep.Name, qualified(dep.Table))
	if dep.Name == "" {
		return fmt.Errorf("sqlserver: cannot rebuild a %s on %s: it has no name", dep.Kind, qualified(dep.Table))
	}
	switch dep.Kind {
	case engine.DependentIndex:
		if dep.Index == nil || dep.Index.Primary {
			return fmt.Errorf("sqlserver: cannot rebuild %s: kind index needs an index that is not a primary key", label)
		}
	case engine.DependentPrimaryKey:
		if dep.Index == nil || !dep.Index.Primary {
			return fmt.Errorf("sqlserver: cannot rebuild %s: kind primary-key needs a primary key index", label)
		}
	case engine.DependentUniqueConstraint:
		return fmt.Errorf("sqlserver: cannot rebuild %s: a unique constraint is kind index with Index.Constraint, the kind its change carries", label)
	case engine.DependentForeignKey:
		if dep.ForeignKey == nil {
			return fmt.Errorf("sqlserver: cannot rebuild %s: no foreign key definition", label)
		}
	case engine.DependentCheck:
		if dep.Check == nil || strings.TrimSpace(dep.Check.Expression) == "" {
			return fmt.Errorf("sqlserver: cannot rebuild %s: no check expression", label)
		}
	case engine.DependentDefault:
		if strings.TrimSpace(dep.Definition) == "" || len(dep.On) != 1 {
			return fmt.Errorf("sqlserver: cannot rebuild %s: a default needs its expression in Definition and the one column it binds to in On", label)
		}
	case engine.DependentStatistic:
		if strings.TrimSpace(dep.Definition) == "" {
			return fmt.Errorf("sqlserver: cannot rebuild %s: no CREATE STATISTICS text in Definition", label)
		}
	default:
		return fmt.Errorf("sqlserver: cannot rebuild %s: unsupported dependent kind %q", label, dep.Kind)
	}
	return nil
}

func indexCreate(table migrate.Name, idx migrate.Index) ([]string, error) {
	if len(idx.Columns) == 0 {
		return nil, fmt.Errorf("sqlserver: index %s on %s has no key columns", idx.Name, qualified(table))
	}
	options, err := indexOptions(idx)
	if err != nil {
		return nil, fmt.Errorf("sqlserver: index %s on %s: %w", idx.Name, qualified(table), err)
	}
	clustering := "NONCLUSTERED"
	if idx.Clustered {
		clustering = "CLUSTERED"
	}
	keys := make([]string, len(idx.Columns))
	for i, c := range idx.Columns {
		keys[i] = quote(c.Name)
		if c.Descending {
			keys[i] += " DESC"
		}
	}
	key := "(" + strings.Join(keys, ", ") + ")"
	if idx.Primary || idx.Constraint {
		return keyConstraintCreate(table, idx, clustering, key, options)
	}
	sql := "CREATE "
	if idx.Unique {
		sql += "UNIQUE "
	}
	sql += clustering + " INDEX " + quote(idx.Name) + " ON " + qualified(table) + " " + key
	if len(idx.Included) > 0 {
		sql += " INCLUDE (" + quoteAll(idx.Included) + ")"
	}
	if idx.Filter != "" {
		sql += " WHERE " + idx.Filter
	}
	return []string{sql + options + filegroup(idx)}, nil
}

// keyConstraintCreate renders a primary key or unique constraint, which cannot
// carry included columns or a filter.
func keyConstraintCreate(table migrate.Name, idx migrate.Index, clustering, key, options string) ([]string, error) {
	if len(idx.Included) > 0 || idx.Filter != "" {
		return nil, fmt.Errorf("sqlserver: %s on %s is a constraint and cannot have included columns or a filter", idx.Name, qualified(table))
	}
	kind := "UNIQUE"
	if idx.Primary {
		kind = "PRIMARY KEY"
	}
	return []string{"ALTER TABLE " + qualified(table) + " ADD CONSTRAINT " + quote(idx.Name) + " " + kind + " " + clustering + " " + key + options + filegroup(idx)}, nil
}

func filegroup(idx migrate.Index) string {
	if idx.Filegroup == "" {
		return ""
	}
	return " ON " + quote(idx.Filegroup)
}

// indexOptions renders the WITH clause: only the options that differ from the
// server defaults, in a fixed order.
func indexOptions(idx migrate.Index) (string, error) {
	var opts []string
	if idx.PadIndex {
		opts = append(opts, "PAD_INDEX = ON")
	}
	if idx.FillFactor < 0 || idx.FillFactor > 100 {
		return "", fmt.Errorf("fill factor %d is outside 0-100", idx.FillFactor)
	}
	if idx.FillFactor > 0 && idx.FillFactor < 100 {
		opts = append(opts, fmt.Sprintf("FILLFACTOR = %d", idx.FillFactor))
	}
	for _, flag := range []struct {
		on  bool
		sql string
	}{
		{idx.IgnoreDupKey, "IGNORE_DUP_KEY = ON"},
		{idx.NoRecompute, "STATISTICS_NORECOMPUTE = ON"},
		{idx.NoRowLocks, "ALLOW_ROW_LOCKS = OFF"},
		{idx.NoPageLocks, "ALLOW_PAGE_LOCKS = OFF"},
	} {
		if flag.on {
			opts = append(opts, flag.sql)
		}
	}
	switch idx.Compression {
	case "":
	case "ROW", "PAGE":
		opts = append(opts, "DATA_COMPRESSION = "+idx.Compression)
	default:
		return "", fmt.Errorf("compression %q is not ROW or PAGE", idx.Compression)
	}
	if len(opts) == 0 {
		return "", nil
	}
	return " WITH (" + strings.Join(opts, ", ") + ")", nil
}

func foreignKeyCreate(table migrate.Name, fk migrate.ForeignKey) ([]string, error) {
	if len(fk.Columns) == 0 || len(fk.Columns) != len(fk.ReferencedColumns) {
		return nil, fmt.Errorf("sqlserver: foreign key %s on %s needs matching columns and referencedColumns", fk.Name, qualified(table))
	}
	sql := "ALTER TABLE " + qualified(table) + " WITH CHECK ADD CONSTRAINT " + quote(fk.Name) +
		" FOREIGN KEY (" + quoteAll(fk.Columns) + ") REFERENCES " + qualified(fk.References) + " (" + quoteAll(fk.ReferencedColumns) + ")"
	for _, action := range []struct{ clause, value string }{{"ON DELETE", fk.OnDelete}, {"ON UPDATE", fk.OnUpdate}} {
		if action.value == "" {
			continue
		}
		option, err := referenceAction(action.value)
		if err != nil {
			return nil, fmt.Errorf("foreign key %s on %s: %w", fk.Name, qualified(table), err)
		}
		sql += " " + action.clause + " " + string(option)
	}
	if fk.NotForReplication {
		sql += " NOT FOR REPLICATION"
	}
	return []string{sql, trustStatement(table, fk.Name)}, nil
}

func checkCreate(table migrate.Name, c migrate.Check) []string {
	sql := "ALTER TABLE " + qualified(table) + " WITH CHECK ADD CONSTRAINT " + quote(c.Name) + " CHECK "
	if c.NotForReplication {
		sql += "NOT FOR REPLICATION "
	}
	return []string{sql + "(" + c.Expression + ")", trustStatement(table, c.Name)}
}

// trustStatement enables a constraint WITH CHECK created it, so it is trusted
// and enforced.
func trustStatement(table migrate.Name, name string) string {
	return "ALTER TABLE " + qualified(table) + " CHECK CONSTRAINT " + quote(name)
}
