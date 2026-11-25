package engine_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
)

// fakeServer is an in-memory "database" for the executor tests: a map of
// table to column type that statements mutate, a log of everything run, and
// transactions that roll the map back. Errors and guard rows are scripted.
type fakeServer struct {
	mu        sync.Mutex
	world     map[string]string
	indexes   map[string]string // "table.index" -> the columns it covers
	log       []string
	execErr   map[string]error
	commitErr error
	rows      map[string]bool // query text -> returns one row
}

func newFakeServer() *fakeServer {
	return &fakeServer{world: map[string]string{}, indexes: map[string]string{}, execErr: map[string]error{}, rows: map[string]bool{}}
}

func (s *fakeServer) indexSnapshot() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.indexes)
}

type fakeState struct{ world, indexes map[string]string }

func (s *fakeServer) open() *sql.DB { return sql.OpenDB(fakeConnector{s}) }

func (s *fakeServer) logged(skip ...string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.DeleteFunc(slices.Clone(s.log), func(l string) bool {
		return slices.ContainsFunc(skip, func(p string) bool { return strings.HasPrefix(l, p) })
	})
}

func (s *fakeServer) snapshot() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.world)
}

type fakeConnector struct{ s *fakeServer }

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) { return &fakeConn{s: c.s}, nil }
func (c fakeConnector) Driver() driver.Driver                        { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use the connector") }

type fakeConn struct {
	s     *fakeServer
	saved fakeState
}

func (c *fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}
func (c *fakeConn) Close() error { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *fakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.s.log = append(c.s.log, "BEGIN")
	c.saved = fakeState{world: maps.Clone(c.s.world), indexes: maps.Clone(c.s.indexes)}
	return c, nil
}

func (c *fakeConn) Commit() error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.s.log = append(c.s.log, "COMMIT")
	if c.s.commitErr != nil {
		c.s.world, c.s.indexes = c.saved.world, c.saved.indexes
		return c.s.commitErr
	}
	return nil
}

func (c *fakeConn) Rollback() error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.s.log = append(c.s.log, "ROLLBACK")
	c.s.world, c.s.indexes = c.saved.world, c.saved.indexes
	return nil
}

// ExecContext understands "ALTER <table>", which retypes the table to bigint,
// "DROP <table>", and "DROP INDEX <key>" / "CREATE INDEX <key>" for indexes.
func (c *fakeConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.s.log = append(c.s.log, query)
	if err := c.s.execErr[query]; err != nil {
		return nil, err
	}
	if key, ok := strings.CutPrefix(query, "DROP INDEX "); ok {
		delete(c.s.indexes, key)
		return driver.RowsAffected(0), nil
	}
	if key, ok := strings.CutPrefix(query, "CREATE INDEX "); ok {
		c.s.indexes[key] = "ref"
		return driver.RowsAffected(0), nil
	}
	if table, ok := strings.CutPrefix(query, "ALTER "); ok {
		c.s.world[table] = "bigint"
	}
	if table, ok := strings.CutPrefix(query, "DROP "); ok {
		delete(c.s.world, table)
	}
	return driver.RowsAffected(0), nil
}

func (c *fakeConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.s.log = append(c.s.log, query)
	return &fakeRows{pending: c.s.rows[query]}, nil
}

type fakeRows struct{ pending bool }

func (r *fakeRows) Columns() []string { return []string{"violation"} }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if !r.pending {
		return io.EOF
	}
	r.pending = false
	dest[0] = int64(1)
	return nil
}
