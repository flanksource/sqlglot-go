package sqlserver

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate"
)

// HCLLayout says how ExportHCL splits the tables it writes.
type HCLLayout int

const (
	// HCLDocument writes every table into one file, with one schema block per schema.
	HCLDocument HCLLayout = iota
	// HCLFilePerTable writes one file per table, each starting with its own schema block.
	HCLFilePerTable
)

// HCLOptions configure ExportHCL.
type HCLOptions struct {
	Layout HCLLayout
	// OmitUnsupported leaves out what the SQL Server HCL cannot declare, each
	// omission written as a comment line. Without it the export fails, naming
	// the table, the member and the attribute.
	OmitUnsupported bool
	// Columns declares only the named columns of a table, keyed by table name: a
	// partial declaration (`partial = true`), with no keys, indexes, foreign keys
	// or checks, for a table the declaration does not own. A table declared
	// without Columns is declared in full and carries no attribute, unless its
	// schema marks it Partial.
	Columns map[string][]string
	// DatabaseCollation is the database's default collation. A column carrying
	// it is written without a collation, so the HCL stays valid on a database
	// with another default. Empty writes every column's collation.
	DatabaseCollation string
	// Notes are comment lines written at the top of the output (of the first
	// file), for what the caller left out before the export, each without its
	// comment marker.
	Notes []string
}

// HCLFile is one file ExportHCL writes: Name is relative and ends in .hcl.
type HCLFile struct {
	Name    string
	Content string
}

// ExportHCL writes the tables of s as SQL Server HCL that EvalHCL and FromAtlas
// read back to the same tables (members in a different order, and the names
// SQL Server gave its system-named defaults, apart). Objects other than tables
// are not part of the HCL; they and everything else the HCL cannot declare
// (ROWGUIDCOL, fill factor and the other index options, NOT FOR REPLICATION, a
// foreign key to a table outside s) fail the export unless HCLOptions
// OmitUnsupported is set.
func ExportHCL(s migrate.Schema, opts HCLOptions) ([]HCLFile, error) {
	w := &hclWriter{opts: opts, exported: map[migrate.Name]bool{}}
	for _, note := range opts.Notes {
		w.header += "# " + note + "\n"
	}
	tables := slices.Clone(s.Tables)
	slices.SortFunc(tables, func(a, b migrate.Table) int { return compareNames(a.Name, b.Name) })
	for _, t := range tables {
		w.exported[t.Name] = true
	}
	if err := w.checkRequest(s, tables); err != nil {
		return nil, err
	}
	texts := make([]string, len(tables))
	for i, t := range tables {
		texts[i] = w.table(t)
	}
	if len(w.problems) > 0 {
		return nil, errors.Join(w.problems...)
	}
	return w.assemble(tables, texts)
}

type hclWriter struct {
	opts     HCLOptions
	exported map[migrate.Name]bool
	problems []error
	// header is the comment lines for what was omitted that no table owns.
	header string
}

// checkRequest refuses what the schema or the options name that cannot be exported.
func (w *hclWriter) checkRequest(s migrate.Schema, tables []migrate.Table) error {
	if len(tables) == 0 {
		return errors.New("sqlserver: no tables to export")
	}
	for _, o := range otherObjects(s) {
		reason := fmt.Sprintf("%s.%s is not a table, and the HCL declares tables only", o.Schema, o.Name)
		if !w.opts.OmitUnsupported {
			w.problems = append(w.problems, fmt.Errorf("sqlserver: %s; set OmitUnsupported to leave it out", reason))
			continue
		}
		w.header += "# omitted: " + reason + "\n"
	}
	for name, columns := range w.opts.Columns {
		i := slices.IndexFunc(tables, func(t migrate.Table) bool { return t.Name.Name == name })
		if i < 0 {
			return fmt.Errorf("sqlserver: columns requested of table %q, which is not in the schema", name)
		}
		for _, c := range columns {
			if !slices.ContainsFunc(tables[i].Columns, func(col migrate.Column) bool { return col.Name == c }) {
				return fmt.Errorf("sqlserver: table %q has no column %q", name, c)
			}
		}
	}
	return nil
}

// otherObjects lists the objects of s that are not tables.
func otherObjects(s migrate.Schema) []migrate.Name {
	var names []migrate.Name
	for _, d := range s.Definitions {
		names = append(names, d.Name)
	}
	for _, q := range s.Sequences {
		names = append(names, q.Name)
	}
	for _, t := range s.TableTypes {
		names = append(names, t.Name)
	}
	return names
}

// unsupported records that something cannot be declared. With OmitUnsupported it
// returns the comment line to write in its place; otherwise it records the
// failure and returns "".
func (w *hclWriter) unsupported(table migrate.Name, member, attribute, why string) string {
	label := fmt.Sprintf("table %s.%s", table.Schema, table.Name)
	if member != "" {
		label += ", " + member
	}
	if w.opts.OmitUnsupported {
		return fmt.Sprintf("  # omitted: %s: %s %s\n", label, attribute, why)
	}
	w.problems = append(w.problems, fmt.Errorf("sqlserver: %s: %s %s; set OmitUnsupported to leave it out", label, attribute, why))
	return ""
}

func (w *hclWriter) assemble(tables []migrate.Table, texts []string) ([]HCLFile, error) {
	if w.opts.Layout == HCLDocument {
		var b strings.Builder
		b.WriteString(w.header)
		var declared []string
		for _, t := range tables {
			if !slices.Contains(declared, t.Name.Schema) {
				declared = append(declared, t.Name.Schema)
				fmt.Fprintf(&b, "schema %s {}\n", hclString(t.Name.Schema))
			}
		}
		for _, text := range texts {
			b.WriteString("\n" + text)
		}
		return []HCLFile{{Name: "schema.hcl", Content: b.String()}}, nil
	}
	files := make([]HCLFile, len(tables))
	for i, t := range tables {
		if strings.ContainsAny(t.Name.Name, `/\`) || slices.ContainsFunc(files[:i], func(f HCLFile) bool { return f.Name == t.Name.Name+".hcl" }) {
			return nil, fmt.Errorf("sqlserver: table %s.%s has no distinct file name", t.Name.Schema, t.Name.Name)
		}
		files[i] = HCLFile{Name: t.Name.Name + ".hcl", Content: fmt.Sprintf("schema %s {}\n\n%s", hclString(t.Name.Schema), texts[i])}
	}
	files[0].Content = w.header + files[0].Content
	return files, nil
}

func (w *hclWriter) table(t migrate.Table) string {
	var b strings.Builder
	fmt.Fprintf(&b, "table %s {\n  schema = schema.%s\n", hclString(t.Name.Name), t.Name.Schema)
	partial, isPartial := w.opts.Columns[t.Name.Name]
	if isPartial || t.Partial {
		b.WriteString("  partial = true\n")
	}
	for _, c := range t.Columns {
		if isPartial && !slices.Contains(partial, c.Name) {
			continue
		}
		w.column(&b, t.Name, c)
	}
	if !isPartial {
		w.members(&b, t)
	}
	b.WriteString("}\n")
	return b.String()
}

func (w *hclWriter) members(b *strings.Builder, t migrate.Table) {
	indexes := slices.Clone(t.Indexes)
	slices.SortStableFunc(indexes, func(x, y migrate.Index) int {
		switch {
		case x.Primary == y.Primary:
			return 0
		case x.Primary:
			return -1
		}
		return 1
	})
	for _, i := range indexes {
		w.index(b, t.Name, i)
	}
	for _, f := range t.ForeignKeys {
		w.foreignKey(b, t.Name, f)
	}
	for _, c := range t.Checks {
		w.check(b, t.Name, c)
	}
}

var bareType = regexp.MustCompile(`^[a-z_0-9]+(\(\d+(,\d+)?\))?$`)

func hclType(t string) string {
	t = strings.ReplaceAll(t, ", ", ",")
	if bareType.MatchString(t) {
		return t
	}
	return "sql(" + hclString(t) + ")"
}

func (w *hclWriter) column(b *strings.Builder, table migrate.Name, c migrate.Column) {
	fmt.Fprintf(b, "  column %s {\n    null = %t\n    type = %s\n", hclString(c.Name), c.Nullable, hclType(c.Type))
	if c.Default != nil {
		fmt.Fprintf(b, "    default = sql(%s)\n", hclString(c.Default.Expression))
		if c.Default.Name != "" && !c.Default.SystemNamed {
			fmt.Fprintf(b, "    default_name = %s\n", hclString(c.Default.Name))
		}
	}
	if c.Collation != "" && !strings.EqualFold(c.Collation, w.opts.DatabaseCollation) {
		fmt.Fprintf(b, "    collate = %s\n", hclString(c.Collation))
	}
	if c.Identity != nil {
		fmt.Fprintf(b, "    identity {\n      seed      = %d\n      increment = %d\n    }\n", c.Identity.Seed, c.Identity.Increment)
	}
	if c.RowGUID {
		b.WriteString(w.unsupported(table, "column "+c.Name, "ROWGUIDCOL", "cannot be declared"))
	}
	b.WriteString("  }\n")
}

// undeclarableIndexOptions lists the options of an index the HCL cannot declare.
func undeclarableIndexOptions(i migrate.Index) []string {
	var out []string
	add := func(set bool, what string) {
		if set {
			out = append(out, what)
		}
	}
	add(i.FillFactor != 0, fmt.Sprintf("fill factor %d", i.FillFactor))
	add(i.PadIndex, "PAD_INDEX")
	add(i.IgnoreDupKey, "IGNORE_DUP_KEY")
	add(i.NoRowLocks, "ALLOW_ROW_LOCKS = OFF")
	add(i.NoPageLocks, "ALLOW_PAGE_LOCKS = OFF")
	add(i.NoRecompute, "STATISTICS_NORECOMPUTE")
	add(i.Compression != "", "data compression "+i.Compression)
	add(i.Filegroup != "", "filegroup "+i.Filegroup)
	return out
}

func (w *hclWriter) index(b *strings.Builder, table migrate.Name, i migrate.Index) {
	member := "index " + i.Name
	if i.Primary {
		member = "primary key " + i.Name
	}
	for _, option := range undeclarableIndexOptions(i) {
		b.WriteString(w.unsupported(table, member, option, "cannot be declared"))
	}
	if i.Primary && (i.Filter != "" || len(i.Included) > 0) {
		b.WriteString(w.unsupported(table, member, "filter or included columns", "cannot be declared on a primary key"))
	}
	if i.Primary {
		fmt.Fprintf(b, "  primary_key %s {\n", hclString(i.Name))
	} else {
		fmt.Fprintf(b, "  index %s {\n", hclString(i.Name))
		if i.Unique {
			b.WriteString("    unique = true\n")
		}
		if i.Constraint {
			b.WriteString("    constraint = true\n")
		}
	}
	if i.Clustered != i.Primary {
		fmt.Fprintf(b, "    clustered = %t\n", i.Clustered)
	}
	indexKey(b, i)
	if len(i.Included) > 0 {
		fmt.Fprintf(b, "    include = %s\n", columnRefs(i.Included))
	}
	if i.Filter != "" {
		fmt.Fprintf(b, "    where = %s\n", hclString(i.Filter))
	}
	b.WriteString("  }\n")
}

// indexKey writes the key columns: a list when none is descending, otherwise one
// `on` block per column.
func indexKey(b *strings.Builder, i migrate.Index) {
	if !slices.ContainsFunc(i.Columns, func(c migrate.IndexColumn) bool { return c.Descending }) {
		names := make([]string, len(i.Columns))
		for n, c := range i.Columns {
			names[n] = c.Name
		}
		fmt.Fprintf(b, "    columns = %s\n", columnRefs(names))
		return
	}
	for _, c := range i.Columns {
		fmt.Fprintf(b, "    on {\n      column = %s\n", columnRef(c.Name))
		if c.Descending {
			b.WriteString("      desc   = true\n")
		}
		b.WriteString("    }\n")
	}
}

func (w *hclWriter) foreignKey(b *strings.Builder, table migrate.Name, f migrate.ForeignKey) {
	member := "foreign key " + f.Name
	if !w.exported[f.References] || f.References.Schema != table.Schema {
		b.WriteString(w.unsupported(table, member, "reference to "+f.References.Schema+"."+f.References.Name,
			"cannot be declared: the HCL references a table of the same schema in the same export"))
		return
	}
	if f.NotForReplication {
		b.WriteString(w.unsupported(table, member, "NOT FOR REPLICATION", "cannot be declared"))
	}
	refs := make([]string, len(f.ReferencedColumns))
	for n, c := range f.ReferencedColumns {
		refs[n] = tableRef(f.References.Name) + "." + columnRef(c)
	}
	fmt.Fprintf(b, "  foreign_key %s {\n    columns     = %s\n    ref_columns = [%s]\n    on_update   = %s\n    on_delete   = %s\n  }\n",
		hclString(f.Name), columnRefs(f.Columns), strings.Join(refs, ", "), referenceVar(f.OnUpdate), referenceVar(f.OnDelete))
}

func referenceVar(action string) string {
	if action == "" {
		return "NO_ACTION"
	}
	return strings.ReplaceAll(strings.ToUpper(action), " ", "_")
}

func (w *hclWriter) check(b *strings.Builder, table migrate.Name, c migrate.Check) {
	if c.NotForReplication {
		b.WriteString(w.unsupported(table, "check "+c.Name, "NOT FOR REPLICATION", "cannot be declared"))
	}
	fmt.Fprintf(b, "  check %s {\n    expr = %s\n  }\n", hclString(c.Name), hclString(c.Expression))
}

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

func tableRef(name string) string {
	if identifier.MatchString(name) {
		return "table." + name
	}
	return "table[" + hclString(name) + "]"
}

func columnRef(name string) string {
	if identifier.MatchString(name) {
		return "column." + name
	}
	return "column[" + hclString(name) + "]"
}

func columnRefs(names []string) string {
	refs := make([]string, len(names))
	for i, n := range names {
		refs[i] = columnRef(n)
	}
	return "[" + strings.Join(refs, ", ") + "]"
}

// hclString is s as an HCL quoted string: control characters as \u escapes, and
// the template openers as $${ and %%{.
func hclString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04x`, r)
		case (r == '$' || r == '%') && strings.HasPrefix(s[i+1:], "{"):
			b.WriteRune(r)
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
