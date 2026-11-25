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
	// Native is a dialect's own lossless description of the schema (Atlas HCL) when the fields above cannot hold it; the fields are then a projection of the same objects.
	Native string `json:"native,omitempty"`
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

// Table is a table declaration. A declaration is complete: the columns, indexes,
// keys and checks it omits are dropped from a table that has them. Partial says
// it names only some of the table's members, so the plan leaves every other
// member of the live table alone and never creates the table. It is read from
// the desired declaration; an inspected table never sets it, and the diff does
// not compare it.
type Table struct {
	Name        Name         `json:"name"`
	Partial     bool         `json:"partial,omitempty"`
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
	// The options below are read from a live index so a rebuild re-creates it
	// faithfully; the diff does not compare them.
	PadIndex     bool `json:"padIndex,omitempty"`
	IgnoreDupKey bool `json:"ignoreDupKey,omitempty"`
	NoRowLocks   bool `json:"noRowLocks,omitempty"`
	NoPageLocks  bool `json:"noPageLocks,omitempty"`
	NoRecompute  bool `json:"noRecompute,omitempty"`
	// Compression is ROW or PAGE; empty is none.
	Compression string `json:"compression,omitempty"`
	// Filegroup is set only when the index is not on the default filegroup.
	Filegroup string `json:"filegroup,omitempty"`
}
type ForeignKey struct {
	Name              string   `json:"name"`
	Columns           []string `json:"columns"`
	References        Name     `json:"references"`
	ReferencedColumns []string `json:"referencedColumns"`
	OnDelete          string   `json:"onDelete,omitempty"`
	OnUpdate          string   `json:"onUpdate,omitempty"`
	// NotForReplication is read from a live key so a rebuild keeps it; the diff does not compare it.
	NotForReplication bool `json:"notForReplication,omitempty"`
}
type Check struct {
	Name       string `json:"name"`
	Expression string `json:"expression"`
	// NotForReplication is read from a live check so a rebuild keeps it; the diff does not compare it.
	NotForReplication bool `json:"notForReplication,omitempty"`
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
	// Cause is the ID of the change that forced this rebuild.
	Cause string `json:"cause,omitempty"`
}
