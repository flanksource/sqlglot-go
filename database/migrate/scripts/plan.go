package scripts

import (
	"encoding/json"
	"fmt"
)

// Build plans the bundle in Ordered order. With an empty phase it plans both
// phases in one pass; a phase filters the plan to that phase while still
// propagating every earlier script's writes.
func Build(opts PlanOptions) (Plan, error) {
	if opts.Target == "" {
		return Plan{}, fmt.Errorf("script plan target is empty")
	}
	if opts.Phase != "" && opts.Phase != Pre && opts.Phase != Post {
		return Plan{}, fmt.Errorf("invalid script phase %q", opts.Phase)
	}
	ordered, err := Ordered(opts.Scripts)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Target: opts.Target, Phase: opts.Phase, Scripts: []Pending{}}
	current := map[string]Pending{}
	walk := newPropagation(opts.Modules)
	schemaWritten := false
	for _, script := range ordered {
		if script.Phase == Post && !schemaWritten {
			walk.write(SchemaWriter, opts.SchemaWrites)
			schemaWritten = true
		}
		item := pending(script, opts, current)
		walk.visit(&item)
		current[script.ID] = item
		if opts.Phase == "" || opts.Phase == script.Phase {
			plan.Scripts = append(plan.Scripts, item)
			if item.Selected && len(item.EffectsUnknown) > 0 {
				plan.Warnings = append(plan.Warnings, unknownWarning(item))
			}
		}
	}
	content, err := json.Marshal(plan)
	if err != nil {
		return Plan{}, err
	}
	plan.Hash = SourceHash(string(content))
	return plan, nil
}

// StateHash is the ledger hash of a state: its source, policy and inputs.
func StateHash(s State) string {
	return Digest("script-v2", s.SourceHash, s.PolicyHash, InputHash(s.Inputs))
}

func pending(script Script, opts PlanOptions, current map[string]Pending) Pending {
	old, observations := opts.Recorded[script.ID], opts.Observations[script.ID]
	policy, _ := json.Marshal(struct {
		Phase                              Phase
		Always, NonTransactional, Explicit bool
		DependsOn                          []string
		Dependencies                       []Dependency
		ContextPolicy                      string
	}{script.Phase, script.Always, script.NonTransactional, script.ExplicitDependencies, script.DependsOn, script.Dependencies, script.ContextPolicy})
	item := Pending{Script: script, State: State{Version: Version, SourceHash: SourceHash(script.SQL), PolicyHash: SourceHash(string(policy)),
		EffectsHash: effectsHash(script), Inputs: map[string]string{}}}
	legacy := old.Version == legacyVersion
	for key, input := range observations {
		if input.Error != "" {
			item.Blocked = append(item.Blocked, key+": "+input.Error)
			continue
		}
		if input.Hash == "" {
			item.Blocked = append(item.Blocked, key+": empty dependency hash")
			continue
		}
		item.State.Inputs[key] = input.Hash
		if old.Hash != "" && old.Inputs[key] != input.Hash {
			item.Reasons = append(item.Reasons, input.Kind+"-changed")
		}
	}
	for _, dep := range script.DependsOn {
		input := current[dep]
		if len(input.Blocked) > 0 {
			item.Blocked = append(item.Blocked, "blocked prerequisite "+dep)
		}
		if opts.Phase == Post && input.Script.Phase == Pre && input.Selected &&
			(len(input.Reasons) != 1 || input.Reasons[0] != "always") {
			item.Blocked = append(item.Blocked, "pending pre-deployment prerequisite "+dep)
		}
		item.State.Inputs["script:"+dep] = input.State.Hash
		changed := old.Inputs["script:"+dep] != input.State.Hash
		if legacy {
			changed = false
			for _, reason := range input.Reasons {
				changed = changed || reason != "always"
			}
		}
		if old.Hash != "" && changed {
			item.Reasons = append(item.Reasons, "script-dependency-changed")
		}
	}
	item.State.Hash = StateHash(item.State)
	switch {
	case old.Outcome == "partial" || old.Outcome == "commit-unknown":
		item.Blocked = append(item.Blocked, "reconciliation required: "+old.Outcome)
	case old.Outcome == "failed":
		item.Reasons = append(item.Reasons, "retry-failed")
	case old.Hash == "":
		item.Reasons = append(item.Reasons, "new")
	case old.SourceHash != item.State.SourceHash:
		item.Reasons = append(item.Reasons, "source-changed")
	case legacy && len(observations) > 0:
		item.Blocked = append(item.Blocked, "adoption-required")
	case !legacy && old.Hash != item.State.Hash && len(item.Reasons) == 0:
		item.Reasons = append(item.Reasons, "policy-changed")
	}
	if script.Always {
		item.Reasons = append(item.Reasons, "always")
	}
	item.settle()
	return item
}
