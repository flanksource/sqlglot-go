package sqlserver

import (
	"context"
	"fmt"
	"maps"
	"slices"

	catalog "github.com/flanksource/sqlglot-go/database/inspect/catalog/sqlserver"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
)

// loadedModule is what the epoch read about one module: its cached analysis
// and catalog references, or why its definition cannot be used.
type loadedModule struct {
	analysis scripts.CachedAnalysis
	refs     []catalog.ModuleReference
	err      error
}

// modulesOf names every module observing s may read: its calls, its function
// dependencies and the registered functions. A script observe skips reads none.
func (opts Options) modulesOf(s scripts.Script) []string {
	if len(opts.Functions) == 0 && len(s.Dependencies) == 0 {
		return nil
	}
	names := slices.Clone(opts.analysis[s.ID].Calls)
	for _, d := range s.Dependencies {
		if d.Kind == "function-result" || d.Kind == "function-definition" {
			names = append(names, d.Name)
		}
	}
	for _, f := range opts.Functions {
		names = append(names, f.Name)
	}
	return names
}

// closureModules names the routines s's effects call, directly or from a
// routine it defines, so the plan can follow them into the catalog.
func closureModules(s scripts.Script) []string {
	var names []string
	add := func(effects scripts.Effects) {
		for _, call := range effects.Calls {
			names = append(names, string(call))
		}
	}
	if s.Effects != nil {
		add(*s.Effects)
	}
	for _, effects := range s.Defines {
		add(effects)
	}
	return names
}

// load reads the module graph reachable from names level by level, so its
// cost follows the graph's depth, not the number of scripts: each level is one
// ReadModules over the unseen names, one ReadModuleReferences over the modules
// found and one analysis-cache read over their definitions. A module that is
// missing or unusable is recorded for the scripts that need it; only a failed
// query is an error.
func (e *observationEpoch) load(ctx context.Context, opts Options, names []string) error {
	for frontier := e.unseen(names); len(frontier) > 0; {
		found, err := e.readModules(ctx, frontier)
		if err != nil {
			return err
		}
		if frontier, err = e.loadDefinitions(ctx, opts, found); err != nil {
			return err
		}
	}
	return nil
}

func (e *observationEpoch) unseen(names []string) []string {
	var out []string
	for _, name := range names {
		_, known := e.names[name]
		_, missing := e.missing[name]
		if !known && !missing && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// readModules resolves names and returns the modules not loaded before, by
// schema-qualified key.
func (e *observationEpoch) readModules(ctx context.Context, names []string) (map[string]catalog.Module, error) {
	quoted := make(map[string]string, len(names))
	for _, name := range names {
		q, err := scripts.QuoteName(name)
		if err != nil {
			e.missing[name] = err
			continue
		}
		quoted[name] = q
	}
	modules, err := catalog.ReadModules(ctx, catalog.FromDB(e.q), slices.Sorted(maps.Values(quoted)))
	if err != nil {
		return nil, err
	}
	found := map[string]catalog.Module{}
	for name, q := range quoted {
		m, ok := modules[q]
		switch {
		case !ok:
			e.missing[name] = fmt.Errorf("module %s not found or not visible", q)
		case m.Unavailable:
			e.missing[name] = fmt.Errorf("module %s definition is missing, encrypted or unavailable", q)
		default:
			key := m.Schema + "." + m.Name
			e.names[name], e.names[key] = m, m
			if _, done := e.loaded[key]; !done {
				found[key] = m
			}
		}
	}
	return found, nil
}

// loadDefinitions reads the references and analyses of found and returns the
// next level: the routines they reference or call that the epoch has not seen.
func (e *observationEpoch) loadDefinitions(ctx context.Context, opts Options, found map[string]catalog.Module) ([]string, error) {
	quoted := map[string]string{}
	bodies := map[string]string{}
	for key, m := range found {
		q, err := scripts.QuoteName(key)
		if err != nil {
			e.loaded[key] = loadedModule{err: err}
			continue
		}
		quoted[key] = q
		bodies[scripts.AnalysisKey(m.Definition, true)] = m.Definition
	}
	refs, err := catalog.ReadModuleReferences(ctx, catalog.FromDB(e.q), slices.Sorted(maps.Values(quoted)))
	if err != nil {
		return nil, err
	}
	analyses, failed, err := cachedBodies(ctx, opts, bodies, analyseModule)
	if err != nil {
		return nil, err
	}
	var next []string
	for key, q := range quoted {
		analysisKey := scripts.AnalysisKey(found[key].Definition, true)
		if err := failed[analysisKey]; err != nil {
			e.loaded[key] = loadedModule{err: fmt.Errorf("module %s: %w", key, err)}
			continue
		}
		loaded := loadedModule{analysis: analyses[analysisKey], refs: refs[q]}
		e.loaded[key] = loaded
		next = append(next, loaded.analysis.Analysis.Calls...)
		for _, ref := range loaded.refs {
			if ref.Bound && ref.Server == "" && ref.Database == "" && slices.Contains([]string{"FN", "P", "IF", "TF"}, ref.Kind) {
				next = append(next, ref.Schema+"."+ref.Name)
			}
		}
	}
	return e.unseen(next), nil
}
