package atlasdriver

import (
	"fmt"
	"slices"
	"strings"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/hashicorp/hcl/v2/hclparse"
)

const nativeFile = "snapshot.hcl"

// decode evaluates a Native snapshot to its single schema, a copy the caller
// may change.
func (d *Dialect) decode(native string) (*schema.Schema, error) {
	s, err := d.decodeCached(native)
	if err != nil {
		return nil, err
	}
	return cloneSchema(s), nil
}

// evaluate parses and evaluates a Native snapshot, uncached.
func (d *Dialect) evaluate(native string) (*schema.Schema, error) {
	if strings.TrimSpace(native) == "" {
		return nil, fmt.Errorf("%s: schema has no Native snapshot", d.Name())
	}
	p := hclparse.NewParser()
	if _, diag := p.ParseHCL([]byte(native), nativeFile); diag.HasErrors() {
		return nil, fmt.Errorf("%s: parse native schema: %s", d.Name(), diag.Error())
	}
	realm := &schema.Realm{}
	if err := d.atlas.Eval.Eval(p, realm, nil); err != nil {
		return nil, fmt.Errorf("%s: evaluate native schema: %w", d.Name(), err)
	}
	if len(realm.Schemas) != 1 {
		return nil, fmt.Errorf("%s: native schema must declare exactly one schema, got %d", d.Name(), len(realm.Schemas))
	}
	s := realm.Schemas[0]
	if len(s.Views)+len(s.Funcs)+len(s.Procs)+len(realm.Objects) > 0 {
		return nil, fmt.Errorf("%s: native schema %q holds objects other than tables and enum types", d.Name(), s.Name)
	}
	return s, nil
}

// encode serializes s to canonical Native HCL and proves the round trip lossless.
func (d *Dialect) encode(s *schema.Schema) (string, error) {
	canonicalize(s)
	data, err := d.atlas.Marshal.MarshalSpec(s)
	if err != nil {
		return "", fmt.Errorf("%s: serialize schema %q: %w", d.Name(), s.Name, err)
	}
	native := string(data)
	rt, err := d.decode(native)
	if err != nil {
		return "", err
	}
	changes, err := d.diffSchemas(s, rt)
	if err != nil {
		return "", err
	}
	if len(changes) > 0 {
		return "", &lossyError{changes: changes}
	}
	return native, nil
}

type lossyError struct{ changes []schema.Change }

func (e *lossyError) Error() string {
	return fmt.Sprintf("unsupported lossy schema snapshot: %d round-trip changes", len(e.changes))
}

// load decodes a schema's Native snapshot and keeps only the tables and enum
// types still present in its projection.
func (d *Dialect) load(m migrate.Schema) (*schema.Schema, error) {
	s, err := d.decode(m.Native)
	if err != nil {
		return nil, err
	}
	return s, d.filter(s, m)
}

func (d *Dialect) filter(s *schema.Schema, m migrate.Schema) error {
	keep := map[string]bool{}
	for _, t := range m.Tables {
		if t.Name.Schema != s.Name {
			return fmt.Errorf("%s: table %s.%s is not in native schema %q", d.Name(), t.Name.Schema, t.Name.Name, s.Name)
		}
		if _, ok := s.Table(t.Name.Name); !ok {
			return fmt.Errorf("%s: table %s.%s is in the projection but not in the native schema", d.Name(), t.Name.Schema, t.Name.Name)
		}
		keep[t.Name.Name] = true
	}
	s.Tables = slices.DeleteFunc(s.Tables, func(t *schema.Table) bool { return !keep[t.Name] })
	keepEnums := map[string]bool{}
	for _, def := range m.Definitions {
		if def.Kind == enumKind {
			keepEnums[def.Name.Name] = true
		}
	}
	for _, t := range s.Tables {
		for _, c := range t.Columns {
			if e := enumOf(c.Type.Type); e != nil {
				keepEnums[e.T] = true
			}
		}
	}
	s.Objects = slices.DeleteFunc(s.Objects, func(o schema.Object) bool {
		e, ok := o.(*schema.EnumType)
		return ok && !keepEnums[e.T]
	})
	return nil
}

// sortTables orders tables so every table follows those its foreign keys
// reference (Atlas HCL resolves cross-table references only backwards), ties
// and cycles broken by name so equal schemas serialize equally.
func sortTables(s *schema.Schema) {
	slices.SortFunc(s.Tables, func(a, b *schema.Table) int { return strings.Compare(a.Name, b.Name) })
	placed := map[string]bool{}
	ordered := make([]*schema.Table, 0, len(s.Tables))
	for len(ordered) < len(s.Tables) {
		progressed := false
		for _, t := range s.Tables {
			if !placed[t.Name] && referencesPlaced(s, t, placed) {
				placed[t.Name] = true
				ordered = append(ordered, t)
				progressed = true
				break
			}
		}
		if !progressed {
			for _, t := range s.Tables {
				if !placed[t.Name] {
					placed[t.Name] = true
					ordered = append(ordered, t)
				}
			}
		}
	}
	s.Tables = ordered
}

func referencesPlaced(s *schema.Schema, t *schema.Table, placed map[string]bool) bool {
	for _, fk := range t.ForeignKeys {
		n := fk.RefTable.Name
		if _, inSchema := s.Table(n); inSchema && n != t.Name && !placed[n] {
			return false
		}
	}
	return true
}

func canonicalize(s *schema.Schema) {
	sortTables(s)
	for _, t := range s.Tables {
		slices.SortFunc(t.Indexes, func(a, b *schema.Index) int { return strings.Compare(a.Name, b.Name) })
		slices.SortFunc(t.ForeignKeys, func(a, b *schema.ForeignKey) int { return strings.Compare(a.Symbol, b.Symbol) })
		slices.SortStableFunc(t.Attrs, func(a, b schema.Attr) int { return strings.Compare(attributeKey(a), attributeKey(b)) })
	}
	slices.SortStableFunc(s.Objects, func(a, b schema.Object) int {
		return strings.Compare(objectKey(a), objectKey(b))
	})
}

func objectKey(o schema.Object) string {
	key := fmt.Sprintf("%T", o)
	if e, ok := o.(*schema.EnumType); ok {
		key += e.T
	}
	return key
}

func attributeKey(a schema.Attr) string {
	if check, ok := a.(*schema.Check); ok {
		return "check:" + check.Name + ":" + check.Expr
	}
	return fmt.Sprintf("%T", a)
}
