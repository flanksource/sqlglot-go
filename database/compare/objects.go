package compare

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// Object is one changed schema object with its definition on each side:
// module SQL, a JSON table snapshot (SQL Server) or an HCL block (Postgres,
// SQLite). Before is empty for an added object and After for a deleted one.
type Object struct {
	Kind     string       `json:"kind"`
	Name     migrate.Name `json:"name"`
	Status   string       `json:"status"`
	Before   string       `json:"before,omitempty"`
	After    string       `json:"after,omitempty"`
	Language string       `json:"language"`
	Changes  []string     `json:"changes"`
}

// definitions maps object kind and name to its rendered definition per side.
type definitions map[string]map[migrate.Name]string

func (s *sides) objects(changes []migrate.Change) ([]Object, error) {
	before, after, language, err := s.definitions()
	if err != nil {
		return nil, err
	}
	objects := []Object{}
	index := map[string]int{}
	for _, c := range changes {
		kind := objectKind(c)
		key := kind + "\x00" + c.Object.Schema + "\x00" + c.Object.Name
		i, ok := index[key]
		if !ok {
			o := Object{Kind: kind, Name: c.Object, Before: before[kind][c.Object], After: after[kind][c.Object], Language: language, Changes: []string{}}
			if slices.Contains([]string{"procedure", "function", "view", "trigger"}, kind) {
				o.Language = "sql"
			}
			switch {
			case o.Before == "" && o.After == "":
				return nil, fmt.Errorf("changed %s %s.%s is on neither side", kind, c.Object.Schema, c.Object.Name)
			case o.Before == "":
				o.Status = "added"
			case o.After == "":
				o.Status = "deleted"
			default:
				o.Status = "changed"
			}
			i, index[key] = len(objects), len(objects)
			objects = append(objects, o)
		}
		objects[i].Changes = append(objects[i].Changes, c.ID)
	}
	return objects, nil
}

// objectKind folds table members into their table.
func objectKind(c migrate.Change) string {
	switch c.Kind {
	case "column", "index", "primary-key", "foreign-key", "check", "attribute":
		return "table"
	}
	return c.Kind
}

// definitions renders both sides' objects. A schema that carries its dialect's
// own HCL shows its tables and enum types as the HCL blocks; any other shows
// each object as JSON. Modules and the objects the HCL does not hold show as SQL.
func (s *sides) definitions() (before, after definitions, language string, err error) {
	language = "json"
	if s.current.Schema.Native != "" || s.reference.Schema.Native != "" {
		language = "hcl"
	}
	if before, err = schemaDefinitions(s.current.Schema, firstSchema(s.schemas)); err != nil {
		return nil, nil, "", err
	}
	after, err = schemaDefinitions(s.reference.Schema, firstSchema(s.schemas))
	return before, after, language, err
}

func firstSchema(schemas []string) string {
	if len(schemas) == 0 {
		return ""
	}
	return schemas[0]
}

func schemaDefinitions(schema migrate.Schema, schemaName string) (definitions, error) {
	d := definitions{}
	if schema.Native != "" {
		var err error
		if d, err = hclDefinitions(schema.Native, schemaName); err != nil {
			return nil, err
		}
	} else if err := d.putJSON(schema); err != nil {
		return nil, err
	}
	for _, def := range schema.Definitions {
		if kind := strings.ToLower(def.Kind); d[kind][def.Name] == "" {
			d.set(kind, def.Name, def.SQL)
		}
	}
	return d, nil
}

// putJSON adds every table, sequence and table type as indented JSON.
func (d definitions) putJSON(schema migrate.Schema) error {
	put := func(kind string, name migrate.Name, value any) error {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		d.set(kind, name, string(data))
		return nil
	}
	for _, t := range schema.Tables {
		if err := put("table", t.Name, t); err != nil {
			return err
		}
	}
	for _, q := range schema.Sequences {
		if err := put("sequence", q.Name, q); err != nil {
			return err
		}
	}
	for _, t := range schema.TableTypes {
		if err := put("table-type", t.Name, t); err != nil {
			return err
		}
	}
	return nil
}

// hclDefinitions slices each table and enum block out of a native snapshot.
func hclDefinitions(text, schemaName string) (definitions, error) {
	file, diags := hclsyntax.ParseConfig([]byte(text), "snapshot.hcl", hcl.InitialPos)
	if diags.HasErrors() {
		return nil, fmt.Errorf("parse snapshot: %s", diags.Error())
	}
	d := definitions{}
	for _, block := range file.Body.(*hclsyntax.Body).Blocks {
		kind := block.Type
		if len(block.Labels) != 1 || kind != "table" && kind != "enum" && kind != "schema" {
			continue
		}
		r := block.Range()
		d.set(kind, migrate.Name{Schema: schemaName, Name: block.Labels[0]}, text[r.Start.Byte:r.End.Byte])
	}
	return d, nil
}

func (d definitions) set(kind string, name migrate.Name, text string) {
	if d[kind] == nil {
		d[kind] = map[migrate.Name]string{}
	}
	d[kind][name] = text
}
