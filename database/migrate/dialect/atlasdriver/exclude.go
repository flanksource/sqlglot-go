package atlasdriver

import (
	"context"
	"fmt"
	"strings"

	"ariga.io/atlas/sql/schema"
)

type excludeKey struct{}

// WithExclude returns a context under which Inspect hides the objects the Atlas
// glob patterns name ("table", "table.column", "table.*"), exactly as Atlas's
// own inspection exclusions do. engine.Scope has no place for them, and the
// same context must reach every Inspect of one plan and its apply, so the
// postcondition of a unit sees the same schema the plan did.
func WithExclude(ctx context.Context, patterns []string) context.Context {
	if len(patterns) == 0 {
		return ctx
	}
	return context.WithValue(ctx, excludeKey{}, append([]string(nil), patterns...))
}

func excludedPatterns(ctx context.Context) []string {
	patterns, _ := ctx.Value(excludeKey{}).([]string)
	return patterns
}

// Exclude removes the objects the patterns name from an evaluated schema, so a
// declared schema hides the same objects an inspected one does.
func Exclude(s *schema.Schema, patterns []string) error {
	if len(patterns) == 0 {
		return nil
	}
	if _, err := schema.ExcludeSchema(s, patterns); err != nil {
		return fmt.Errorf("apply exclusion patterns %s: %w", strings.Join(patterns, ", "), err)
	}
	return nil
}
