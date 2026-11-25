package sqlserver

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
)

func prepare(ctx context.Context, opts Options) (Options, error) {
	if opts.DB == nil || strings.TrimSpace(opts.Scope) == "" || len(opts.Scope) > 256 {
		return Options{}, fmt.Errorf("script database and scope (up to 256 characters) are required")
	}
	if err := validateAbsorbed(opts); err != nil {
		return Options{}, err
	}
	if err := validateSchema(opts.Schema); err != nil {
		return Options{}, err
	}
	encoded, err := json.Marshal(struct {
		Scripts   []scripts.Script
		Functions []Function
	}{opts.Scripts, opts.Functions})
	if err != nil {
		return Options{}, err
	}
	var copy struct {
		Scripts   []scripts.Script
		Functions []Function
	}
	if err := json.Unmarshal(encoded, &copy); err != nil {
		return Options{}, err
	}
	opts.Scripts, opts.Functions = copy.Scripts, copy.Functions
	slices.SortFunc(opts.Functions, func(a, b Function) int { return strings.Compare(a.Name, b.Name) })
	for i := range opts.Functions {
		slices.Sort(opts.Functions[i].Prerequisites)
	}
	registryJSON, err := json.Marshal(opts.Functions)
	if err != nil {
		return Options{}, err
	}
	registered := map[string]bool{}
	for _, f := range opts.Functions {
		if _, err := scripts.QuoteName(f.Name); err != nil {
			return Options{}, err
		}
		if f.Type == "" || registered[strings.ToLower(f.Name)] {
			return Options{}, fmt.Errorf("invalid or duplicate registered function %q", f.Name)
		}
		registered[strings.ToLower(f.Name)] = true
	}
	if opts.analysis, err = opts.scriptAnalyses(ctx); err != nil {
		return Options{}, err
	}
	for i := range opts.Scripts {
		opts.prepareScript(&opts.Scripts[i], scripts.SourceHash(string(registryJSON)))
	}
	opts.Scripts, err = scripts.Ordered(opts.Scripts)
	return opts, err
}

// prepareScript sets s's effects and definitions from its analysis, adds the
// prerequisites of the registered functions s calls and sorts its
// declarations, so preparing a prepared script changes nothing.
func (opts Options) prepareScript(s *scripts.Script, contextPolicy string) {
	analysis := opts.analysis[s.ID]
	s.NoStatements = analysis.NoStatements
	effects := analysis.Effects.Normalized()
	s.Effects, s.Defines, s.ContextPolicy = &effects, analysis.Defines, contextPolicy
	calls := slices.Clone(analysis.Calls)
	for _, d := range s.Dependencies {
		if d.Kind == "function-result" {
			calls = append(calls, d.Name)
		}
	}
	for _, f := range opts.Functions {
		if !slices.ContainsFunc(calls, func(call string) bool { return strings.EqualFold(call, f.Name) }) {
			continue
		}
		for _, id := range f.Prerequisites {
			if !slices.Contains(s.DependsOn, id) && id != s.ID {
				s.DependsOn = append(s.DependsOn, id)
			}
		}
	}
	slices.Sort(s.DependsOn)
	slices.SortFunc(s.Dependencies, func(a, b scripts.Dependency) int {
		if order := strings.Compare(a.Kind, b.Kind); order != 0 {
			return order
		}
		return strings.Compare(a.Name, b.Name)
	})
}

// Plan observes the target once, without creating metadata or running
// anything, and returns the plan file Apply runs: every script of the bundle,
// pre scripts before post scripts, planned in one pass, and the schema step
// between them. What runs is decided here and nowhere else: a script is selected
// for its own source, inputs and reads as they are now, never for what an
// earlier script in the plan will write.
func Plan(ctx context.Context, opts Options) (PlanFile, error) {
	for _, s := range opts.Scripts {
		if s.Effects != nil || len(s.Defines) > 0 || s.NoStatements {
			return PlanFile{}, fmt.Errorf("script %s supplies analysis: Effects, Defines and NoStatements are derived from its SQL", s.ID)
		}
	}
	prepared, err := prepare(ctx, opts)
	if err != nil {
		return PlanFile{}, err
	}
	return plan(ctx, prepared.DB, prepared)
}

func plan(ctx context.Context, q queryer, opts Options) (PlanFile, error) {
	if opts.AbsorbScopes != nil {
		absorbed, err := absorbableScopes(ctx, q, opts)
		if err != nil {
			return PlanFile{}, err
		}
		opts.absorbed = absorbed
	}
	target, err := observeTarget(ctx, q, opts)
	if err != nil {
		return PlanFile{}, err
	}
	file := PlanFile{Kind: PlanFileKind, Version: PlanFileVersion, Ledger: target.states, Absorbed: opts.absorbed,
		Target: PlanTarget{Server: target.server, Database: target.database, Scope: opts.Scope},
		Bundle: PlanBundle{Scripts: opts.Scripts, Functions: opts.Functions, Schema: opts.Schema}}
	built, err := scripts.Build(scripts.PlanOptions{Target: file.Target.String(), Scripts: opts.Scripts, Recorded: target.states,
		Observations: target.observations, Modules: target.modules, SchemaWrites: schemaWrites(opts.Schema),
		Reads: target.reads, UntrackedReads: target.untracked, Answers: target.answers})
	if err != nil {
		return PlanFile{}, err
	}
	file.Actions, file.Observations = built.Scripts, observedInputs(built.Scripts)
	file.Warnings = slices.Concat(opts.Warnings, built.Warnings, planWarnings(file, target.warnings))
	return file.seal()
}

// observedInputs is every observed input of the plan; one observation
// observes each key once, so scripts sharing a key agree on it.
func observedInputs(actions []scripts.Pending) map[string]string {
	values := map[string]string{}
	for _, item := range actions {
		for key, hash := range item.State.Inputs {
			if !strings.HasPrefix(key, "script:") {
				values[key] = hash
			}
		}
	}
	return values
}

// planWarnings warns about each selected script; observed are the warnings its
// observation found, by script ID.
func planWarnings(file PlanFile, observed map[string][]string) []string {
	var out []string
	declared := schemaWrites(file.Bundle.Schema)
	for _, item := range file.Actions {
		if item.Script.NoStatements {
			out = append(out, fmt.Sprintf("SQL script %s has no statements; skipped", cmp.Or(item.Script.Path, item.Script.ID)))
		}
		if !item.Selected {
			continue
		}
		for _, warning := range observed[item.Script.ID] {
			out = append(out, fmt.Sprintf("script %s (%s) %s", item.Script.Name, item.Script.ID, warning))
		}
		if item.Script.NonTransactional {
			out = append(out, fmt.Sprintf("script %s runs outside a transaction: a failure leaves it partial, blocked until reconciled", item.Script.ID))
		}
		if item.Script.Phase != scripts.Pre || item.Script.Effects == nil {
			continue
		}
		for _, object := range item.Script.Effects.Writes {
			if slices.Contains(declared, object) {
				out = append(out, fmt.Sprintf("pre script %s writes declared object %s: apply refuses the schema step if it changes it", item.Script.ID, object))
			}
		}
	}
	return out
}

// observedTarget is what one observation read from the target: its identity,
// the ledger of the scope and every script's observed inputs and warnings.
type observedTarget struct {
	server, database string
	states           map[string]scripts.State
	observations     map[string]map[string]scripts.Observation
	warnings         map[string][]string
	modules          map[scripts.ObjectName]scripts.Effects
	reads            map[string][]scripts.Read
	untracked        map[string][]scripts.ObjectName
	answers          map[string]scripts.ReadAnswer
}

func observeTarget(ctx context.Context, q queryer, opts Options) (observedTarget, error) {
	var t observedTarget
	var err error
	if t.server, t.database, err = readIdentity(ctx, q); err != nil {
		return t, err
	}
	if t.states, err = readStates(ctx, q, opts.ledger()); err != nil {
		return t, err
	}
	t.observations = map[string]map[string]scripts.Observation{}
	t.warnings = map[string][]string{}
	epoch := newObservationEpoch(q)
	var modules []string
	for _, s := range opts.Scripts {
		if s.NoStatements {
			continue
		}
		modules = append(append(modules, opts.modulesOf(s)...), closureModules(s)...)
	}
	if err := epoch.load(ctx, opts, modules); err != nil {
		return t, err
	}
	for _, s := range opts.Scripts {
		if s.NoStatements {
			continue
		}
		o := opts.observe(ctx, epoch, s)
		t.observations[s.ID], t.warnings[s.ID] = o.inputs, o.warnings
		for _, expression := range o.environment {
			o.inputs[scripts.KindBuiltin+":"+expression] = epoch.observeEnvironment(ctx, expression)
		}
	}
	t.modules = epoch.moduleEffects()
	t.reads, t.untracked = map[string][]scripts.Read{}, map[string][]scripts.ObjectName{}
	for id, analysis := range opts.analysis {
		t.reads[id], t.untracked[id] = analysis.Reads, analysis.UntrackedReads
	}
	t.answers, err = answerReads(ctx, q, opts.comparableReads(t.states))
	return t, err
}

func readIdentity(ctx context.Context, q queryer) (server, database string, err error) {
	rows, err := q.QueryContext(ctx, `SELECT CONVERT(nvarchar(128),SERVERPROPERTY('ServerName')),DB_NAME()`)
	if err != nil {
		return "", "", err
	}
	if !rows.Next() {
		return "", "", errors.Join(fmt.Errorf("target identity returned no row"), rows.Err(), rows.Close())
	}
	if err := rows.Scan(&server, &database); err != nil {
		return "", "", errors.Join(err, rows.Close())
	}
	return server, database, rows.Close()
}
