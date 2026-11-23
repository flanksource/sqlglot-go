package sqlserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
)

type Result struct {
	// Outcome is applied, already-applied, stale, or the worst outcome of the
	// scripts that failed (failed, partial or commit-unknown). A completed
	// apply with tolerated script failures is applied-with-warnings.
	Outcome string   `json:"outcome"`
	Applied []string `json:"applied"`
	// Schema is applied when the schema step ran statements.
	Schema string `json:"schema,omitempty"`
	// Blocked lists the scripts the plan left blocked.
	Blocked []string `json:"blocked,omitempty"`
	// Failed are the scripts that failed; Skipped the selected ones that did
	// not run because of them.
	Failed   []ScriptFailure `json:"failed,omitempty"`
	Skipped  []SkippedScript `json:"skipped,omitempty"`
	Warnings []ScriptFailure `json:"warnings,omitempty"`
}

// Apply runs file on db: it verifies the file's integrity and target, re-plans
// its bundle against the target and refuses the file with a StalePlanError
// when the ledger, an observed input or a selection moved since, before
// anything runs. It then runs the selected pre scripts, the schema step, any
// WithBarrier step and the selected post scripts, on one locked connection.
// An input an upstream script in the plan writes is re-observed once after
// that writer; every other input holds its planned value. A script with
// unknown effects is followed by a re-plan, and work the file did not plan
// stops the apply with a StalePlanError: the scripts it already committed stay
// recorded, and a new plan covers the rest. A script that fails does not stop
// the apply: the scripts that directly depend on it are skipped, the rest run,
// and Apply returns every failure. It does stop when the session is lost
// and, after a pre script failed, before the schema step or a barrier.
func Apply(ctx context.Context, db *sql.DB, file PlanFile, options ...ApplyOption) (result Result, returnErr error) {
	result = Result{Outcome: "failed", Applied: []string{}}
	cfg := resolveApply(options)
	opts, err := file.options(ctx, db, cfg)
	if err != nil {
		return result, err
	}
	s, err := open(ctx, opts)
	if err != nil {
		return result, err
	}
	defer func() { returnErr = errors.Join(returnErr, s.Close()) }()
	result, err = s.apply(ctx, file, cfg)
	var stale *StalePlanError
	if errors.As(err, &stale) {
		result.Outcome = "stale"
	}
	return result, err
}

func (s *session) apply(ctx context.Context, file PlanFile, cfg applyConfig) (Result, error) {
	result := Result{Outcome: "failed", Applied: []string{}}
	if err := cfg.report(ProgressCheck, "", func() error { return s.verify(ctx, file) }); err != nil {
		return result, err
	}
	if err := s.absorb(ctx); err != nil {
		return result, err
	}
	run := applyRun{s: s, file: file, cfg: cfg, stale: map[string]bool{}, recorded: map[string]string{}, unmet: map[string]bool{}, values: map[string]*string{}}
	for _, item := range file.Actions {
		if len(item.Blocked) > 0 {
			result.Blocked = append(result.Blocked, item.Script.ID)
		}
	}
	if len(file.Selected()) > 0 {
		if err := ensureStore(ctx, s.conn); err != nil {
			return result, err
		}
		run.snapshot = newSnapshot(s.opts.Scope, file)
		if err := run.snapshot.write(ctx, s.conn); err != nil {
			return result, err
		}
		if _, err := s.conn.ExecContext(ctx, setLockTimeout); err != nil {
			return result, err
		}
	}
	if err := run.phase(ctx, &result, scripts.Pre); err != nil {
		return result, err
	}
	if len(run.failures) > 0 && (cfg.barrier != nil || file.Bundle.Schema != nil) {
		return result, errors.Join(run.failures...)
	}
	if err := run.schema(ctx, &result); err != nil {
		return result, err
	}
	if cfg.barrier != nil {
		if err := cfg.barrier(ctx, s.conn); err != nil {
			return result, fmt.Errorf("barrier between pre and post scripts: %w", err)
		}
	}
	if err := run.phase(ctx, &result, scripts.Post); err != nil {
		return result, err
	}
	if len(run.failures) > 0 {
		return result, errors.Join(run.failures...)
	}
	result.Outcome = "applied"
	if len(result.Warnings) > 0 {
		result.Outcome = "applied-with-warnings"
	} else if len(result.Applied) == 0 && result.Schema == "" {
		result.Outcome = "already-applied"
	}
	return result, nil
}

// applyRun is one Apply over a plan file.
type applyRun struct {
	s        *session
	file     PlanFile
	cfg      applyConfig
	snapshot observationSnapshot
	// stale holds the after-apply inputs a writer has changed since they
	// were last observed.
	stale map[string]bool
	// recorded is the ledger hash each script applied in this run recorded.
	recorded map[string]string
	// values are the function values re-observed in this run, by input key.
	values map[string]*string
	// unverified is set once a script with unknown effects ran: the snapshot
	// no longer predicts the inputs of the scripts after it.
	unverified bool
	// unmet are the scripts that failed, or were skipped after one that did.
	unmet    map[string]bool
	failures []error
}

// check is how item's run verifies its inputs: every input before and after
// its body when its writes are not known (no effects, unknown effects, or
// after a script with unknown effects ran), otherwise only the inputs its own
// writes reach, after its body.
func (r *applyRun) check(item scripts.Pending) inputCheck {
	if r.unverified || item.Script.Effects == nil || len(item.EffectsUnknown) > 0 {
		return inputCheck{full: true}
	}
	return inputCheck{after: item.SelfWritten}
}

// schema runs the schema step, then marks the inputs it writes stale.
func (r *applyRun) schema(ctx context.Context, result *Result) error {
	step := r.file.Bundle.Schema
	if step == nil {
		return nil
	}
	if err := r.cfg.report(ProgressSchema, "", func() error { return step.run(ctx, r.s.conn, r.file.Observations[declaredSchemaKey]) }); err != nil {
		return err
	}
	if len(step.Statements) > 0 {
		result.Schema = "applied"
		r.markStale(scripts.SchemaWriter)
	}
	return nil
}

// phase runs the selected scripts of phase in plan order.
func (r *applyRun) phase(ctx context.Context, result *Result, phase scripts.Phase) error {
	for _, planned := range r.file.Actions {
		if !planned.Selected || planned.Script.Phase != phase {
			continue
		}
		if dependency := r.unmetDependency(planned); dependency != "" {
			r.skip(result, planned.Script.ID, dependency)
			continue
		}
		item, applied, err := r.runScript(ctx, result, planned)
		if err != nil {
			return errors.Join(append(r.failures, err)...)
		}
		if !applied {
			continue
		}
		result.Applied = append(result.Applied, item.Script.ID)
		r.recorded[item.Script.ID] = item.State.Hash
		r.markStale(item.Script.ID)
		if len(item.EffectsUnknown) > 0 {
			r.unverified = true
			if err := r.cfg.report(ProgressCheck, item.Script.ID, func() error { return r.unpredicted(ctx, item.Script.ID, result.Applied) }); err != nil {
				return errors.Join(append(r.failures, err)...)
			}
		}
	}
	return nil
}

func (r *applyRun) runScript(ctx context.Context, result *Result, planned scripts.Pending) (scripts.Pending, bool, error) {
	p := Progress{Step: ProgressScript, Script: planned.Script.ID}
	r.cfg.emit(p)
	p.Done = true
	item, err := r.settle(ctx, planned)
	if err == nil {
		if err = r.s.execute(ctx, item, r.file.Hash, r.check(item)); err != nil {
			err = &scriptError{err}
		}
	}
	if err == nil {
		r.cfg.emit(p)
		return item, true, nil
	}
	warned, stop := r.failed(ctx, result, item.Script.ID, err)
	if warned {
		p.Warning = err
	} else {
		p.Err = err
	}
	r.cfg.emit(p)
	return item, false, stop
}

// settle is planned as it will run: its inputs at their snapshot values,
// the after-apply ones re-observed since their writers ran, and its
// prerequisites at the hashes this run recorded.
func (r *applyRun) settle(ctx context.Context, planned scripts.Pending) (scripts.Pending, error) {
	var stale []string
	for _, input := range planned.AfterApply {
		if r.stale[input.Key] {
			stale = append(stale, input.Key)
		}
	}
	if len(stale) > 0 {
		observed, err := r.s.opts.observeKeys(ctx, r.s.conn, stale)
		if err != nil {
			return planned, err
		}
		for _, key := range stale {
			if observed[key].Error != "" {
				return planned, &StalePlanError{Moved: []string{fmt.Sprintf("input %s of %s cannot be observed after its writers: %s", key, planned.Script.ID, observed[key].Error)}}
			}
			delete(r.stale, key)
			r.values[key] = observed[key].Value
		}
		if err := r.snapshot.update(ctx, r.s.conn, observed); err != nil {
			return planned, err
		}
	}
	item := planned
	item.State.Inputs = maps.Clone(planned.State.Inputs)
	for key := range item.State.Inputs {
		dep, ok := strings.CutPrefix(key, "script:")
		if ok && r.recorded[dep] != "" {
			item.State.Inputs[key] = r.recorded[dep]
		}
		if ok {
			continue
		}
		value, observed := r.snapshot.values[key]
		if !observed {
			return planned, fmt.Errorf("script %s input %s is missing from the plan snapshot", planned.Script.ID, key)
		}
		item.State.Inputs[key] = value
	}
	item.State.Values = maps.Clone(planned.State.Values)
	for key := range item.State.Values {
		if value, ok := r.values[key]; ok {
			item.State.Values[key] = value
		}
	}
	item.State.Hash = scripts.StateHash(item.State)
	return item, nil
}

// markStale marks every later after-apply input writer wrote.
func (r *applyRun) markStale(writer string) {
	for _, item := range r.file.Actions {
		for _, input := range item.AfterApply {
			if slices.Contains(input.Writers, writer) {
				r.stale[input.Key] = true
			}
		}
	}
}

// unpredicted re-plans after writer, a script with unknown effects, and
// refuses the rest of the plan when that blocks a script the file did not. What
// it would newly select is the next plan's work.
func (r *applyRun) unpredicted(ctx context.Context, writer string, applied []string) error {
	fresh, err := plan(ctx, r.s.conn, r.s.opts)
	if err != nil {
		return err
	}
	r.s.states = fresh.Ledger
	var moved []string
	for _, item := range fresh.Actions {
		planned := r.file.action(item.Script.ID)
		switch {
		case slices.Contains(applied, item.Script.ID), r.unmet[item.Script.ID]:
		case len(item.Blocked) > 0 && len(planned.Blocked) == 0:
			moved = append(moved, fmt.Sprintf("%s is now blocked by %s", item.Script.ID, strings.Join(item.Blocked, ", ")))
		}
	}
	if len(moved) == 0 {
		return nil
	}
	return &StalePlanError{Moved: append([]string{"script " + writer + " with unknown effects changed the plan"}, moved...)}
}
