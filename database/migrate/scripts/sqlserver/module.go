package sqlserver

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	catalog "github.com/flanksource/sqlglot-go/database/inspect/catalog/sqlserver"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	"github.com/flanksource/sqlglot-go/sqlglot"
)

type moduleSnapshot struct {
	Metadata   catalog.Module
	Analysis   scripts.Analysis
	References []string
	Hash       string
}

func validateProbe(f Function, modules map[string]moduleSnapshot) error {
	if _, err := (scripts.Scalar{Type: f.Type}).Fingerprint(f.Name); err != nil {
		return err
	}
	m, ok := modules[f.Name]
	if !ok || m.Metadata.Kind != "FN" || m.Metadata.Parameters != 0 {
		return fmt.Errorf("tracked function %s must be a visible zero-argument T-SQL scalar", f.Name)
	}
	if !strings.EqualFold(f.Type, m.Metadata.ReturnType) {
		return fmt.Errorf("function %s: expected type %s, got %s", f.Name, f.Type, m.Metadata.ReturnType)
	}
	visited := map[string]bool{}
	var visit func(string) error
	visit = func(key string) error {
		if visited[key] {
			return nil
		}
		visited[key] = true
		snapshot, ok := modules[key]
		if !ok {
			return fmt.Errorf("function %s: unresolved dependency %s", f.Name, key)
		}
		if len(snapshot.Analysis.Blocked) > 0 {
			return fmt.Errorf("function %s: cannot analyze %s: %v", f.Name, key, snapshot.Analysis.Blocked)
		}
		if len(snapshot.Analysis.Unresolved) > 0 {
			return fmt.Errorf("function %s has unclassified calls in %s: %v", f.Name, key, snapshot.Analysis.Unresolved)
		}
		for _, builtin := range snapshot.Analysis.Builtins {
			if builtin.Dependency != sqlglot.DependencyEnvironment {
				return fmt.Errorf("function %s uses unstable builtin %s in %s", f.Name, builtin.Name, key)
			}
		}
		for _, ref := range snapshot.References {
			if err := visit(ref); err != nil {
				return err
			}
		}
		return nil
	}
	return visit(f.Name)
}

// readModule answers from what the epoch loaded; a name it never loaded is a
// broken invariant, not a reason to query.
func (o *observer) readModule(name string) (catalog.Module, error) {
	if m, ok := o.names[name]; ok {
		return m, nil
	}
	if err, ok := o.missing[name]; ok {
		return catalog.Module{}, err
	}
	return catalog.Module{}, fmt.Errorf("module %s was not loaded for this observation", name)
}

func (o *observer) module(name string) error {
	m, err := o.readModule(name)
	if err != nil {
		return err
	}
	key := m.Schema + "." + m.Name
	if o.visited[key] {
		return nil
	}
	o.visited[key] = true
	snapshot, err := o.moduleDefinition(key, m)
	if err != nil {
		return err
	}
	if !o.explicit && len(snapshot.Analysis.Blocked) > 0 {
		return fmt.Errorf("module %s requires explicit runtime dependencies: %v", name, snapshot.Analysis.Blocked)
	}
	if !o.nonTransactional && snapshot.Analysis.TransactionControl {
		return fmt.Errorf("module %s controls its own transaction; review this script as nontransactional", key)
	}
	o.inputs["definition:"+key] = scripts.Observation{Kind: "function-definition", Hash: snapshot.Hash}
	o.modules[key] = snapshot
	for _, ref := range snapshot.References {
		if err := o.module(ref); err != nil {
			return err
		}
	}
	return nil
}

func (o *observer) moduleDefinition(key string, m catalog.Module) (moduleSnapshot, error) {
	if definition, exists := o.definitions[key]; exists {
		return definition, nil
	}
	loaded, ok := o.loaded[key]
	if !ok {
		return moduleSnapshot{}, fmt.Errorf("module %s was not loaded for this observation", key)
	}
	if loaded.err != nil {
		return moduleSnapshot{}, loaded.err
	}
	a := loaded.analysis.Analysis
	snapshot := moduleSnapshot{Metadata: m, Analysis: a, Hash: scripts.Digest(loaded.analysis.Canonical, strconv.FormatBool(m.ANSI), strconv.FormatBool(m.Quoted))}
	for _, ref := range loaded.refs {
		if ref.Database != "" || ref.Server != "" || !ref.Bound {
			return moduleSnapshot{}, fmt.Errorf("module %s: unresolved or external dependency %s.%s", key, ref.Schema, ref.Name)
		}
		switch ref.Kind {
		case "FN", "P", "IF", "TF":
			snapshot.References = append(snapshot.References, ref.Schema+"."+ref.Name)
		case "U", "V":
		default:
			return moduleSnapshot{}, fmt.Errorf("module %s has unsupported dependency %s.%s (%s)", key, ref.Schema, ref.Name, ref.Kind)
		}
	}
	for _, call := range a.Calls {
		called, err := o.readModule(call)
		if err != nil {
			return moduleSnapshot{}, err
		}
		reference := called.Schema + "." + called.Name
		if !slices.Contains(snapshot.References, reference) {
			snapshot.References = append(snapshot.References, reference)
		}
	}
	o.definitions[key] = snapshot
	return snapshot, nil
}
