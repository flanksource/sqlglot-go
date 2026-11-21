package sqlserver

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	catalog "github.com/flanksource/sqlglot-go/database/inspect/catalog/sqlserver"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
)

// declaredSchemaKey is the observation fingerprinting every declared object.
const declaredSchemaKey = "declared-schema"

// SchemaStep is the reviewed reconciliation of the bundle's declared tables
// and routines: the statements apply runs between the pre and post scripts,
// planned against the declared objects as the plan observed them.
type SchemaStep struct {
	// Objects are the declared tables and routines, schema-qualified.
	Objects    []string          `json:"objects"`
	Statements []SchemaStatement `json:"statements"`
}

type SchemaStatement struct {
	SQL         string `json:"sql"`
	Comment     string `json:"comment,omitempty"`
	Destructive bool   `json:"destructive,omitempty"`
}

// writes is what the step changes: every declared object, once it runs any
// statement.
func (step *SchemaStep) writes() []scripts.ObjectName {
	if step == nil || len(step.Statements) == 0 {
		return nil
	}
	out := make([]scripts.ObjectName, 0, len(step.Objects))
	for _, object := range step.Objects {
		out = append(out, objectName(object))
	}
	return out
}

func (step *SchemaStep) validate() error {
	if step == nil {
		return nil
	}
	if len(step.Objects) == 0 {
		return fmt.Errorf("schema step declares no objects")
	}
	for _, object := range step.Objects {
		if _, err := scripts.QuoteName(object); err != nil {
			return fmt.Errorf("schema step object: %w", err)
		}
	}
	for i, statement := range step.Statements {
		if strings.TrimSpace(statement.SQL) == "" {
			return fmt.Errorf("schema step statement %d is empty", i+1)
		}
	}
	return nil
}

// observe fingerprints the declared objects: the tables' columns, indexes,
// keys and checks, and each routine's definition, an absent object included.
func (step *SchemaStep) observe(ctx context.Context, q queryer) (string, error) {
	bySchema := map[string][]string{}
	quoted := make([]string, 0, len(step.Objects))
	for _, object := range step.Objects {
		schema, name, _ := strings.Cut(object, ".")
		bySchema[schema] = append(bySchema[schema], name)
		name, err := scripts.QuoteName(object)
		if err != nil {
			return "", err
		}
		quoted = append(quoted, name)
	}
	tables := map[string]schemaSnapshot{}
	for _, schema := range slices.Sorted(maps.Keys(bySchema)) {
		snapshot, err := readSnapshot(ctx, catalog.FromDB(q), catalog.Scope{Schema: schema, Names: bySchema[schema]})
		if err != nil {
			return "", fmt.Errorf("observe declared tables of %s: %w", schema, err)
		}
		tables[schema] = snapshot
	}
	modules, err := catalog.ReadModules(ctx, catalog.FromDB(q), slices.Sorted(slices.Values(quoted)))
	if err != nil {
		return "", fmt.Errorf("observe declared routines: %w", err)
	}
	definitions := map[string]string{}
	for name, m := range modules {
		definitions[name] = scripts.Digest(m.Kind, m.Definition, strconv.FormatBool(m.ANSI), strconv.FormatBool(m.Quoted), strconv.FormatBool(m.Unavailable))
	}
	encoded, err := json.Marshal(struct {
		Tables      map[string]schemaSnapshot
		Definitions map[string]string
	}{tables, definitions})
	if err != nil {
		return "", err
	}
	return scripts.SourceHash(string(encoded)), nil
}

// run executes the planned statements on q once the declared objects still
// match the plan; a moved object makes the plan stale before any runs.
func (step *SchemaStep) run(ctx context.Context, q sessionDB, planned string) error {
	observed, err := step.observe(ctx, q)
	if err != nil {
		return err
	}
	if observed != planned {
		return &StalePlanError{Moved: []string{"declared schema objects changed since the plan"}}
	}
	for i, statement := range step.Statements {
		if _, err := q.ExecContext(ctx, statement.SQL); err != nil {
			return fmt.Errorf("schema statement %d of %d: %w", i+1, len(step.Statements), err)
		}
	}
	return nil
}
