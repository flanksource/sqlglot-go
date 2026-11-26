package migrate

// The types below are the v1 offline SQL Server planning API that
// sqlserver.Plan and sqlserver.BuildArtifact serve on top of the engine. New
// callers plan with engine.Plan, whose artifact carries units, preconditions
// and forced rebuilds that a v1 Plan flattens into steps.

type Request struct {
	Current Schema `json:"current"`
	Desired Schema `json:"desired"`
	// Selection limits the plan to these change IDs; empty plans every change.
	Selection []string `json:"selection,omitempty"`
}

type Step struct {
	Change Change `json:"change"`
	SQL    string `json:"sql"`
}

type Plan struct {
	Steps []Step `json:"steps"`
	// Changes lists every change between the snapshots, selected or not, and
	// the rebuilds the selected ones force.
	Changes []Change `json:"changes"`
	// Warnings names each definition whose body is not understood. It is
	// compared and created as written.
	Warnings []string `json:"warnings,omitempty"`
}

// Artifact binds reviewed SQL to complete baseline and desired snapshots.
type Artifact struct {
	Version int      `json:"version"`
	Schemas []string `json:"schemas"`
	Request Request  `json:"request"`
	Plan    Plan     `json:"plan"`
	Hash    string   `json:"hash"`
}

type ArtifactRequest struct {
	Schemas []string
	Request Request
}
