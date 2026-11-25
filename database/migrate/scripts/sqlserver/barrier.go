package sqlserver

import (
	"context"
	"time"

	"github.com/flanksource/clicky/cache"
)

// Connection is the locked SQL session a barrier runs on.
type Connection interface{ sessionDB }

// ApplyOption configures Apply.
type ApplyOption func(*applyConfig)

type applyConfig struct {
	barrier            func(context.Context, Connection) error
	analysis           cache.Store
	analysisTTL        time.Duration
	progress           func(Progress)
	primaryKeyWarnings bool
	schemaLockTimeout  time.Duration
}

// WithSchemaLockTimeout bounds how long each unit of the schema step waits for
// the migration lock and for a blocked statement, before failing it. It
// defaults to 15 seconds, the scripts' own statement lock timeout, and must be
// positive: the dialect reads zero as "do not wait".
func WithSchemaLockTimeout(timeout time.Duration) ApplyOption {
	return func(c *applyConfig) { c.schemaLockTimeout = timeout }
}

// WithPrimaryKeyWarnings continues after a script's primary-key violation when the
// script rolled back and its failure was recorded. Scripts that depend on it are skipped.
func WithPrimaryKeyWarnings() ApplyOption {
	return func(c *applyConfig) { c.primaryKeyWarnings = true }
}

// WithBarrier runs fn on the locked apply connection after the schema step
// and before the post scripts, for deployment steps that belong between the
// phases. Its writes are not part of the plan.
func WithBarrier(fn func(context.Context, Connection) error) ApplyOption {
	return func(c *applyConfig) { c.barrier = fn }
}

// WithAnalysisCache reuses script and module analyses across Plan and Apply,
// as Options.Analysis does for Plan.
func WithAnalysisCache(store cache.Store, ttl time.Duration) ApplyOption {
	return func(c *applyConfig) { c.analysis, c.analysisTTL = store, ttl }
}

func resolveApply(options []ApplyOption) applyConfig {
	cfg := applyConfig{schemaLockTimeout: defaultSchemaLockTimeout}
	for _, option := range options {
		option(&cfg)
	}
	return cfg
}
