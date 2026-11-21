package sqlserver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// newestPerPath ranks the ledger rows of several scopes: the newest row of a
// path is what ran last on the database, whichever scope recorded it. Plan
// reads exactly the rows apply keeps when it absorbs the other scopes.
const newestPerPath = `ROW_NUMBER() OVER (PARTITION BY path ORDER BY updated_at DESC, hash DESC)`

// ledgerScope is the scope whose ledger a plan reads and writes, and the other
// scopes it absorbs.
type ledgerScope struct {
	Scope    string
	Absorbed []string
}

func (opts Options) ledger() ledgerScope {
	return ledgerScope{Scope: opts.Scope, Absorbed: opts.absorbed}
}

// placeholders binds the scopes from parameter first on and returns their
// list for an IN clause.
func placeholders(first, count int) string {
	names := make([]string, count)
	for i := range names {
		names[i] = fmt.Sprintf("@p%d", first+i)
	}
	return strings.Join(names, ",")
}

// query selects path, hash and state of the ledger: the scope's rows, or the
// newest row per path across the scope and the absorbed ones.
func (l ledgerScope) query(extended bool) (string, []any) {
	state := "CAST(NULL AS nvarchar(max))"
	if extended {
		state = "state"
	}
	if len(l.Absorbed) == 0 {
		return `SELECT path,hash,` + state + ` FROM dbo.sqlglot_migrations WHERE scope=@p1`, []any{l.Scope}
	}
	args := []any{l.Scope}
	for _, scope := range l.Absorbed {
		args = append(args, scope)
	}
	return `SELECT path,hash,state FROM (SELECT path,hash,` + state + ` AS state,` + newestPerPath + ` AS n
FROM dbo.sqlglot_migrations WHERE scope IN (` + placeholders(1, len(args)) + `)) AS ranked WHERE n=1`, args
}

// absorbableScopes lists the other scopes of the ledger that opts absorbs.
func absorbableScopes(ctx context.Context, q queryer, opts Options) (_ []string, cause error) {
	rows, err := q.QueryContext(ctx, `IF OBJECT_ID(N'dbo.sqlglot_migrations',N'U') IS NOT NULL
EXEC(N'SELECT DISTINCT scope FROM dbo.sqlglot_migrations')
ELSE SELECT CAST(NULL AS nvarchar(256)) AS scope WHERE 1=0`)
	if err != nil {
		return nil, fmt.Errorf("list ledger scopes: %w", err)
	}
	defer func() { cause = errors.Join(cause, rows.Close()) }()
	var absorbed []string
	for rows.Next() {
		var scope string
		if err := rows.Scan(&scope); err != nil {
			return nil, err
		}
		if scope != opts.Scope && opts.AbsorbScopes(scope) {
			absorbed = append(absorbed, scope)
		}
	}
	slices.Sort(absorbed)
	return absorbed, rows.Err()
}

func validateAbsorbed(opts Options) error {
	for _, scope := range opts.absorbed {
		if scope == opts.Scope || strings.TrimSpace(scope) == "" || len(scope) > 256 {
			return fmt.Errorf("invalid absorbed ledger scope %q for scope %q", scope, opts.Scope)
		}
	}
	return nil
}

// absorbStatement keeps the newest row per path across @p1 and the count
// absorbed scopes bound from @p2 on, and re-keys them to @p1. Only
// placeholders are spliced in; every scope is a bound parameter.
func absorbStatement(count int) string {
	all, others := placeholders(1, count+1), placeholders(2, count)
	return `;WITH ranked AS (SELECT ` + newestPerPath + ` AS n FROM dbo.sqlglot_migrations
WHERE scope IN (` + all + `)) DELETE FROM ranked WHERE n>1;
UPDATE dbo.sqlglot_migrations SET scope=@p1 WHERE scope IN (` + others + `);
UPDATE dbo.sqlglot_migration_attempts SET scope=@p1 WHERE scope IN (` + others + `);
DELETE FROM dbo.sqlglot_observations WHERE scope IN (` + others + `);`
}

// absorb moves the absorbed scopes' ledger into the session's scope, in one
// transaction under the session's lock: the newest row of each path stays,
// re-keyed to the scope; attempts are re-keyed, and the absorbed scopes'
// observation snapshots, made for other plans, are dropped.
func (s *session) absorb(ctx context.Context) (cause error) {
	absorbed := s.opts.absorbed
	if len(absorbed) == 0 {
		return nil
	}
	if err := ensureStore(ctx, s.conn); err != nil {
		return err
	}
	args := []any{s.opts.Scope}
	for _, scope := range absorbed {
		args = append(args, scope)
	}
	tx, err := s.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if cause != nil {
			cause = errors.Join(cause, tx.Rollback())
		}
	}()
	if _, err := tx.ExecContext(ctx, absorbStatement(len(absorbed)), args...); err != nil {
		return fmt.Errorf("absorb ledger scopes %s into %s: %w", strings.Join(absorbed, ", "), s.opts.Scope, err)
	}
	return tx.Commit()
}
