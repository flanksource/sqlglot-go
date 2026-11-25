// Package compare diffs the schemas of two live connections and turns a
// reviewed selection of the differences into an applyable patch. Every
// comparison and patch is one tracked run, recorded to history with its result.
package compare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/flanksource/clicky/task"
	"github.com/flanksource/sqlglot-go/database/history"
	dialects "github.com/flanksource/sqlglot-go/database/migrate/dialect"
	dbtarget "github.com/flanksource/sqlglot-go/database/target"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// actionRebuild is the action of a change that drops and re-creates a
// dependent object around a change the diff lists.
const actionRebuild = "rebuild"

// Options names the two sides. Reference is the desired schema; Target is
// the database the patch brings in line with it.
type Options struct {
	Reference string   `json:"reference"`
	Target    string   `json:"target"`
	Schemas   []string `json:"schemas,omitempty"`
	// Run names this run: its clicky group id and its history record id.
	Run string `json:"run"`
	// ConnectionsDir resolves connection:// references.
	ConnectionsDir string `json:"-"`
	// History records the run and its result.
	History *history.Store `json:"-"`
}

type Step struct {
	Changes []string `json:"changes"`
	SQL     string   `json:"sql"`
}

type Result struct {
	Dialect   string           `json:"dialect"`
	Reference string           `json:"reference"`
	Target    string           `json:"target"`
	Schemas   []string         `json:"schemas"`
	Changes   []migrate.Change `json:"changes"`
	// Rebuilds are the dependents the changes force to be dropped and
	// re-created; each names its change in Cause and cannot be selected alone.
	Rebuilds []migrate.Change `json:"rebuilds,omitempty"`
	Steps    []Step           `json:"steps"`
	Objects  []Object         `json:"objects"`
	// DiffHash binds a patch to the exact differences that were reviewed.
	DiffHash string `json:"diffHash"`
}

type PatchOptions struct {
	Options
	Selection []string `json:"select"`
	DiffHash  string   `json:"diffHash"`
	// Comparison is the recorded comparison run the selection was reviewed in,
	// when there is one.
	Comparison string `json:"comparison,omitempty"`
}

type Patch struct {
	Dialect   string          `json:"dialect"`
	Artifact  json.RawMessage `json:"artifact"`
	SQL       string          `json:"sql"`
	Hash      string          `json:"hash"`
	Acks      []string        `json:"acks"`
	Selection []string        `json:"selection"`
}

// sides holds both inspected schemas and the target connection to plan with.
type sides struct {
	dialect   string
	schemas   []string
	target    dbtarget.Connection
	current   dialects.Snapshot
	reference dialects.Snapshot
}

// plan is a planned artifact, split into what the diff lists and what the
// engine adds to it.
type plan struct {
	artifact engine.Artifact
	changes  []migrate.Change
	rebuilds []migrate.Change
	steps    []Step
	acks     []string
}

// Compare inspects both sides and plans every difference.
func Compare(ctx context.Context, opts Options) (Result, error) {
	r, err := startRun(ctx, opts, history.KindComparison)
	if err != nil {
		return Result{}, err
	}
	planned := r.add("Plan all differences", func(ctx context.Context, t *task.Task) (any, error) {
		s, err := r.sides()
		if err != nil {
			return nil, err
		}
		p, err := s.planned(ctx, t, nil)
		if err != nil {
			return nil, err
		}
		objects, err := s.objects(p.changes)
		if err != nil {
			return nil, err
		}
		hash, err := diffHash(p)
		if err != nil {
			return nil, err
		}
		return Result{Dialect: s.dialect, Reference: opts.Reference, Target: opts.Target, Schemas: s.schemas, Changes: p.changes, Rebuilds: p.rebuilds, Steps: p.steps, Objects: objects, DiffHash: hash}, nil
	}, r.reference.Task, r.target.Task)
	result, err := result[Result](planned)
	recorded := result
	recorded.Reference, recorded.Target = history.Redact(result.Reference), history.Redact(result.Target)
	return recorded, r.finish(ctx, finish{value: recorded, summary: comparisonSummary(result), err: err})
}

// BuildPatch re-inspects both sides, refuses when the differences changed
// since they were reviewed, and plans only the selected changes.
func BuildPatch(ctx context.Context, opts PatchOptions) (Patch, error) {
	if opts.DiffHash == "" {
		return Patch{}, fmt.Errorf("reviewed diff hash is required")
	}
	if len(opts.Selection) == 0 {
		return Patch{}, fmt.Errorf("select at least one change")
	}
	r, err := startRun(ctx, opts.Options, history.KindPatch)
	if err != nil {
		return Patch{}, err
	}
	checked := r.add("Check for drift since review", func(ctx context.Context, t *task.Task) (any, error) {
		s, err := r.sides()
		if err != nil {
			return nil, err
		}
		full, err := s.planned(ctx, t, nil)
		if err != nil {
			return nil, err
		}
		hash, err := diffHash(full)
		if err != nil {
			return nil, err
		}
		if hash != opts.DiffHash {
			return nil, fmt.Errorf("schema drift: the differences between %s and %s changed since review; compare again", history.Redact(opts.Reference), history.Redact(opts.Target))
		}
		return s, nil
	}, r.reference.Task, r.target.Task)
	planned := r.add(fmt.Sprintf("Plan %d selected changes", len(opts.Selection)), func(ctx context.Context, t *task.Task) (any, error) {
		s, err := result[*sides](checked)
		if err != nil {
			return nil, err
		}
		p, err := s.planned(ctx, t, opts.Selection)
		if err != nil {
			return nil, err
		}
		artifact, err := json.Marshal(p.artifact)
		if err != nil {
			return nil, err
		}
		sql, err := dialects.Script(s.dialect, p.statements())
		if err != nil {
			return nil, err
		}
		return Patch{Dialect: s.dialect, Artifact: artifact, SQL: sql, Hash: p.artifact.Hash, Acks: p.acks, Selection: opts.Selection}, nil
	}, checked.Task)
	patch, err := result[Patch](planned)
	return patch, r.finish(ctx, finish{parent: opts.Comparison, value: patch, summary: map[string]int{"selected": len(patch.Selection), "acks": len(patch.Acks)}, err: err})
}

// planned plans the selected changes, or every difference when selection is
// nil, reporting the size of the plan on t.
func (s *sides) planned(ctx context.Context, t *task.Task, selection []string) (plan, error) {
	t.SetProgress(0, 0)
	p, err := s.build(ctx, selection)
	if err == nil {
		t.SetDescription(fmt.Sprintf("%d changes in %d steps", len(p.changes), len(p.steps)))
	}
	return p, err
}

// build plans the target toward the reference in complete scope, so the
// reference's absence of an object is a drop that must be acknowledged. The
// engine reads the target again, which is what makes the artifact's baseline
// the database the patch is applied to.
func (s *sides) build(ctx context.Context, selection []string) (plan, error) {
	artifact, err := engine.PlanLive(ctx, s.target.DB, engine.PlanLiveRequest{
		Dialect: s.dialect, Scope: engine.Scope{Mode: engine.ScopeComplete, Schemas: s.schemas}, Desired: s.reference.Schema, Selection: selection,
	})
	if err != nil {
		return plan{}, err
	}
	if err := s.reference.RefuseTouched(artifact.Changes); err != nil {
		return plan{}, err
	}
	return newPlan(artifact), nil
}

// newPlan lists the changes the diff names, the rebuilds the engine added, and
// the statements in execution order. A rebuild's statements are listed under
// the change that forced it, which is the one a reviewer selects.
func newPlan(a engine.Artifact) plan {
	p := plan{artifact: a, changes: []migrate.Change{}, steps: []Step{}, acks: engine.Acknowledgements(a)}
	cause := map[string]string{}
	for _, c := range a.Changes {
		if c.Action != actionRebuild {
			p.changes = append(p.changes, c)
			continue
		}
		p.rebuilds = append(p.rebuilds, c)
		cause[c.ID] = c.Cause
	}
	for _, u := range a.Units {
		for _, op := range u.Ops {
			id := op.Change
			if forced, ok := cause[id]; ok {
				id = forced
			}
			for _, s := range op.Statements {
				p.steps = append(p.steps, Step{Changes: []string{id}, SQL: s.SQL})
			}
		}
	}
	return p
}

func (p plan) statements() []string {
	out := make([]string, len(p.steps))
	for i, s := range p.steps {
		out[i] = s.SQL
	}
	return out
}

func diffHash(p plan) (string, error) {
	data, err := json.Marshal(struct {
		Changes []migrate.Change `json:"changes"`
		Steps   []Step           `json:"steps"`
	}{p.changes, p.steps})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
