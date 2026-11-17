package sqlserver

import (
	"context"
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

func (o *observer) readModule(ctx context.Context, name string) (catalog.Module, error) {
	if m, ok := o.names[name]; ok {
		return m, nil
	}
	quoted, err := scripts.QuoteName(name)
	if err != nil {
		return catalog.Module{}, err
	}
	m, err := catalog.ReadModule(ctx, catalog.FromDB(o.q), quoted)
	if err == nil {
		o.names[name], o.names[m.Schema+"."+m.Name] = m, m
	}
	return m, err
}

func (o *observer) module(ctx context.Context, name string) error {
	m, err := o.readModule(ctx, name)
	if err != nil {
		return err
	}
	key := m.Schema + "." + m.Name
	if o.visited[key] {
		return nil
	}
	o.visited[key] = true
	quoted, err := scripts.QuoteName(key)
	if err != nil {
		return err
	}
	canonical, err := sqlglot.CanonicalSQL(m.Definition, "tsql")
	if err != nil {
		return fmt.Errorf("module %s: %w", name, err)
	}
	a := scripts.Analyze(scripts.AnalysisOptions{SQL: m.Definition, InvokedModule: true})
	if !o.explicit && len(a.Blocked) > 0 {
		return fmt.Errorf("module %s requires explicit runtime dependencies: %v", name, a.Blocked)
	}
	o.inputs["definition:"+key] = scripts.Observation{Kind: "function-definition", Hash: scripts.Digest(canonical, strconv.FormatBool(m.ANSI), strconv.FormatBool(m.Quoted))}
	refs, err := catalog.ModuleReferences(ctx, catalog.FromDB(o.q), quoted)
	if err != nil {
		return err
	}
	snapshot := moduleSnapshot{Metadata: m, Analysis: a}
	for _, ref := range refs {
		if ref.Database != "" || ref.Server != "" || !ref.Bound {
			return fmt.Errorf("module %s: unresolved or external dependency %s.%s", name, ref.Schema, ref.Name)
		}
		switch ref.Kind {
		case "FN", "P", "IF", "TF":
			snapshot.References = append(snapshot.References, ref.Schema+"."+ref.Name)
		case "U", "V":
		default:
			return fmt.Errorf("module %s has unsupported dependency %s.%s (%s)", name, ref.Schema, ref.Name, ref.Kind)
		}
	}
	for _, call := range a.Calls {
		called, err := o.readModule(ctx, call)
		if err != nil {
			return err
		}
		reference := called.Schema + "." + called.Name
		if !slices.Contains(snapshot.References, reference) {
			snapshot.References = append(snapshot.References, reference)
		}
	}
	o.modules[key] = snapshot
	for _, ref := range snapshot.References {
		if err := o.module(ctx, ref); err != nil {
			return err
		}
	}
	return nil
}
