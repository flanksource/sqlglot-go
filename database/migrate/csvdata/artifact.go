package csvdata

import (
	"fmt"
	"slices"
)

const ArtifactKind = "csv-data"

type Artifact struct {
	Kind      string              `json:"kind"`
	Version   int                 `json:"version"`
	Schema    string              `json:"schema"`
	Database  string              `json:"database"`
	Target    string              `json:"target"`
	Source    string              `json:"source"`
	Phase     string              `json:"phase,omitempty"`
	Files     []File              `json:"files"`
	Unmatched []string            `json:"unmatched,omitempty"`
	Filters   map[string][]string `json:"filters"`
	Tables    []TablePlan         `json:"tables"`
	Hash      string              `json:"hash"`
}

type TablePlan struct {
	Table           string `json:"table"`
	Phase           string `json:"phase"`
	Inserts         int    `json:"inserts"`
	Updates         int    `json:"updates"`
	ExplicitDeletes int    `json:"explicitDeletes"`
	SnapshotDeletes int    `json:"snapshotDeletes"`
	Unchanged       int    `json:"unchanged"`
	SchemaHash      string `json:"schemaHash"`
	StateHash       string `json:"stateHash"`
	DeleteHash      string `json:"deleteHash,omitempty"`
}

func (a Artifact) Acknowledgements() []string {
	result := []string{}
	for _, table := range a.Tables {
		if count := table.ExplicitDeletes + table.SnapshotDeletes; count > 0 {
			result = append(result, fmt.Sprintf("csv-delete:%s:%d:%s", table.Table, count, table.DeleteHash))
		}
	}
	slices.Sort(result)
	return result
}

func (a Artifact) fingerprint() string {
	a.Hash = ""
	return jsonDigest(a)
}

func (a Artifact) Validate(hash string, acknowledgements []string) error {
	if a.Kind != ArtifactKind || a.Version != 1 || a.Hash == "" || a.Hash != hash || a.Hash != a.fingerprint() {
		return fmt.Errorf("reviewed CSV hash does not match artifact")
	}
	provided := slices.Clone(acknowledgements)
	slices.Sort(provided)
	if !slices.Equal(a.Acknowledgements(), provided) {
		return fmt.Errorf("CSV apply requires the exact reviewed delete acknowledgements")
	}
	return nil
}
