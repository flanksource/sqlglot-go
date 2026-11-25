package dialect

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/sqlglot-go/database/migrate/dialect/postgres"
	"github.com/flanksource/sqlglot-go/database/migrate/dialect/sqlite"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// Snapshot is the complete schema of a database, as a dialect inspects it. It
// is what `inspect --snapshot` prints and what a "snapshot" migration source
// holds as the desired state.
type Snapshot struct {
	Dialect string         `json:"dialect"`
	Schemas []string       `json:"schemas"`
	Schema  migrate.Schema `json:"schema"`
	// Refusals are the objects the dialect could not read faithfully; they are
	// not in Schema.
	Refusals []engine.Refusal `json:"refusals,omitempty"`
}

// Inspect reads every object of schemas through the dialect's own inspector.
func Inspect(ctx context.Context, q engine.Querier, name string, schemas []string) (Snapshot, error) {
	d, err := engine.Lookup(name)
	if err != nil {
		return Snapshot{}, err
	}
	insp, err := d.Inspect(ctx, q, engine.Scope{Mode: engine.ScopeComplete, Schemas: schemas})
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspect %s schemas %v: %w", name, schemas, err)
	}
	return Snapshot{Dialect: name, Schemas: slices.Clone(schemas), Schema: insp.Schema, Refusals: insp.Refusals}, nil
}

// CheckWithin refuses a snapshot holding an object outside schemas.
func (s Snapshot) CheckWithin(schemas []string) error {
	if !slices.Equal(s.Schemas, schemas) {
		return fmt.Errorf("snapshot covers schemas %v, not the target's %v", s.Schemas, schemas)
	}
	for _, n := range schemaObjects(s.Schema) {
		if !slices.Contains(schemas, n.Schema) {
			return fmt.Errorf("desired object %s.%s is outside the schemas %v", n.Schema, n.Name, schemas)
		}
	}
	return nil
}

func schemaObjects(s migrate.Schema) []migrate.Name {
	var names []migrate.Name
	for _, t := range s.Tables {
		names = append(names, t.Name)
	}
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

// RefuseTouched fails when a planned change acts on an object this snapshot
// could not read. A snapshot used as the desired state leaves a refused object
// out, so planning would drop the target's copy of it.
func (s Snapshot) RefuseTouched(changes []migrate.Change) error {
	for _, r := range s.Refusals {
		i := slices.IndexFunc(changes, func(c migrate.Change) bool {
			return c.Object == r.Object && (r.Member == "" || c.Member == r.Member)
		})
		if i >= 0 {
			return fmt.Errorf("change %s touches %s.%s, which the reference snapshot refused: %s", changes[i].ID, r.Object.Schema, r.Object.Name, r.Reason)
		}
	}
	return nil
}

// Summary counts the snapshot's objects by kind; kinds with none are omitted,
// except tables.
func (s Snapshot) Summary() map[string]int {
	summary := map[string]int{"table": len(s.Schema.Tables)}
	count := func(kind string, n int) {
		if n > 0 {
			summary[kind] += n
		}
	}
	count("sequence", len(s.Schema.Sequences))
	count("table-type", len(s.Schema.TableTypes))
	for _, d := range s.Schema.Definitions {
		count(strings.ToLower(d.Kind), 1)
	}
	return summary
}

var terminators = map[string]string{
	sqlServer:     "\nGO\n", // module DDL must be first in its batch
	postgres.Name: ";\n",
	sqlite.Name:   ";\n",
}

// Script joins statements into one script of the dialect.
func Script(name string, statements []string) (string, error) {
	end, ok := terminators[name]
	if !ok {
		return "", fmt.Errorf("dialect %q has no script format", name)
	}
	var b strings.Builder
	for _, s := range statements {
		b.WriteString(strings.TrimSuffix(strings.TrimSpace(s), ";"))
		b.WriteString(end)
	}
	return b.String(), nil
}
