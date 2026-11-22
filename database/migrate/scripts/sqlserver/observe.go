package sqlserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	catalog "github.com/flanksource/sqlglot-go/database/inspect/catalog/sqlserver"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
)

// observationEpoch holds what one observation read from the target, so every
// script observed in it sees the same catalog and function results.
type observationEpoch struct {
	q           queryer
	names       map[string]catalog.Module
	missing     map[string]error
	loaded      map[string]loadedModule
	definitions map[string]moduleSnapshot
	schemas     map[string]schemaObservation
	results     map[string]scripts.Observation
	// environments are the environment expressions observed, by SQL.
	environments map[string]scripts.Observation
}

func newObservationEpoch(q queryer) *observationEpoch {
	return &observationEpoch{q: q, names: map[string]catalog.Module{}, missing: map[string]error{}, loaded: map[string]loadedModule{},
		definitions: map[string]moduleSnapshot{}, schemas: map[string]schemaObservation{}, results: map[string]scripts.Observation{}, environments: map[string]scripts.Observation{}}
}

const (
	ownTransaction   = "its own transaction inside the migration's: if it ends that transaction, or leaves one open, it is commit-unknown and blocked until reconciled"
	untrackedDynamic = "dynamic SQL: what it reads is not tracked, so a change to it does not run the script again"
)

type observer struct {
	*observationEpoch
	visited          map[string]bool
	inputs           map[string]scripts.Observation
	modules          map[string]moduleSnapshot
	warnings         []string
	environment      []string
	explicit         bool
	nonTransactional bool
}

// observe is what s depends on, observed in epoch: its inputs, and warnings
// about what running it does that no input shows.
func (opts Options) observe(ctx context.Context, epoch *observationEpoch, s scripts.Script) *observer {
	o := &observer{observationEpoch: epoch, visited: map[string]bool{}, inputs: map[string]scripts.Observation{},
		modules: map[string]moduleSnapshot{}, explicit: s.ExplicitDependencies, nonTransactional: s.NonTransactional}
	a := opts.analysis[s.ID]
	registry := opts.Functions
	defer func() { o.reduce(s, a) }()
	if a.TransactionControl && !s.NonTransactional {
		o.warnings = append(o.warnings, "controls "+ownTransaction)
	}
	if len(registry) == 0 && len(s.Dependencies) == 0 {
		return o
	}
	if !s.ExplicitDependencies && len(a.Blocked) > 0 {
		o.fail("analysis", fmt.Errorf("explicit dependencies required: %s", strings.Join(a.Blocked, "; ")))
	}
	if !s.ExplicitDependencies && a.Dynamic {
		o.warnings = append(o.warnings, "runs "+untrackedDynamic)
	}
	if err := epoch.load(ctx, opts, opts.modulesOf(s)); err != nil {
		o.fail("catalog", err)
		return o
	}
	for _, name := range a.Calls {
		if err := o.module(name); err != nil {
			o.fail(name, err)
		}
	}
	for _, d := range s.Dependencies {
		var err error
		switch d.Kind {
		case "function-result":
			if !slices.ContainsFunc(registry, func(f Function) bool { return strings.EqualFold(f.Name, d.Name) }) {
				err = fmt.Errorf("function %s is not registered for result sampling", d.Name)
			} else {
				err = o.module(d.Name)
			}
		case "function-definition":
			err = o.module(d.Name)
		case "schema-dependency":
			err = o.schema(ctx, d.Name)
		default:
			err = fmt.Errorf("unsupported dependency kind %q", d.Kind)
		}
		if err != nil {
			o.fail(d.Name, err)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(o.modules)) {
		index := slices.IndexFunc(registry, func(f Function) bool { return strings.EqualFold(f.Name, key) })
		if index < 0 {
			continue
		}
		f := registry[index]
		registered, err := o.readModule(f.Name)
		if err != nil {
			o.fail(key, err)
			continue
		}
		if registered.Schema+"."+registered.Name != key {
			continue
		}
		if err := o.result(ctx, f, key); err != nil {
			o.fail(key, err)
		}
	}
	return o
}

// result observes registered function f, resolved to the catalog name key,
// probing it at most once per epoch.
func (o *observer) result(ctx context.Context, f Function, key string) error {
	f.Name = key
	if err := validateProbe(f, o.modules); err != nil {
		return err
	}
	if observed, ok := o.results[key]; ok {
		o.inputs["result:"+key] = observed
		return nil
	}
	quoted, err := scripts.QuoteName(key)
	if err == nil {
		err = o.probe(ctx, quoted, key, o.modules[key].Metadata, f)
	}
	if err == nil {
		o.results[key] = o.inputs["result:"+key]
	}
	return err
}

// observeKeys re-observes exactly keys in a fresh epoch on q: apply's
// targeted re-probe of the inputs an upstream write made stale. The policy
// checks of a full observation already passed when the plan was made.
func (opts Options) observeKeys(ctx context.Context, q queryer, keys []string) (map[string]scripts.Observation, error) {
	epoch := newObservationEpoch(q)
	var names []string
	for _, key := range keys {
		if kind, name, _ := strings.Cut(key, ":"); kind == "result" || kind == "definition" {
			names = append(names, name)
		}
	}
	if err := epoch.load(ctx, opts, names); err != nil {
		return nil, err
	}
	o := observer{observationEpoch: epoch, visited: map[string]bool{}, inputs: map[string]scripts.Observation{},
		modules: map[string]moduleSnapshot{}, explicit: true, nonTransactional: true}
	out := make(map[string]scripts.Observation, len(keys))
	for _, key := range keys {
		err := o.observeKey(ctx, opts.Functions, key)
		observed, ok := o.inputs[key]
		switch {
		case err != nil:
			observed = scripts.Observation{Kind: "blocked", Error: err.Error()}
		case !ok:
			observed = scripts.Observation{Kind: "blocked", Error: "input " + key + " was not observed"}
		}
		out[key] = observed
	}
	return out, nil
}

func (o *observer) observeKey(ctx context.Context, registry []Function, key string) error {
	kind, name, _ := strings.Cut(key, ":")
	switch kind {
	case "definition":
		return o.module(name)
	case "schema":
		return o.schema(ctx, name)
	case "result":
		index := slices.IndexFunc(registry, func(f Function) bool { return strings.EqualFold(f.Name, name) })
		if index < 0 {
			return fmt.Errorf("function %s is not registered for result sampling", name)
		}
		if err := o.module(name); err != nil {
			return err
		}
		return o.result(ctx, registry[index], name)
	}
	return fmt.Errorf("input %s cannot be re-observed", key)
}

// reduce keeps the inputs s's calls depend on. The sampled result of a
// registered function stands for everything inside it, so neither its
// definition, nor its callees, nor the environment they read is an input; a
// definition s declares always is.
func (o *observer) reduce(s scripts.Script, a scripts.Analysis) {
	kept := map[string]bool{}
	var keep func(name string, declared bool)
	keep = func(name string, declared bool) {
		m, ok := o.names[name]
		if !ok {
			return
		}
		key := m.Schema + "." + m.Name
		if _, sampled := o.inputs["result:"+key]; kept[key] || sampled && !declared {
			return
		}
		kept[key] = true
		snapshot := o.modules[key]
		if !declared {
			o.environment = append(o.environment, snapshot.Analysis.Environment...)
		}
		for _, ref := range snapshot.References {
			keep(ref, declared)
		}
	}
	o.environment = append(o.environment, a.Environment...)
	for _, call := range a.Calls {
		keep(call, false)
	}
	for _, d := range s.Dependencies {
		if d.Kind == "function-definition" {
			keep(d.Name, true)
		}
	}
	maps.DeleteFunc(o.inputs, func(key string, _ scripts.Observation) bool {
		name, definition := strings.CutPrefix(key, "definition:")
		return definition && !kept[name]
	})
}

// observeEnvironment observes expression, a constant call of environment
// built-ins, once per epoch.
func (e *observationEpoch) observeEnvironment(ctx context.Context, expression string) scripts.Observation {
	if observed, ok := e.environments[expression]; ok {
		return observed
	}
	observed := scripts.Observation{Kind: scripts.KindBuiltin}
	var value sql.NullString
	if err := scanOne(ctx, e.q, "SELECT CONVERT(nvarchar(4000), "+expression+")", &value); err != nil {
		observed.Error = err.Error()
	} else if value.Valid {
		observed.Value = &value.String
	}
	e.environments[expression] = observed
	return observed
}

// scanOne scans the one row query answers.
func scanOne(ctx context.Context, q queryer, query string, value any) (cause error) {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer func() { cause = errors.Join(cause, rows.Close()) }()
	if !rows.Next() {
		return errors.Join(fmt.Errorf("%s returned no row", query), rows.Err())
	}
	return rows.Scan(value)
}

func (o *observer) fail(name string, err error) {
	o.inputs["error:"+name] = scripts.Observation{Kind: "blocked", Error: err.Error()}
}

func (o *observer) probe(ctx context.Context, quoted, key string, m catalog.Module, f Function) (cause error) {
	var value sql.NullString
	rows, err := o.q.QueryContext(ctx, "SELECT "+quoted+"()")
	if err != nil {
		return fmt.Errorf("evaluate %s: %w", f.Name, err)
	}
	defer func() { cause = errors.Join(cause, rows.Close()) }()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	if len(columns) != 1 || !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return fmt.Errorf("function %s must return one row and column", f.Name)
	}
	if err := rows.Scan(&value); err != nil {
		return err
	}
	if rows.Next() || rows.NextResultSet() {
		return fmt.Errorf("function %s returned extra rows or results", f.Name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !f.Nullable && (!value.Valid || strings.TrimSpace(value.String) == "") {
		return fmt.Errorf("function %s returned NULL or an empty context", f.Name)
	}
	scalar := scripts.Scalar{Type: m.ReturnType, Length: m.Length, Precision: m.Precision, Scale: m.Scale, Null: !value.Valid, Value: value.String}
	hash, err := scalar.Fingerprint(key)
	if err != nil {
		return err
	}
	observed := scripts.Observation{Kind: scripts.KindResult, Hash: hash}
	if value.Valid {
		observed.Value = &value.String
	}
	o.inputs["result:"+key] = observed
	return nil
}
