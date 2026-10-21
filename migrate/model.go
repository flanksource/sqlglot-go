package migrate

type Name struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
}

type Schema struct {
	Tables      []Table      `json:"tables"`
	Definitions []Definition `json:"definitions"`
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
}

type Identity struct {
	Seed      int64 `json:"seed"`
	Increment int64 `json:"increment"`
}
type Default struct {
	Name       string `json:"name"`
	Expression string `json:"expression"`
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
	Name         Name   `json:"name"`
	Kind         string `json:"kind"`
	SQL          string `json:"sql"`
	Parent       *Name  `json:"parent,omitempty"`
	Disabled     bool   `json:"disabled,omitempty"`
	Dependencies []Name `json:"dependencies,omitempty"`
}

type Request struct {
	Current Schema `json:"current"`
	Desired Schema `json:"desired"`
}
type Change struct {
	ID          string `json:"id"`
	Action      string `json:"action"`
	Kind        string `json:"kind"`
	Object      Name   `json:"object"`
	Member      string `json:"member,omitempty"`
	Destructive bool   `json:"destructive"`
	Reason      string `json:"reason,omitempty"`
}
type Step struct {
	Change Change `json:"change"`
	SQL    string `json:"sql"`
}
type Plan struct {
	Steps   []Step   `json:"steps"`
	Changes []Change `json:"changes"`
}
