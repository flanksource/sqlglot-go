package migrate

type Name struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
}

type Schema struct {
	Tables      []Table      `json:"tables"`
	Definitions []Definition `json:"definitions"`
	Sequences   []Sequence   `json:"sequences,omitempty"`
	TableTypes  []TableType  `json:"tableTypes,omitempty"`
}

// TableType is a CREATE TYPE ... AS TABLE user-defined table type. SQL Server
// names a table type's constraints itself, so its defaults, keys and checks
// carry no names: Default.Name, Index.Name and Check.Name stay empty. Keys are
// the PRIMARY KEY and UNIQUE constraints; a table type cannot be altered.
type TableType struct {
	Name    Name     `json:"name"`
	Columns []Column `json:"columns"`
	Keys    []Index  `json:"keys,omitempty"`
	Checks  []string `json:"checks,omitempty"`
}

// Sequence is a CREATE SEQUENCE object. Numeric bounds are exact decimal
// strings so every integer and decimal sequence type round-trips. CacheSize 0
// with NoCache false is the server's default cache.
type Sequence struct {
	Name      Name   `json:"name"`
	Type      string `json:"type"`
	Start     string `json:"start"`
	Increment string `json:"increment"`
	Minimum   string `json:"minimum"`
	Maximum   string `json:"maximum"`
	Cycle     bool   `json:"cycle,omitempty"`
	NoCache   bool   `json:"noCache,omitempty"`
	CacheSize int64  `json:"cacheSize,omitempty"`
}

type Table struct {
	Name        Name         `json:"name"`
	Columns     []Column     `json:"columns"`
	Indexes     []Index      `json:"indexes,omitempty"`
	ForeignKeys []ForeignKey `json:"foreignKeys,omitempty"`
	Checks      []Check      `json:"checks,omitempty"`
}

type Column struct {
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Nullable  bool      `json:"nullable"`
	Identity  *Identity `json:"identity,omitempty"`
	Default   *Default  `json:"default,omitempty"`
	Collation string    `json:"collation,omitempty"`
	RowGUID   bool      `json:"rowGuid,omitempty"`
}

type Identity struct {
	Seed      int64 `json:"seed"`
	Increment int64 `json:"increment"`
}
type Default struct {
	Name       string `json:"name"`
	Expression string `json:"expression"`
	// SystemNamed marks a constraint SQL Server named itself (DF__table__col__hex).
	// Its name differs in every database, so it is never compared, and a new
	// one is created unnamed; Name is still what a drop of the live one uses.
	SystemNamed bool `json:"systemNamed,omitempty"`
}
type IndexColumn struct {
	Name       string `json:"name"`
	Descending bool   `json:"descending,omitempty"`
}
type Index struct {
	Name       string        `json:"name"`
	Columns    []IndexColumn `json:"columns"`
	Included   []string      `json:"included,omitempty"`
	Unique     bool          `json:"unique,omitempty"`
	Primary    bool          `json:"primary,omitempty"`
	Clustered  bool          `json:"clustered,omitempty"`
	Constraint bool          `json:"constraint,omitempty"`
	Filter     string        `json:"filter,omitempty"`
	// FillFactor is 1-99; 0 is the server default, which FILLFACTOR = 100 equals.
	FillFactor int `json:"fillFactor,omitempty"`
}
type ForeignKey struct {
	Name              string   `json:"name"`
	Columns           []string `json:"columns"`
	References        Name     `json:"references"`
	ReferencedColumns []string `json:"referencedColumns"`
	OnDelete          string   `json:"onDelete,omitempty"`
	OnUpdate          string   `json:"onUpdate,omitempty"`
}
type Check struct {
	Name       string `json:"name"`
	Expression string `json:"expression"`
}
type Definition struct {
	Name     Name   `json:"name"`
	Kind     string `json:"kind"`
	SQL      string `json:"sql"`
	Parent   *Name  `json:"parent,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	// QuotedIdentifierOff records a module created under SET QUOTED_IDENTIFIER
	// OFF, where double quotes delimit strings rather than identifiers.
	QuotedIdentifierOff bool   `json:"quotedIdentifierOff,omitempty"`
	Dependencies        []Name `json:"dependencies,omitempty"`
	// RuntimeDependencies are references the catalog could not bind to a local
	// object: missing objects, cross-database or linked-server names, built-ins
	// and method calls. They resolve when the module runs, so they never order DDL.
	RuntimeDependencies []RuntimeDependency `json:"runtimeDependencies,omitempty"`
}

type RuntimeDependency struct {
	Server   string `json:"server,omitempty"`
	Database string `json:"database,omitempty"`
	Schema   string `json:"schema,omitempty"`
	Name     string `json:"name"`
}

type Request struct {
	Current Schema `json:"current"`
	Desired Schema `json:"desired"`
	// Selection limits the plan to these change IDs; empty plans every change.
	Selection []string `json:"selection,omitempty"`
}
type Change struct {
	ID          string `json:"id"`
	Action      string `json:"action"`
	Kind        string `json:"kind"`
	Object      Name   `json:"object"`
	Member      string `json:"member,omitempty"`
	Destructive bool   `json:"destructive"`
	Reason      string `json:"reason,omitempty"`
	// RuntimeDependencies flags a module whose references bind only at runtime.
	RuntimeDependencies []RuntimeDependency `json:"runtimeDependencies,omitempty"`
	// Requires lists the change IDs that must be selected with this one.
	Requires []string `json:"requires,omitempty"`
}
type Step struct {
	Change Change `json:"change"`
	SQL    string `json:"sql"`
}
type Plan struct {
	Steps   []Step   `json:"steps"`
	Changes []Change `json:"changes"`
}
