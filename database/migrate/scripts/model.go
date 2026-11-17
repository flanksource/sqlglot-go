// Package scripts owns migration source identity, dependencies and consumed fingerprints.
package scripts

type Phase string

const (
	Pre     Phase = "pre"
	Post    Phase = "post"
	Version       = 2
)

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
}

type State struct {
	Version      int               `json:"version"`
	SourceHash   string            `json:"sourceHash"`
	PolicyHash   string            `json:"policyHash"`
	Hash         string            `json:"hash"`
	Inputs       map[string]string `json:"inputs"`
	Outcome      string            `json:"outcome,omitempty"`
	EvidenceHash string            `json:"evidenceHash,omitempty"`
	Provenance   string            `json:"provenance,omitempty"`
	LastSuccess  *State            `json:"lastSuccess,omitempty"`
}

type Observation struct {
	Kind  string `json:"kind"`
	Hash  string `json:"hash"`
	Error string `json:"error,omitempty"`
}

type Pending struct {
	Script   Script   `json:"script"`
	State    State    `json:"state"`
	Selected bool     `json:"selected"`
	Reasons  []string `json:"reasons,omitempty"`
	Blocked  []string `json:"blocked,omitempty"`
}

type Plan struct {
	Target  string    `json:"target"`
	Phase   Phase     `json:"phase"`
	Hash    string    `json:"hash"`
	Scripts []Pending `json:"scripts"`
}

type PlanOptions struct {
	Target       string
	Phase        Phase
	Scripts      []Script
	Recorded     map[string]State
	Observations map[string]map[string]Observation
}
