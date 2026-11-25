package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/flanksource/sqlglot-go/migrate"
)

// ArtifactVersion is the version of the reviewed artifact format.
const ArtifactVersion = 2

// Artifact binds reviewed, ordered, grouped SQL to the snapshots it was planned from.
type Artifact struct {
	Version      int            `json:"version"`
	Dialect      string         `json:"dialect"`
	Capabilities Capabilities   `json:"capabilities"`
	Scope        Scope          `json:"scope"`
	Baseline     migrate.Schema `json:"baseline"`
	Desired      migrate.Schema `json:"desired"`
	// Context lists objects read only because a dependent sits on them; they
	// are never planned or preserved.
	Context   []migrate.Name `json:"context,omitempty"`
	Selection []string       `json:"selection,omitempty"`
	// SelectNone says the plan was limited to no change at all.
	SelectNone bool `json:"selectNone,omitempty"`
	// Dependents are the dependents the plan rebuilds, with the definitions to re-create and verify.
	Dependents []Dependent `json:"dependents,omitempty"`
	// Refusals are the inspection refusals no planned change touches.
	Refusals []Refusal `json:"refusals,omitempty"`
	// Warnings name the definitions the dialect compares as written.
	Warnings []string `json:"warnings,omitempty"`
	// Changes are the planned changes, forced rebuilds included.
	Changes []migrate.Change `json:"changes"`
	// Preserved are the drops left unplanned because the table is declared
	// partial (or, for a whole object, because the scope is declared).
	Preserved []migrate.Change `json:"preserved,omitempty"`
	Units     []Unit           `json:"units"`
	Atomic    bool             `json:"atomic,omitempty"`
	Hash      string           `json:"hash"`
}

func artifactHash(a Artifact) (string, error) {
	a.Hash = ""
	body, err := json.Marshal(a)
	if err != nil {
		return "", fmt.Errorf("engine: hash artifact: %w", err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// Validate checks the version and hash, then re-plans from the artifact's own
// inputs and requires the same hash, so edited SQL, units or changes are refused.
func Validate(a Artifact) error {
	if a.Version != ArtifactVersion {
		return fmt.Errorf("engine: unsupported migration artifact version %d, want %d (re-plan)", a.Version, ArtifactVersion)
	}
	hash, err := artifactHash(a)
	if err != nil {
		return err
	}
	if a.Hash != hash {
		return fmt.Errorf("engine: migration artifact hash mismatch: recorded %s, content hashes to %s", a.Hash, hash)
	}
	expected, err := Plan(PlanRequest{
		Dialect: a.Dialect, Scope: a.Scope, Context: a.Context, Current: a.Baseline, Desired: a.Desired,
		Selection: a.Selection, SelectNone: a.SelectNone, Dependents: a.Dependents, Refusals: a.Refusals, Capabilities: a.Capabilities, Atomic: a.Atomic,
	})
	if err != nil {
		return fmt.Errorf("engine: re-plan migration artifact: %w", err)
	}
	if expected.Hash != a.Hash {
		return fmt.Errorf("engine: migration artifact does not match its snapshots and planner version: re-plan gives %s, recorded %s", expected.Hash, a.Hash)
	}
	return nil
}

// Acknowledgements lists the selected destructive change IDs an artifact
// plans. A forced rebuild is acknowledged through its cause.
func Acknowledgements(a Artifact) []string {
	ids := []string{}
	for _, c := range a.Changes {
		if c.Destructive && c.Action != actionRebuild {
			ids = append(ids, c.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

// ValidateAcknowledgements requires every destructive change to be
// acknowledged exactly once and nothing else.
func ValidateAcknowledgements(a Artifact, acknowledgements []string) error {
	required := map[string]bool{}
	for _, id := range Acknowledgements(a) {
		required[id] = true
	}
	for _, id := range acknowledgements {
		if !required[id] {
			return fmt.Errorf("engine: unknown or duplicate destructive acknowledgement %q", id)
		}
		delete(required, id)
	}
	if len(required) > 0 {
		return fmt.Errorf("engine: migration requires acknowledgements for %d destructive changes", len(required))
	}
	return nil
}
