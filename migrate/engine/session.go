package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// TxSession is the session of a dialect whose unit is one transaction: it
// begins one on conn, runs setup inside it (lock, lock timeout, session
// options), and commits or rolls back in Finish. A failing setup rolls back
// and returns the setup error.
func TxSession(ctx context.Context, conn *sql.Conn, setup func(ctx context.Context, q Querier) error) (UnitSession, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return UnitSession{}, fmt.Errorf("begin migration transaction: %w", err)
	}
	if setup != nil {
		if err := setup(ctx, tx); err != nil {
			return UnitSession{}, errors.Join(err, rollback(tx))
		}
	}
	return UnitSession{Q: tx, Finish: func(_ context.Context, commit bool) error {
		if !commit {
			return rollback(tx)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration transaction: %w", err)
		}
		return nil
	}}, nil
}

func rollback(tx *sql.Tx) error {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return fmt.Errorf("rollback migration transaction: %w", err)
	}
	return nil
}
