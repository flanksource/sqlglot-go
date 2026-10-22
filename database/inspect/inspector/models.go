package inspector

import (
	"context"
	"database/sql"
	"fmt"
)

// Table represents a database table or view
type Table struct {
	Schema  string
	Name    string
	Type    string // "table" or "view"
	ViewDef string // SQL definition for views
}

// Column represents a table column
type Column struct {
	TableName        string
	ColumnName       string
	DataType         string
	IsNullable       bool
	DefaultValue     *string
	Position         int
	IsAutoIncrement  bool
	IsPrimaryKey     bool
	IsUnique         bool
	MaxLength        *int // For varchar, char
	NumericScale     *int // For decimal, numeric
	NumericPrecision *int // For decimal, numeric
	Comment          *string
	EnumValues       []string // For enum types
}

// Index represents a table index
type Index struct {
	TableName string
	IndexName string
	IsUnique  bool
	IsPrimary bool
	Columns   []string // Ordered list of column names
	Type      string   // btree, hash, gin, gist, etc.
	Condition string   // WHERE clause for partial indexes
}

// ForeignKey represents a foreign key constraint
type ForeignKey struct {
	TableName      string
	ColumnName     string
	RefSchemaName  string
	RefTableName   string
	RefColumnName  string
	ConstraintName string
}

// StoredProc represents a stored procedure or function
type StoredProc struct {
	ID         string
	Schema     string
	Name       string
	Type       string // "procedure" or "function"
	SQL        string // Complete SQL body/definition
	ReturnType string // For functions: the SQL return type (e.g. "int", "TABLE"); empty for procedures
}

// ProcParam represents a stored procedure parameter
type ProcParam struct {
	ProcID    string
	ProcName  string
	ParamName string
	DataType  string
	Position  int
}

// Trigger represents a database trigger attached to a table.
//
// Event is one or more trigger events, comma-joined when a single trigger
// fires on multiple DML actions (e.g. "INSERT,UPDATE"). Timing is "AFTER"
// or "INSTEAD OF" for SQL Server, which does not support BEFORE triggers.
type Trigger struct {
	Schema     string
	Name       string
	TableName  string
	Event      string
	Timing     string
	IsDisabled bool
	SQL        string // CREATE TRIGGER body from OBJECT_DEFINITION
}

// Inspector provides database introspection capabilities.
//
// Every catalogue method is schema-scoped: it returns a bounded set of rows
// in one round trip, and each row carries the name of the object that owns it
// (Column.TableName, Index.TableName, ForeignKey.TableName, ProcParam.ProcName).
// Callers group; dialects do not. Asking per object turned a schema dump into
// thousands of round trips for no extra information.
//
// Ordering is part of the contract, not an accident: every method returns rows
// ordered by owner and then by the object's own natural order (ordinal position,
// index name, constraint column position, parameter id). Consumers cache and
// compare catalogs, so row ordering must be deterministic.
type Inspector interface {
	// Schema operations
	GetSchemas(ctx context.Context) ([]string, error)
	GetDefaultSchema(ctx context.Context) (string, error)
	GetDatabaseName(ctx context.Context) (string, error)

	// Table operations
	GetTables(ctx context.Context, schema string, tableType string) ([]*Table, error)
	GetColumns(ctx context.Context, schema string) ([]*Column, error)
	GetIndexes(ctx context.Context, schema string) ([]*Index, error)
	GetForeignKeys(ctx context.Context, schema string) ([]*ForeignKey, error)

	// Stored procedure operations
	GetStoredProcs(ctx context.Context, schema string) ([]*StoredProc, error)
	GetProcParams(ctx context.Context, schema string) ([]*ProcParam, error)

	// Trigger operations. Dialects without trigger support return (nil, nil);
	// only absence-of-feature is signalled by the nil slice, errors are real
	// introspection failures.
	GetTriggers(ctx context.Context, schema string) ([]*Trigger, error)
}

// NewInspector creates an inspector for the given database driver
func NewInspector(db *sql.DB, driver string, bounds *Bounds) (Inspector, error) {
	reader := &boundedDB{DB: db, driver: driver, bounds: bounds}
	switch driver {
	case "sqlserver":
		return &SQLServerInspector{db: reader}, nil
	case "postgres":
		return &PostgresInspector{db: reader}, nil
	default:
		return nil, fmt.Errorf("unsupported database driver: %s", driver)
	}
}
