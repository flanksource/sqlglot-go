package sqlserver

import (
	"context"
	"fmt"
)

// Connection is the owned SQL session available during a deployment barrier.
type Connection interface{ sessionDB }

// Barrier runs a caller's reviewed schema operation while retaining the bundle lock.
// Subsequent inspections start with fresh observations.
func (s *Session) Barrier(ctx context.Context, run func(context.Context, Connection) error) error {
	if s.closed || run == nil {
		return fmt.Errorf("barrier requires an open session and an operation")
	}
	return run(ctx, s.conn)
}

type ContinuationError struct{ Preview Preview }

func (e *ContinuationError) Error() string {
	return "script dependencies changed; review continuation " + e.Preview.Plan.Hash
}
