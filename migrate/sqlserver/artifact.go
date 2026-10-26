package sqlserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/flanksource/sqlglot-go/migrate"
)

func BuildArtifact(input migrate.ArtifactRequest) (migrate.Artifact, error) {
	body, err := json.Marshal(migrate.Artifact{Version: 1, Schemas: input.Schemas, Request: input.Request})
	if err != nil {
		return migrate.Artifact{}, err
	}
	var artifact migrate.Artifact
	if err := json.Unmarshal(body, &artifact); err != nil {
		return migrate.Artifact{}, err
	}
	if err := artifactScope(artifact); err != nil {
		return migrate.Artifact{}, err
	}
	slices.Sort(artifact.Schemas)
	slices.Sort(artifact.Request.Selection)
	orderSnapshot(&artifact.Request.Current)
	orderSnapshot(&artifact.Request.Desired)
	artifact.Plan, err = Plan(artifact.Request)
	if err != nil {
		return migrate.Artifact{}, err
	}
	artifact.Hash, err = artifactHash(artifact)
	return artifact, err
}

func ValidateArtifact(artifact migrate.Artifact) error {
	if artifact.Version != 1 {
		return fmt.Errorf("sqlserver: unsupported migration artifact version %d", artifact.Version)
	}
	hash, err := artifactHash(artifact)
	if err != nil {
		return err
	}
	if artifact.Hash != hash {
		return fmt.Errorf("sqlserver: migration artifact hash mismatch")
	}
	expected, err := BuildArtifact(migrate.ArtifactRequest{Schemas: artifact.Schemas, Request: artifact.Request})
	if err != nil {
		return err
	}
	if expected.Hash != artifact.Hash {
		return fmt.Errorf("sqlserver: migration artifact SQL does not match its snapshots and planner version")
	}
	return nil
}

func CheckArtifact(artifact migrate.Artifact, actual migrate.Schema) (migrate.ArtifactState, error) {
	if err := ValidateArtifact(artifact); err != nil {
		return "", err
	}
	if err := artifactScope(migrate.Artifact{Schemas: artifact.Schemas, Request: migrate.Request{Current: actual}}); err != nil {
		return "", err
	}
	desired, err := Plan(migrate.Request{Current: actual, Desired: artifact.Request.Desired})
	if err != nil {
		return "", fmt.Errorf("sqlserver: inspect artifact state: %w", err)
	}
	if slices.Equal(changeIDs(desired.Changes), unselected(artifact)) {
		return migrate.ArtifactApplied, nil
	}
	baseline, err := Plan(migrate.Request{Current: actual, Desired: artifact.Request.Current})
	if err != nil {
		return "", fmt.Errorf("sqlserver: baseline drift: %w", err)
	}
	if len(baseline.Changes) != 0 || len(baseline.Steps) != 0 {
		return "", &DriftError{Changes: baseline.Changes}
	}
	return migrate.ArtifactPending, nil
}

// Acknowledgements lists the destructive change IDs an artifact plans, which
// applying it must acknowledge one by one.
func Acknowledgements(artifact migrate.Artifact) []string {
	ids := []string{}
	for _, c := range artifact.Plan.Changes {
		if c.Destructive && (len(artifact.Request.Selection) == 0 || slices.Contains(artifact.Request.Selection, c.ID)) {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

// unselected lists the planned change IDs a partial artifact leaves for later;
// once it is applied, they are exactly what still differs from desired.
func unselected(artifact migrate.Artifact) []string {
	ids := changeIDs(artifact.Plan.Changes)
	if len(artifact.Request.Selection) == 0 {
		return []string{}
	}
	return slices.DeleteFunc(ids, func(id string) bool { return slices.Contains(artifact.Request.Selection, id) })
}

func changeIDs(changes []migrate.Change) []string {
	ids := make([]string, 0, len(changes))
	for _, c := range changes {
		ids = append(ids, c.ID)
	}
	slices.Sort(ids)
	return ids
}

type DriftError struct{ Changes []migrate.Change }

func (e *DriftError) Error() string {
	return fmt.Sprintf("sqlserver: baseline drift: %d catalog changes since preview", len(e.Changes))
}

func artifactHash(artifact migrate.Artifact) (string, error) {
	artifact.Hash = ""
	body, err := json.Marshal(artifact)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func artifactScope(artifact migrate.Artifact) error {
	if len(artifact.Schemas) == 0 {
		return fmt.Errorf("sqlserver: migration artifact requires explicit schema scope")
	}
	seen := map[string]bool{}
	for _, name := range artifact.Schemas {
		if name == "" || seen[name] {
			return fmt.Errorf("sqlserver: empty or duplicate artifact schema %q", name)
		}
		seen[name] = true
	}
	for _, snapshot := range []migrate.Schema{artifact.Request.Current, artifact.Request.Desired} {
		for _, table := range snapshot.Tables {
			if !seen[table.Name.Schema] {
				return fmt.Errorf("sqlserver: table %v is outside artifact schema scope", table.Name)
			}
		}
		for _, definition := range snapshot.Definitions {
			if !seen[definition.Name.Schema] {
				return fmt.Errorf("sqlserver: definition %v is outside artifact schema scope", definition.Name)
			}
		}
		for _, sequence := range snapshot.Sequences {
			if !seen[sequence.Name.Schema] {
				return fmt.Errorf("sqlserver: sequence %v is outside artifact schema scope", sequence.Name)
			}
		}
		for _, tableType := range snapshot.TableTypes {
			if !seen[tableType.Name.Schema] {
				return fmt.Errorf("sqlserver: table type %v is outside artifact schema scope", tableType.Name)
			}
		}
	}
	return nil
}
