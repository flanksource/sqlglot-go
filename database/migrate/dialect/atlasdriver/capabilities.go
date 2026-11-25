package atlasdriver

import (
	"context"
	"fmt"

	"github.com/flanksource/sqlglot-go/database/capabilities"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// Capabilities probes the backend through database/capabilities. With q == nil
// it answers the backend's static, offline capabilities.
func (d *Dialect) Capabilities(ctx context.Context, q engine.Querier) (engine.Capabilities, error) {
	if q == nil {
		return d.backend.OfflineCapabilities(), nil
	}
	probed, err := capabilities.ProbeBackendCapabilities(ctx, q, d.Name())
	if err != nil {
		return engine.Capabilities{}, err
	}
	return MapCapabilities(probed)
}

// MapCapabilities converts a probed backend description to the engine's
// capabilities: the fingerprint, the transactional-DDL flag and one feature per
// reported capability, keyed by its BackendCapability value.
func MapCapabilities(probed capabilities.BackendCapabilities) (engine.Capabilities, error) {
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
