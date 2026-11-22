package sqlserver

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
)

const (
	// PlanFileKind identifies a plan file among migration artifacts.
	PlanFileKind = "sqlglot-go/script-plan"
	// PlanFileVersion is the plan file format this package writes and reads.
	PlanFileVersion = 2
)

// PlanTarget is the database a plan was made for.
type PlanTarget struct {
	Server   string `json:"server"`
	Database string `json:"database"`
	Scope    string `json:"scope"`
}

func (t PlanTarget) String() string { return t.Server + "/" + t.Database + "/" + t.Scope }

// PlanBundle is everything apply needs to run the plan without the sources:
// the prepared scripts with their SQL and effects, the registered functions
// and the declared-schema step.
type PlanBundle struct {
	Scripts   []scripts.Script `json:"scripts"`
	Functions []Function       `json:"functions,omitempty"`
	Schema    *SchemaStep      `json:"schema,omitempty"`
}

// PlanFile is a self-contained, terraform-style plan: what the target looked
// like (its ledger and every observed input) and the ordered actions apply
// takes. Hash covers every other field; it guards the file's integrity and
// nothing else.
type PlanFile struct {
	Kind    string                   `json:"kind"`
	Version int                      `json:"version"`
	Target  PlanTarget               `json:"target"`
	Bundle  PlanBundle               `json:"bundle"`
	Ledger  map[string]scripts.State `json:"ledger"`
	// Absorbed are the other ledger scopes the plan read, newest row per path
	// with Target.Scope, and apply moves into it.
	Absorbed     []string          `json:"absorbed,omitempty"`
	Observations map[string]string `json:"observations"`
	Actions      []scripts.Pending `json:"actions"`
	Warnings     []string          `json:"warnings,omitempty"`
	Hash         string            `json:"hash"`
}

// StalePlanError refuses a plan whose target moved since it was made.
type StalePlanError struct {
	Moved []string `json:"moved"`
}

func (e *StalePlanError) Error() string {
	return "plan is stale: " + strings.Join(e.Moved, "; ") + "; run sqlglot migrate plan again"
}

func (p PlanFile) digest() (string, error) {
	p.Hash = ""
	encoded, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return scripts.SourceHash(string(encoded)), nil
}

// seal sets the integrity hash.
func (p PlanFile) seal() (PlanFile, error) {
	hash, err := p.digest()
	p.Hash = hash
	return p, err
}

// Verify checks the file's kind, version and integrity hash.
func (p PlanFile) Verify() error {
	if p.Kind == PlanFileKind && p.Version == 1 {
		return fmt.Errorf("plan file version 1 predates absorbed ledger scopes; re-run sqlglot migrate plan")
	}
	if p.Kind != PlanFileKind || p.Version != PlanFileVersion {
		return fmt.Errorf("not a version %d %s file (kind %q, version %d)", PlanFileVersion, PlanFileKind, p.Kind, p.Version)
	}
	hash, err := p.digest()
	if err != nil {
		return err
	}
	if p.Hash == "" || hash != p.Hash {
		return fmt.Errorf("plan file integrity check failed: hash %q does not match its content", p.Hash)
	}
	return nil
}

// Functions are the functions and built-ins the plan selects scripts by.
func (p PlanFile) Functions() []scripts.FunctionInput {
	return scripts.FunctionInputs(p.Actions, p.Ledger)
}

// action is the planned action of the script id; every script of the bundle
// has one.
func (p PlanFile) action(id string) scripts.Pending {
	return p.Actions[slices.IndexFunc(p.Actions, func(item scripts.Pending) bool { return item.Script.ID == id })]
}

// moved lists what differs between p and fresh, a plan of the same bundle
// made now: the ledger, the observations and the actions they select.
func (p PlanFile) moved(fresh PlanFile) []string {
	var out []string
	for _, id := range unionKeys(p.Ledger, fresh.Ledger) {
		if before, after := p.Ledger[id], fresh.Ledger[id]; before.Hash != after.Hash || before.Outcome != after.Outcome {
			out = append(out, "ledger row "+id+" changed")
		}
	}
	for _, key := range unionKeys(p.Observations, fresh.Observations) {
		if p.Observations[key] != fresh.Observations[key] {
			out = append(out, "observed input "+key+" changed")
		}
	}
	if len(p.Actions) != len(fresh.Actions) {
		return append(out, fmt.Sprintf("the bundle plans %d actions, the file %d", len(fresh.Actions), len(p.Actions)))
	}
	for i, before := range p.Actions {
		if change := actionChange(before, fresh.Actions[i]); change != "" {
			out = append(out, change)
		}
	}
	return out
}

func actionChange(before, after scripts.Pending) string {
	switch {
	case before.Script.ID != after.Script.ID:
		return "action " + before.Script.ID + " is now " + after.Script.ID
	case before.Selected != after.Selected || !slices.Equal(before.Reasons, after.Reasons):
		return "action " + before.Script.ID + " now " + describeSelection(after)
	case !slices.Equal(before.Blocked, after.Blocked):
		return "action " + before.Script.ID + " blocked by " + strings.Join(after.Blocked, ", ")
	case before.State.Hash != after.State.Hash:
		return "action " + before.Script.ID + " source, policy or inputs changed"
	case before.State.EffectsHash != after.State.EffectsHash || !slices.Equal(before.EffectsUnknown, after.EffectsUnknown):
		return "action " + before.Script.ID + " effects changed"
	case !slices.EqualFunc(before.Reads, after.Reads, func(a, b scripts.PlannedRead) bool { return a.SQL == b.SQL }):
		return "action " + before.Script.ID + " reads changed"
	}
	return ""
}

func describeSelection(item scripts.Pending) string {
	if !item.Selected {
		return "skipped"
	}
	return "runs for " + strings.Join(item.Reasons, ", ")
}

func unionKeys[V any](a, b map[string]V) []string {
	keys := slices.Collect(maps.Keys(a))
	for key := range b {
		if _, ok := a[key]; !ok {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

// Selected lists the scripts the plan runs, in order.
func (p PlanFile) Selected() []string {
	var out []string
	for _, item := range p.Actions {
		if item.Selected {
			out = append(out, item.Script.ID)
		}
	}
	return out
}

// Changed reports whether applying the plan changes anything beyond running
// its `runs: always` scripts.
func (p PlanFile) Changed() bool {
	if p.Bundle.Schema != nil && len(p.Bundle.Schema.Statements) > 0 {
		return true
	}
	for _, item := range p.Actions {
		if item.Selected && !slices.Equal(item.Reasons, []string{"always"}) {
			return true
		}
	}
	return false
}
