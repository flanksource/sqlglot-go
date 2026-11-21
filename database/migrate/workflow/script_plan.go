package workflow

import (
	"encoding/json"
	"fmt"

	scriptplan "github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver"
	native "github.com/flanksource/sqlglot-go/migrate"
)

// scriptPlanOf reads the SQL Server plan file data holds, bare or in a bundle
// preview's scriptPlan, with the preview's warnings; nil when data holds none.
func scriptPlanOf(data []byte) (*scriptplan.PlanFile, []string, error) {
	var envelope struct {
		Kind       string               `json:"kind"`
		ScriptPlan *scriptplan.PlanFile `json:"scriptPlan"`
		Warnings   []string             `json:"warnings"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, nil, err
	}
	if envelope.ScriptPlan != nil {
		return envelope.ScriptPlan, envelope.Warnings, nil
	}
	if envelope.Kind != scriptplan.PlanFileKind {
		return nil, nil, nil
	}
	var file scriptplan.PlanFile
	if err := Decode(data, &file); err != nil {
		return nil, nil, fmt.Errorf("decode plan file: %w", err)
	}
	return &file, nil, nil
}

func reviewScriptPlan(data []byte, file *scriptplan.PlanFile, warnings []string, dialect, schema string) (PlanResult, error) {
	if dialect != "" && dialect != "sqlserver" {
		return PlanResult{}, fmt.Errorf("script plan targets SQL Server, not %s", dialect)
	}
	if err := file.Verify(); err != nil {
		return PlanResult{}, err
	}
	artifact, err := json.Marshal(file)
	if err != nil {
		return PlanResult{}, err
	}
	result := PlanResult{Dialect: "sqlserver", Schema: schema, Artifact: artifact, Original: data, Hash: file.Hash,
		Steps: []Step{}, Changes: []native.Change{}, Warnings: append(warnings, file.Warnings...), Applicable: true, ScriptPlan: file}
	if file.Bundle.Schema != nil {
		for i, statement := range file.Bundle.Schema.Statements {
			result.Steps = append(result.Steps, Step{ID: fmt.Sprintf("schema-%d", i+1), SQL: statement.SQL, Destructive: statement.Destructive})
		}
	}
	result.complete()
	return result, nil
}
