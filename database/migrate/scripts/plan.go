package scripts

import (
	"encoding/json"
	"fmt"
	"slices"
)

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
	for _, script := range ordered {
		item := pending(script, opts, current)
		current[script.ID] = item
		if opts.Phase == "" || opts.Phase == script.Phase {
			plan.Scripts = append(plan.Scripts, item)
		}
	}
	content, err := json.Marshal(plan)
	if err != nil {
		return Plan{}, err
	}
	plan.Hash = SourceHash(string(content))
	return plan, nil
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
	item := Pending{Script: script, State: State{Version: Version, SourceHash: SourceHash(script.SQL), PolicyHash: SourceHash(string(policy)), Inputs: map[string]string{}}}
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
		if old.Version < Version {
			changed = false
			for _, reason := range input.Reasons {
				changed = changed || reason != "always"
			}
		}
		if old.Hash != "" && changed {
			item.Reasons = append(item.Reasons, "script-dependency-changed")
		}
	}
	item.State.Hash = Digest("script-v2", item.State.SourceHash, item.State.PolicyHash, InputHash(item.State.Inputs))
	switch {
	case old.Outcome == "partial" || old.Outcome == "commit-unknown":
		item.Blocked = append(item.Blocked, "reconciliation required: "+old.Outcome)
	case old.Outcome == "failed":
		item.Reasons = append(item.Reasons, "retry-failed")
	case old.Hash == "":
		item.Reasons = append(item.Reasons, "new")
	case old.SourceHash != item.State.SourceHash:
		item.Reasons = append(item.Reasons, "source-changed")
	case old.Version < Version && len(observations) > 0:
		item.Blocked = append(item.Blocked, "adoption-required")
	case old.Version == Version && old.Hash != item.State.Hash && len(item.Reasons) == 0:
		item.Reasons = append(item.Reasons, "policy-changed")
	}
	if script.Always {
		item.Reasons = append(item.Reasons, "always")
	}
	slices.Sort(item.Reasons)
	item.Reasons = slices.Compact(item.Reasons)
	slices.Sort(item.Blocked)
	item.Selected = len(item.Reasons) > 0 && len(item.Blocked) == 0
	return item
}
