// Package scripts owns migration source identity, dependencies and consumed fingerprints.
package scripts

type Phase string

const (
	Pre  Phase = "pre"
	Post Phase = "post"
	// Version is the ledger state version this package records. Version 2
	// states (no EffectsHash) read as current; version 1 is the legacy
	// content-only ledger.
	Version = 3
)

// legacyVersion is the content-only ledger that recorded no inputs.
const legacyVersion = 1

type Dependency struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type Script struct {
	ID                   string       `json:"id"`
	Path                 string       `json:"path"`
	Name                 string       `json:"name"`
	SQL                  string       `json:"sql"`
	Phase                Phase        `json:"phase"`
	HeaderPhase          Phase        `json:"-"`
	Always               bool         `json:"always,omitempty"`
	NonTransactional     bool         `json:"nonTransactional,omitempty"`
	DependsOn            []string     `json:"dependsOn,omitempty"`
	Dependencies         []Dependency `json:"dependencies,omitempty"`
	ExplicitDependencies bool         `json:"explicitDependencies,omitempty"`
	ContextPolicy        string       `json:"contextPolicy,omitempty"`
	Effects              *Effects     `json:"effects,omitempty"`
	// Defines holds the body effects of each routine the script creates or
	// alters (each also listed in Effects.Writes). A script calling one of
	// them later in the bundle reaches these effects instead of the catalog's.
	Defines map[ObjectName]Effects `json:"defines,omitempty"`
}

type State struct {
	Version    int    `json:"version"`
	SourceHash string `json:"sourceHash"`
	PolicyHash string `json:"policyHash"`
	// EffectsHash identifies the effects the script was planned with. It is
	// audit evidence only: Hash excludes it, so a changed analysis never
	// selects a script by itself.
	EffectsHash string            `json:"effectsHash,omitempty"`
	Hash        string            `json:"hash"`
	Inputs      map[string]string `json:"inputs"`
	Outcome     string            `json:"outcome,omitempty"`
	LastSuccess *State            `json:"lastSuccess,omitempty"`
}

type Observation struct {
	Kind  string `json:"kind"`
	Hash  string `json:"hash"`
	Error string `json:"error,omitempty"`
}

// UpstreamWrite is an object a script's dependency closure reaches that an
// earlier selected script in the same plan writes.
type UpstreamWrite struct {
	Object ObjectName `json:"object"`
	Script string     `json:"script"`
}

// AfterApplyInput is an input whose value is known only after Writers, the
// earlier selected scripts writing what it observes, have run: apply
// re-observes it after them instead of holding it to its planned value.
type AfterApplyInput struct {
	Key     string   `json:"key"`
	Writers []string `json:"writers"`
}

type Pending struct {
	Script     Script            `json:"script"`
	State      State             `json:"state"`
	Selected   bool              `json:"selected"`
	Reasons    []string          `json:"reasons,omitempty"`
	Blocked    []string          `json:"blocked,omitempty"`
	Upstream   []UpstreamWrite   `json:"upstream,omitempty"`
	AfterApply []AfterApplyInput `json:"afterApply,omitempty"`
	// SelfWritten lists the input keys whose observed objects the script's
	// own writes reach: apply observes exactly these again after the script
	// runs, since a script must not change its own inputs.
	SelfWritten []string `json:"selfWritten,omitempty"`
	// EffectsUnknown lists the statements of the script, or of a routine it
	// reaches, whose effects could not be determined. Apply re-inspects the
	// plan after such a script runs.
	EffectsUnknown []string `json:"effectsUnknown,omitempty"`
}

// AfterApplyKey reports whether key is known only after apply.
func (p Pending) AfterApplyKey(key string) bool {
	for _, input := range p.AfterApply {
		if input.Key == key {
			return true
		}
	}
	return false
}

type Plan struct {
	Target   string    `json:"target"`
	Phase    Phase     `json:"phase"`
	Hash     string    `json:"hash"`
	Scripts  []Pending `json:"scripts"`
	Warnings []string  `json:"warnings,omitempty"`
}

type PlanOptions struct {
	Target string
	// Phase filters the plan to one phase; empty plans the whole bundle in
	// one pass, pre scripts first.
	Phase        Phase
	Scripts      []Script
	Recorded     map[string]State
	Observations map[string]map[string]Observation
	// Modules holds the effects of catalog routines, by name, so a call into
	// the catalog reaches what that routine reads and calls.
	Modules map[ObjectName]Effects
	// SchemaWrites are the declared objects the schema step between the pre
	// and post scripts changes; a post script reaching one is selected as an
	// upstream write of SchemaWriter.
	SchemaWrites []ObjectName
}

// SchemaWriter is the writer the schema step is planned as.
const SchemaWriter = "(schema)"
