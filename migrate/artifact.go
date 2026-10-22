package migrate

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

type ArtifactState string

const (
	ArtifactPending ArtifactState = "pending"
	ArtifactApplied ArtifactState = "applied"
)
