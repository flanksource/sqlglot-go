package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/flanksource/sqlglot-go/database/migrate/csvdata"
	scriptplan "github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// ErrOlderVersion marks a plan file in a format sqlglot no longer reads. Such
// files are never converted: the plan is made again against the live schema.
var ErrOlderVersion = errors.New("the plan file was made by an older version of sqlglot")

func olderVersion(what string) error {
	return fmt.Errorf("%w (%s) and must be re-planned: run `sqlglot migrate plan` again", ErrOlderVersion, what)
}

// planEnvelope is the outside of a plan file: a bare artifact, or a bundle
// preview that wraps its artifact with the sources and warnings it was planned from.
type planEnvelope struct {
	Artifact json.RawMessage `json:"artifact"`
	// Advisory lists why a preview can be reviewed but not applied.
	Advisory []string        `json:"advisory"`
	Warnings []string        `json:"warnings"`
	Sources  []ProjectSource `json:"sources"`
	// The keys of the previews older versions wrote around a SQL Server plan.
	SQLServerArtifact json.RawMessage `json:"sqlServerArtifact"`
	SQLServerPlan     json.RawMessage `json:"sqlServerPlan"`
}

// planFile is a plan file read once: exactly one of script, csv and schema is set.
type planFile struct {
	original []byte
	envelope planEnvelope
	// body is the artifact JSON without the envelope.
	body   json.RawMessage
	script *scriptplan.PlanFile
	csv    *csvdata.Artifact
	schema *engine.Artifact
}

func readPlanFile(data []byte) (planFile, error) {
	pf := planFile{original: data}
	if err := json.Unmarshal(data, &pf.envelope); err != nil {
		return pf, err
	}
	script, warnings, err := scriptPlanOf(data)
	if err != nil {
		return pf, err
	}
	if script != nil {
		pf.script, pf.envelope.Warnings = script, warnings
		return pf, nil
	}
	if pf.envelope.SQLServerArtifact != nil || pf.envelope.SQLServerPlan != nil {
		return pf, olderVersion("a SQL Server schema preview")
	}
	pf.body = data
	if pf.envelope.Artifact != nil {
		pf.body = pf.envelope.Artifact
	} else {
		// A bare artifact's own warnings are not the preview's.
		pf.envelope.Warnings = nil
	}
	var kind struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(pf.body, &kind); err != nil {
		return pf, err
	}
	if kind.Kind == csvdata.ArtifactKind {
		pf.csv = new(csvdata.Artifact)
		return pf, Decode(pf.body, pf.csv)
	}
	pf.schema, err = decodeSchemaArtifact(pf.body)
	return pf, err
}

// decodeSchemaArtifact reads an engine artifact, and tells the two formats it
// replaced apart from a file that is no artifact at all.
func decodeSchemaArtifact(body []byte) (*engine.Artifact, error) {
	var probe struct {
		Dialect string          `json:"dialect"`
		Units   json.RawMessage `json:"units"`
		Steps   json.RawMessage `json:"steps"`
		Plan    json.RawMessage `json:"plan"`
		Request json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, err
	}
	switch {
	case probe.Plan != nil || probe.Request != nil:
		return nil, olderVersion("a version 1 SQL Server artifact")
	case probe.Steps != nil && probe.Units == nil:
		return nil, olderVersion("a PostgreSQL or SQLite artifact with HCL snapshots")
	case probe.Dialect == "" || probe.Units == nil:
		return nil, fmt.Errorf("the plan file is not a migration artifact: it has no dialect and units")
	}
	var artifact engine.Artifact
	if err := Decode(body, &artifact); err != nil {
		return nil, err
	}
	return &artifact, nil
}

// matchTarget refuses an artifact planned for another dialect or schema than
// the target's; an empty dialect or schema is not compared.
func matchTarget(a engine.Artifact, dialect, schema string) error {
	if dialect != "" && dialect != a.Dialect {
		return fmt.Errorf("the artifact is for %s, not the target's %s", a.Dialect, dialect)
	}
	if schema != "" && !slices.Contains(a.Scope.Schemas, schema) {
		return fmt.Errorf("the artifact covers schemas %v, not the target's %q", a.Scope.Schemas, schema)
	}
	return nil
}
