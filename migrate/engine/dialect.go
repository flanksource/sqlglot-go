package engine

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"sort"
	"sync"

	"github.com/flanksource/sqlglot-go/migrate"
)

// Diff is one comparison of two schemas, held by the dialect so that Lower can
// reach the dialect's own representation of each change.
type Diff interface {
	// Changes lists every change with Destructive and Requires set.
	Changes() []migrate.Change
	// Forcing lists the changes that force dependents to be rebuilt.
	Forcing() []Forcing
	// Lower returns the primitive ops for the selected changes and rebuilds.
	Lower(selection []string, rebuilds []Rebuild) ([]Op, error)
	// Warnings lists the definitions the dialect compares as written because it
	// does not understand their bodies.
	Warnings() []string
}

// Dialect supplies everything database-specific. The engine narrows the
// schemas it hands to Diff to the objects of one unit when it classifies that
// unit, so:
//   - Diff must accept a schema whose foreign keys reference a table that is
//     not in the schema (the reference is compared by name and columns).
//   - Inspect in declared mode returns exactly the scope's Objects that exist.
//   - Schema.Native is passed through un-narrowed (the engine treats it as
//     opaque), so a dialect that reads it must filter it to the objects left in
//     the projection itself.
type Dialect interface {
	Name() string
	// Capabilities reads what the database can do and fingerprints it. q == nil
	// means offline (the same convention as Dependents): return the dialect's
	// static capabilities, which hold for every server of the dialect: whether
	// its DDL is transactional, an EMPTY Fingerprint (an offline artifact is not
	// bound to one server, and Apply skips the fingerprint check for it) and only
	// the features no version or compatibility level can lack.
	Capabilities(ctx context.Context, q Querier) (Capabilities, error)
	Inspect(ctx context.Context, q Querier, scope Scope) (Inspection, error)
	// Dependents lists what depends on columns. q == nil means offline: derive
	// from current only.
	Dependents(ctx context.Context, q Querier, current migrate.Schema, columns []ColumnRef) ([]Dependent, error)
	Diff(current, desired migrate.Schema) (Diff, error)
	Preconditions(change migrate.Change, current, desired migrate.Schema, caps Capabilities) ([]Precondition, error)
	// Open starts a unit on its dedicated connection: it sets pragmas, the
	// lock, the lock timeout and session options, and begins a transaction
	// when opts.Transactional. The engine calls the returned Finish exactly
	// once: commit=true only after the postcondition passed.
	Open(ctx context.Context, conn *sql.Conn, opts SessionOptions) (UnitSession, error)
	// Reconcile returns desired rewritten so that unset attributes inherit the
	// live value and values the database considers equal to the live ones are
	// spelled like them (Postgres default expressions, SQL Server collation).
	Reconcile(ctx context.Context, q Querier, current, desired migrate.Schema) (migrate.Schema, error)
	// SameDependent reports whether a dependent that was rebuilt is back as it
	// was: before is the one recorded at plan time, after the one read now. A
	// dialect may accept a changed definition (a Postgres view's stored text
	// changes after a retype); SameDefinition is the strict comparison.
	SameDependent(before, after Dependent) (bool, error)
	// Exec runs one statement of a plan. Any wrapping the dialect needs (SQL
	// Server's nested batch that sets session options) happens here, never in
	// the reviewed SQL.
	Exec(ctx context.Context, q Querier, s Statement) error
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Dialect{}
)

// Register adds a dialect by name. It panics on an empty or duplicate name.
func Register(d Dialect) {
	name := d.Name()
	if name == "" {
		panic("engine: dialect registered with an empty name")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, ok := registry[name]; ok {
		panic(fmt.Sprintf("engine: dialect %q registered twice", name))
	}
	registry[name] = d
}

// Lookup returns the registered dialect, or an error naming the known ones.
func Lookup(name string) (Dialect, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	d, ok := registry[name]
	if !ok {
		known := make([]string, 0, len(registry))
		for n := range registry {
			known = append(known, n)
		}
		sort.Strings(known)
		return nil, fmt.Errorf("engine: unknown dialect %q (registered: %v)", name, slices.Clone(known))
	}
	return d, nil
}
