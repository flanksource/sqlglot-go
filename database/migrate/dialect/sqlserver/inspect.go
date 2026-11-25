package sqlserver

import (
	"context"
	"fmt"
	"slices"

	"github.com/flanksource/sqlglot-go/database/inspect/snapshot"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// Inspect reads the schema. Complete mode reads every object of scope.Schemas.
// Declared mode reads exactly the scope's objects that exist, with the catalog
// narrowed to their names. What the native model cannot hold comes back as a
// refusal instead of failing the inspection, so an object nobody touches never
// blocks a plan.
func (Dialect) Inspect(ctx context.Context, q engine.Querier, scope engine.Scope) (engine.Inspection, error) {
	options := snapshot.Options{CollectRefusals: true}
	switch scope.Mode {
	case engine.ScopeComplete:
		options.Schemas = scope.Schemas
	case engine.ScopeDeclared:
		if len(scope.Objects) == 0 {
			return engine.Inspection{}, nil
		}
		options.Objects = scope.Objects
		options.Schemas = objectSchemas(scope.Objects)
	default:
		return engine.Inspection{}, fmt.Errorf("sqlserver: inspect scope mode must be %q or %q, got %q", engine.ScopeDeclared, engine.ScopeComplete, scope.Mode)
	}
	result, err := snapshot.Inspect(ctx, q, options)
	if err != nil {
		return engine.Inspection{}, err
	}
	return engine.Inspection{Schema: result.Schema, Refusals: engineRefusals(result.Refusals)}, nil
}

func objectSchemas(objects []migrate.Name) []string {
	var schemas []string
	for _, o := range objects {
		if !slices.Contains(schemas, o.Schema) {
			schemas = append(schemas, o.Schema)
		}
	}
	slices.Sort(schemas)
	return schemas
}

func engineRefusals(refusals []snapshot.Refusal) []engine.Refusal {
	out := make([]engine.Refusal, len(refusals))
	for i, r := range refusals {
		out[i] = engine.Refusal{Object: r.Object, Member: r.Member, Reason: r.Reason}
	}
	return out
}
