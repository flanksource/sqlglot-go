package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/database/migrate/dialect/atlasdriver"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

const (
	scratchTable     = "sqlglot_check_scratch"
	scratchSavepoint = "sqlglot_check_scratch"
	constraintDef    = `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid = $1::regclass AND conname = $2`
)

// reconcileChecks spells a desired check like the live one when PostgreSQL
// renders both to the same constraint definition. A check written in any
// equivalent form (IN list, redundant casts, parentheses) would otherwise be
// dropped and re-added, re-validating every row, on each apply; the edit
// detection the dialect adds compares parsed SQL, which cannot know that
// PostgreSQL rewrites an IN list to = ANY (ARRAY[...]). The desired expression
// is normalised by adding it to a scratch copy of the table inside a
// transaction that is always rolled back.
func reconcileChecks(ctx context.Context, q engine.Querier, live, desired *schema.Table) (bool, error) {
	var candidates []*schema.Check
	liveByName := map[string]*schema.Check{}
	for _, c := range live.Checks() {
		liveByName[c.Name] = c
	}
	for _, c := range desired.Checks() {
		if old, ok := liveByName[c.Name]; ok && c.Name != "" && old.Expr != c.Expr {
			candidates = append(candidates, c)
		}
	}
	if len(candidates) == 0 {
		return false, nil
	}
	table := atlasdriver.QuoteIdent(live.Schema.Name) + "." + atlasdriver.QuoteIdent(live.Name)
	changed := false
	err := withScratch(ctx, q, func(tx engine.Querier) error {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("CREATE TEMP TABLE %s (LIKE %s)", scratchTable, table)); err != nil {
			return fmt.Errorf("copy %s to normalize its checks: %w", table, err)
		}
		for _, c := range candidates {
			same, err := sameConstraintDef(ctx, tx, table, c)
			if err != nil {
				return err
			}
			if same {
				c.Expr, changed = liveByName[c.Name].Expr, true
			}
		}
		return nil
	})
	return changed, err
}

func sameConstraintDef(ctx context.Context, tx engine.Querier, table string, c *schema.Check) (bool, error) {
	add := fmt.Sprintf("ALTER TABLE pg_temp.%s ADD CONSTRAINT %s CHECK (%s)", scratchTable, atlasdriver.QuoteIdent(c.Name), c.Expr)
	if _, err := tx.ExecContext(ctx, add); err != nil {
		return false, fmt.Errorf("normalize check %s on %s: %w", c.Name, table, err)
	}
	var normalized, live string
	if err := tx.QueryRowContext(ctx, constraintDef, "pg_temp."+scratchTable, c.Name).Scan(&normalized); err != nil {
		return false, fmt.Errorf("read normalized check %s on %s: %w", c.Name, table, err)
	}
	if err := tx.QueryRowContext(ctx, constraintDef, table, c.Name).Scan(&live); err != nil {
		return false, fmt.Errorf("read live check %s on %s: %w", c.Name, table, err)
	}
	return normalized == live, nil
}

type transactionStarter interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

// withScratch runs fn where nothing it does is kept: in a transaction rolled
// back afterwards, or, when q already is one, in a savepoint rolled back to.
func withScratch(ctx context.Context, q engine.Querier, fn func(engine.Querier) error) (returnErr error) {
	switch c := q.(type) {
	case transactionStarter:
		tx, err := c.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin scratch transaction: %w", err)
		}
		defer func() { returnErr = errors.Join(returnErr, rollbackScratch(tx)) }()
		if _, err := tx.ExecContext(ctx, lockTimeoutSQL(0, true)); err != nil {
			return fmt.Errorf("bound scratch lock wait: %w", err)
		}
		return fn(tx)
	case *sql.Tx:
		if _, err := c.ExecContext(ctx, "SAVEPOINT "+scratchSavepoint); err != nil {
			return fmt.Errorf("open scratch savepoint: %w", err)
		}
		defer func() {
			_, undo := c.ExecContext(context.WithoutCancel(ctx), "ROLLBACK TO SAVEPOINT "+scratchSavepoint)
			_, release := c.ExecContext(context.WithoutCancel(ctx), "RELEASE SAVEPOINT "+scratchSavepoint)
			returnErr = errors.Join(returnErr, undo, release)
		}()
		return fn(c)
	default:
		return fmt.Errorf("cannot normalize checks through a %T: it can neither begin a transaction nor is one", q)
	}
}

func rollbackScratch(tx *sql.Tx) error {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return fmt.Errorf("roll back scratch transaction: %w", err)
	}
	return nil
}
