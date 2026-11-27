// Package atlasdriver is the shared engine.Dialect implementation for the
// dialects that plan with an upstream Atlas driver (Postgres and SQLite). A
// Backend supplies what differs: the Atlas wiring, objects Atlas does not
// model, forced rebuilds and session handling.
package atlasdriver

import (
	"context"
	"database/sql"
	"sync"

	"ariga.io/atlas/schemahcl"
	atlas "ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/changeset"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// Atlas is a dialect's Atlas wiring. The differ and planner are the offline
// ones, because engine.Dialect.Diff has no database handle.
type Atlas struct {
	Marshal schemahcl.Marshaler
	Eval    schemahcl.Evaluator
	Differ  schema.Differ
	Planner atlas.PlanApplier
	// Open builds an inspecting driver over a live connection.
	Open       func(schema.ExecQuerier) (atlas.Driver, error)
	FormatType func(schema.Type) (string, error)
	// IndexFilter reads a partial index predicate from index attributes.
	IndexFilter func([]schema.Attr) string
}

// Extras are the objects of a schema that Atlas does not model.
type Extras struct {
	// Refusals name objects (or tables carrying a feature) that cannot be managed faithfully.
	Refusals []engine.Refusal
	// Definitions are objects carried as definitions only (views), so they can be rebuilt as dependents.
	Definitions []migrate.Definition
}

// Backend is the part of a dialect that the shared implementation cannot derive.
type Backend interface {
	// Name is the registered dialect name and the capability probe's backend.
	Name() string
	// OfflineCapabilities are the capabilities every server of the dialect has
	// (see engine.Dialect.Capabilities with q == nil): no fingerprint, and only
	// the features no version can lack.
	OfflineCapabilities() engine.Capabilities
	Atlas() Atlas
	Changes() changeset.Dialect
	// ValidateSchema rejects a schema name the dialect cannot migrate.
	ValidateSchema(name string) error
	// Extras reads views, triggers and the other objects Atlas does not model.
	Extras(ctx context.Context, q engine.Querier, schemaName string) (Extras, error)
	// Forcing lists the changes whose columns force dependents to be rebuilt.
	Forcing(set *changeset.Set, changes []schema.Change) ([]engine.Forcing, error)
	// Dependents lists what depends on columns; q == nil derives from current only.
	Dependents(ctx context.Context, q engine.Querier, current migrate.Schema, columns []engine.ColumnRef) ([]engine.Dependent, error)
	// Open starts a unit on its dedicated connection (see engine.Dialect.Open).
	Open(ctx context.Context, conn *sql.Conn, opts engine.SessionOptions) (engine.UnitSession, error)
	// Reconcile rewrites des in place so that attributes the database considers
	// equal to the live ones in cur are spelled like them, for the tables keep
	// accepts, and reports whether it changed anything. It must be idempotent.
	Reconcile(ctx context.Context, q engine.Querier, cur, des *schema.Schema, keep func(table string) bool) (bool, error)
	// SameDependent reports whether a rebuilt dependent is back as it was.
	SameDependent(before, after engine.Dependent) (bool, error)
	Exec(ctx context.Context, q engine.Querier, s engine.Statement) error
}

// Dialect implements engine.Dialect over a Backend.
type Dialect struct {
	backend Backend
	atlas   Atlas
	cache   decodeCache
}

var (
	_ engine.Dialect = (*Dialect)(nil)
	_ engine.Diff    = (*planDiff)(nil)
)

// New wraps a backend. The caller registers the result with engine.Register.
func New(b Backend) *Dialect { return &Dialect{backend: b, atlas: b.Atlas()} }

func (d *Dialect) Name() string { return d.backend.Name() }

func (d *Dialect) Dependents(ctx context.Context, q engine.Querier, current migrate.Schema, columns []engine.ColumnRef) ([]engine.Dependent, error) {
	return d.backend.Dependents(ctx, q, current, columns)
}

func (d *Dialect) Open(ctx context.Context, conn *sql.Conn, opts engine.SessionOptions) (engine.UnitSession, error) {
	return d.backend.Open(ctx, conn, opts)
}

func (d *Dialect) SameDependent(before, after engine.Dependent) (bool, error) {
	return d.backend.SameDependent(before, after)
}

func (d *Dialect) Exec(ctx context.Context, q engine.Querier, s engine.Statement) error {
	return d.backend.Exec(ctx, q, s)
}

// decodeCache keeps the most recently used evaluated snapshots by content.
// Evaluating HCL is most of the cost of planning and applying: the engine
// narrows the same desired and baseline snapshots to every unit, and checks
// each unit before and after it runs. The cached schemas are shared and never
// changed; decode hands out copies.
type decodeCache struct {
	mu sync.Mutex
	// entries are ordered from least to most recently used.
	entries []cached
}

type cached struct {
	native string
	schema *schema.Schema
}

const decodeCacheSize = 32

func (c *decodeCache) get(native string) (*schema.Schema, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, e := range c.entries {
		if e.native == native {
			c.entries = append(append(c.entries[:i:i], c.entries[i+1:]...), e)
			return e.schema, true
		}
	}
	return nil, false
}

func (c *decodeCache) put(native string, s *schema.Schema) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) == decodeCacheSize {
		c.entries = c.entries[1:]
	}
	c.entries = append(c.entries, cached{native, s})
}
