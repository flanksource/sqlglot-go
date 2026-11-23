package sqlserver

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
)

// ScriptFailure is a script whose run failed: Outcome is failed when it was
// rolled back, partial when a nontransactional script's writes may remain, and
// commit-unknown when its own transaction control leaves that open. A partial
// or commit-unknown script is blocked until it is reconciled.
type ScriptFailure struct {
	Script  string `json:"script"`
	Outcome string `json:"outcome"`
	Error   string `json:"error"`
}

// SkippedScript is a selected script that did not run because Dependency, a
// script it directly depends on, failed or was itself skipped.
type SkippedScript struct {
	Script     string `json:"script"`
	Dependency string `json:"dependency"`
}

// DependencyError is why a script was skipped, as its Progress reports it.
type DependencyError struct{ SkippedScript }

func (e *DependencyError) Error() string {
	return fmt.Sprintf("script %s was not run: it depends on %s, which failed or was skipped", e.Script, e.Dependency)
}

// failureOutcomes are the outcomes a failed script can leave, worst last.
var failureOutcomes = []string{"applied", "failed", "partial", "commit-unknown"}

// scriptError is a failure of running a script, as opposed to one of settling
// the plan around it.
type scriptError struct{ err error }

func (e *scriptError) Error() string { return e.err.Error() }
func (e *scriptError) Unwrap() error { return e.err }

// failed records id's failure and returns nil when the apply can go on. It
// cannot when the failure is not id's alone: the plan turned out stale, the
// caller cancelled, or the session is gone.
func (r *applyRun) failed(ctx context.Context, result *Result, id string, err error) (warned bool, stop error) {
	var drift *inputDrift
	if errors.As(err, &drift) {
		return false, &StalePlanError{Moved: []string{drift.Error()}}
	}
	outcome := "failed"
	var execution *ExecutionError
	if errors.As(err, &execution) {
		outcome = execution.Outcome
	}
	if outcome == "applied" {
		result.Applied = append(result.Applied, id)
	}
	if slices.Index(failureOutcomes, outcome) >= slices.Index(failureOutcomes, result.Outcome) {
		result.Outcome = outcome
	}
	var script *scriptError
	if outcome == "applied" || !errors.As(err, &script) {
		return false, err
	}
	err = script.err
	failure := ScriptFailure{Script: id, Outcome: outcome, Error: err.Error()}
	defer func() {
		if warned {
			result.Warnings = append(result.Warnings, failure)
		} else {
			result.Failed = append(result.Failed, failure)
		}
	}()
	if ctx.Err() != nil {
		return false, err
	}
	// The script may have left a transaction of its own open, and a fatal error
	// ends the session, and with it the migration lock.
	if _, sessionErr := r.s.conn.ExecContext(ctx, "IF @@TRANCOUNT > 0 ROLLBACK TRANSACTION; "+r.s.reset()); sessionErr != nil {
		return false, errors.Join(err, fmt.Errorf("the session did not survive script %s: %w", id, sessionErr))
	}
	r.markUnmet(id)
	if outcome == "failed" && r.cfg.primaryKeyWarnings && isPrimaryKeyViolation(err) {
		return true, nil
	}
	r.failures = append(r.failures, err)
	if outcome != "failed" {
		r.markStale(id)
	}
	return false, nil
}

// markUnmet marks id and every script still to run that directly or indirectly
// depends on it; plan order puts a dependency before its dependents.
func (r *applyRun) markUnmet(id string) {
	r.unmet[id] = true
	for _, item := range r.file.Actions {
		if item.Selected && r.recorded[item.Script.ID] == "" && r.unmetDependency(item) != "" {
			r.unmet[item.Script.ID] = true
		}
	}
}

// skip leaves id, which directly depends on dependency, out of the run.
func (r *applyRun) skip(result *Result, id, dependency string) {
	skipped := SkippedScript{Script: id, Dependency: dependency}
	result.Skipped = append(result.Skipped, skipped)
	if r.cfg.progress != nil {
		r.cfg.progress(Progress{Step: ProgressScript, Script: id, Done: true, Err: &DependencyError{skipped}})
	}
}

// unmetDependency is the failed or skipped script item directly depends on, if
// any: a prerequisite it declares, the writer of an input it tracks, or the
// definer of a routine it calls. Reading a table another script writes is not
// one: most of a bundle shares its configuration tables.
func (r *applyRun) unmetDependency(item scripts.Pending) string {
	direct := slices.Clone(item.Script.DependsOn)
	for _, input := range item.AfterApply {
		direct = append(direct, input.Writers...)
	}
	for _, id := range direct {
		if r.unmet[id] && id != item.Script.ID {
			return id
		}
	}
	if item.Script.Effects == nil {
		return ""
	}
	for _, id := range slices.Sorted(maps.Keys(r.unmet)) {
		if id == item.Script.ID {
			continue
		}
		definer := r.file.action(id)
		for name := range definer.Script.Defines {
			if slices.Contains(item.Script.Effects.Calls, name) {
				return id
			}
		}
	}
	return ""
}
