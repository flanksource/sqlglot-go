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
	"strings"

	"github.com/flanksource/clicky/task"
	"github.com/flanksource/sqlglot-go/database/history"
	"github.com/flanksource/sqlglot-go/database/migrate/review"
	dbtarget "github.com/flanksource/sqlglot-go/database/target"
	"github.com/flanksource/sqlglot-go/migrate"
	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
)

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
	Steps     []Step           `json:"steps"`
	Objects   []Object         `json:"objects"`
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
	current   migrate.Schema
	desired   migrate.Schema
	baseline  review.Snapshot
	reference review.Snapshot
}

// plan is a planned artifact of either dialect family.
type plan struct {
	artifact any
	hash     string
	changes  []migrate.Change
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
		return Result{Dialect: s.dialect, Reference: opts.Reference, Target: opts.Target, Schemas: s.schemas, Changes: p.changes, Steps: p.steps, Objects: objects, DiffHash: hash}, nil
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
		return Patch{Dialect: s.dialect, Artifact: artifact, SQL: script(s.dialect, p.steps), Hash: p.hash, Acks: p.acks, Selection: opts.Selection}, nil
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

func (s *sides) build(ctx context.Context, selection []string) (plan, error) {
	if s.dialect == "sqlserver" {
		artifact, err := planner.BuildArtifact(migrate.ArtifactRequest{Schemas: s.schemas, Request: migrate.Request{Current: s.current, Desired: s.desired, Selection: selection}})
		if err != nil {
			return plan{}, err
		}
		p := plan{artifact: artifact, hash: artifact.Hash, changes: artifact.Plan.Changes, steps: []Step{}, acks: planner.Acknowledgements(artifact)}
		for _, step := range artifact.Plan.Steps {
			p.steps = append(p.steps, Step{Changes: []string{step.Change.ID}, SQL: step.SQL})
		}
		return p, nil
	}
	artifact, err := review.Build(ctx, s.target.DB, review.Options{Dialect: s.dialect, Schema: s.target.Schema, DesiredHCL: s.reference.HCL, Baseline: &s.baseline, Selection: selection})
	if err != nil {
		return plan{}, err
	}
	p := plan{artifact: artifact, hash: artifact.Hash, changes: artifact.Changes, steps: []Step{}, acks: artifact.Acknowledgements()}
	if p.changes == nil {
		p.changes = []migrate.Change{}
	}
	if p.acks == nil {
		p.acks = []string{}
	}
	for _, step := range artifact.Steps {
		p.steps = append(p.steps, Step{Changes: step.Changes, SQL: step.SQL})
	}
	return p, nil
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

// script joins the planned statements; SQL Server modules need their own batch.
func script(dialect string, steps []Step) string {
	var b strings.Builder
	for _, step := range steps {
		b.WriteString(strings.TrimSuffix(strings.TrimSpace(step.SQL), ";"))
		if dialect == "sqlserver" {
			b.WriteString("\nGO\n")
		} else {
			b.WriteString(";\n")
		}
	}
	return b.String()
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
