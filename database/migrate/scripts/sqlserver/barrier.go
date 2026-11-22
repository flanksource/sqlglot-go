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
	barrier     func(context.Context, Connection) error
	analysis    cache.Store
	analysisTTL time.Duration
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
	var cfg applyConfig
	for _, option := range options {
		option(&cfg)
	}
	return cfg
}
