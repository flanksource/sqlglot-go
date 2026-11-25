// Package scripts owns migration source identity, dependencies and consumed fingerprints.
package scripts

import "strconv"

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
	ID   string `json:"id"`
	Path string `json:"path"`
	Name string `json:"name"`
	// Labels are the project labels of the source, carried into the plan file.
	Labels               map[string][]string `json:"labels,omitempty"`
	SQL                  string              `json:"sql"`
	NoStatements         bool                `json:"noStatements,omitempty"`
	Phase                Phase               `json:"phase"`
	HeaderPhase          Phase               `json:"-"`
	Always               bool                `json:"always,omitempty"`
	NonTransactional     bool                `json:"nonTransactional,omitempty"`
	DependsOn            []string            `json:"dependsOn,omitempty"`
	Dependencies         []Dependency        `json:"dependencies,omitempty"`
	ExplicitDependencies bool                `json:"explicitDependencies,omitempty"`
	ContextPolicy        string              `json:"contextPolicy,omitempty"`
	Effects              *Effects            `json:"effects,omitempty"`
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
	// Values are the values of the function inputs: those of Inputs with a
	// sampled result, and the built-ins, which only Values tracks. Hash excludes
	// them, so a row that predates a built-in's tracking never runs again for it.
	// A script:<id> value identifies the built-ins of that prerequisite.
	Values map[string]*string `json:"values,omitempty"`
	// Reads are the answers of the script's reads right after it ran, by
	// Read.Key. Hash excludes them, as it does Values.
	Reads       map[string]ReadAnswer `json:"reads,omitempty"`
	Outcome     string                `json:"outcome,omitempty"`
	LastSuccess *State                `json:"lastSuccess,omitempty"`
}

// The kinds of observed input.
const (
	KindResult     = "function-result"
	KindDefinition = "function-definition"
	KindSchema     = "schema-dependency"
	// KindBuiltin is a call of server built-ins alone, such as
	// SUBSTRING(DB_NAME(), 5, 3), tracked by value.
	KindBuiltin = "builtin"
)

type Observation struct {
	Kind  string `json:"kind"`
	Hash  string `json:"hash"`
	Error string `json:"error,omitempty"`
	// Value is the observed value of a result or built-in, nil for NULL.
	Value *string `json:"value,omitempty"`
}

// ReadAnswer is what a read query answered: Digest identifies its columns and
// rows in any order, and Value is the value when there is only one.
type ReadAnswer struct {
	Digest string  `json:"digest,omitempty"`
	Rows   int     `json:"rows"`
	Value  *string `json:"value,omitempty"`
	Error  string  `json:"error,omitempty"`
}

func (a ReadAnswer) String() string {
	switch {
	case a.Error != "":
		return a.Error
	case a.Value != nil:
		return strconv.Quote(*a.Value)
	case a.Rows == 1:
		return "1 row"
	}
	return strconv.Itoa(a.Rows) + " rows"
}

// PlannedRead is a read of a script: what it answered when the script last ran,
// and now. A read with no recorded answer is not run.
type PlannedRead struct {
	Read
	Recorded *ReadAnswer `json:"recorded,omitempty"`
	Answer   *ReadAnswer `json:"answer,omitempty"`
	Changed  bool        `json:"changed,omitempty"`
}

// Change is a changed read's answer when the script ran, and now.
func (r PlannedRead) Change() string {
	before, after := r.Recorded.String(), r.Answer.String()
	if before == after {
		after += " with other values"
	}
	return before + " → " + after
}

// AfterApplyInput is an input whose value is known only after Writers, the
// earlier selected scripts writing what it observes, have run: apply
// re-observes it after them instead of holding it to its planned value.
type AfterApplyInput struct {
	Key     string   `json:"key"`
	Writers []string `json:"writers"`
}

type Pending struct {
	Script   Script   `json:"script"`
	State    State    `json:"state"`
	Selected bool     `json:"selected"`
	Reasons  []string `json:"reasons,omitempty"`
	Blocked  []string `json:"blocked,omitempty"`
	// Reads are the queries answering what the script reads; one whose answer
	// changed since its last run selects it. UntrackedReads are the tables it
	// reads in no query of its own, or through a routine: they never select it.
	Reads          []PlannedRead     `json:"reads,omitempty"`
	UntrackedReads []ObjectName      `json:"untrackedReads,omitempty"`
	AfterApply     []AfterApplyInput `json:"afterApply,omitempty"`
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
	// and post scripts changes.
	SchemaWrites []ObjectName
	// Reads and UntrackedReads are what each script reads, by script ID, and
	// Answers what the reads with a recorded answer answer now, by SQL.
	Reads          map[string][]Read
	UntrackedReads map[string][]ObjectName
	Answers        map[string]ReadAnswer
}

// SchemaWriter is the writer the schema step is planned as.
const SchemaWriter = "(schema)"
