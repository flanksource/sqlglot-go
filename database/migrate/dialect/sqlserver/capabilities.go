package sqlserver

import (
	"context"
	"fmt"

	"github.com/flanksource/sqlglot-go/database/capabilities"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// Capabilities probes the database. Feature keys are the BackendCapability
// values, so the planner's FeatureTryConvert is the key of the same name. With
// q == nil it answers the planner's static, offline capabilities.
func (d Dialect) Capabilities(ctx context.Context, q engine.Querier) (engine.Capabilities, error) {
	if q == nil {
		return d.Planner.Capabilities(ctx, nil)
	}
	probed, err := capabilities.ProbeBackendCapabilities(ctx, q, "sqlserver")
	if err != nil {
		return engine.Capabilities{}, err
	}
	return mapCapabilities(probed)
}

// mapCapabilities converts a probed description to the engine's: the
// fingerprint, the transactional-DDL flag, and one feature per capability.
func mapCapabilities(probed capabilities.BackendCapabilities) (engine.Capabilities, error) {
	fingerprint, err := probed.Fingerprint()
	if err != nil {
		return engine.Capabilities{}, err
	}
	transactional, ok := probed.Features[capabilities.BackendCapabilityTransactionalDDL]
	if !ok {
		return engine.Capabilities{}, fmt.Errorf("backend %q did not report capability %q", probed.Backend, capabilities.BackendCapabilityTransactionalDDL)
	}
	features := make(map[string]bool, len(probed.Features))
	for name, status := range probed.Features {
		features[string(name)] = status.Supported
	}
	return engine.Capabilities{Fingerprint: fingerprint, TransactionalDDL: transactional.Supported, Features: features}, nil
}
