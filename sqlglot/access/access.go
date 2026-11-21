// Package access reads which database objects SQL statements read, write and
// call. ReadTSQL reads a whole T-SQL file -- its anonymous statements and the
// routines it creates, each body nested in the IF, WHILE and TRY that guard
// it; ReadStatement reads one statement of any dialect sqlglot parses.
//
// Positions are per statement: sqlglot expressions carry no offsets, so every
// access a statement makes carries the line the statement starts on.
package access

import "strings"

// Kind is what a statement does to an object.
type Kind string

const (
	Read  Kind = "read"
	Write Kind = "write"
	Call  Kind = "call"
)

// Object is a table, view or routine as a statement names it, with the
// default schema filled in. Database is set only for a name in another
// database.
type Object struct {
	Database string `json:"database,omitempty"`
	Schema   string `json:"schema"`
	Name     string `json:"name"`
}

// String is the object's dotted name, its database first when it has one.
func (o Object) String() string {
	if o.Database == "" {
		return o.Schema + "." + o.Name
	}
	return o.Database + "." + o.Schema + "." + o.Name
}

// Access is one read, write or call, on the line its statement starts on.
type Access struct {
	Kind   Kind   `json:"kind"`
	Object Object `json:"object"`
	Line   int    `json:"line"`
}

// NodeKind is what a Node of a T-SQL body is.
type NodeKind string

const (
	// StatementNode is one statement.
	StatementNode NodeKind = "statement"
	// IfNode runs Then when Condition holds, else Else.
	IfNode NodeKind = "if"
	// WhileNode runs Then while Condition holds.
	WhileNode NodeKind = "while"
	// TryNode runs Then, and Catch when it fails.
	TryNode NodeKind = "try"
)

// Node is one statement of a T-SQL body, or the IF, WHILE or TRY around the
// statements it governs. A statement that accesses nothing is no node.
type Node struct {
	Kind NodeKind `json:"kind"`
	// Line is the 1-based line the statement starts on; Text is the statement
	// as written, comments blanked. An IF's or WHILE's Text is its whole
	// statement, condition included.
	Line int    `json:"line"`
	Text string `json:"text,omitempty"`
	// Accesses are what the statement, or an IF's or WHILE's condition,
	// does: its writes first, then its reads and calls in the order they are
	// met.
	Accesses []Access `json:"accesses,omitempty"`
	// Dynamic is what the string literal a statement's EXEC or
	// sp_executesql runs does, as statements of their own; they run after
	// Accesses.
	Dynamic   []Node `json:"dynamic,omitempty"`
	Condition string `json:"condition,omitempty"`
	// Then is an IF's branch, a WHILE's body or a TRY's block; Else is an
	// IF's ELSE branch and Catch a TRY's CATCH block. HasElse and HasCatch
	// say the branch was written, since one that accesses nothing is empty.
	Then     []Node `json:"then,omitempty"`
	Else     []Node `json:"else,omitempty"`
	Catch    []Node `json:"catch,omitempty"`
	HasElse  bool   `json:"hasElse,omitempty"`
	HasCatch bool   `json:"hasCatch,omitempty"`
}

// Collect is every access of nodes and the nodes they nest, in the order
// they are written.
func Collect(nodes []Node) []Access {
	var out []Access
	for _, node := range nodes {
		out = append(out, node.Accesses...)
		out = append(out, Collect(node.Dynamic)...)
		out = append(out, Collect(node.Then)...)
		out = append(out, Collect(node.Else)...)
		out = append(out, Collect(node.Catch)...)
	}
	return out
}

// Diagnostic is one statement whose accesses could not be read: the 1-based
// line it starts on, its text, and why.
type Diagnostic struct {
	Line      int    `json:"line"`
	Statement string `json:"statement"`
	Message   string `json:"message"`
}

// UnsupportedSQLError reports a statement that parsed but whose data access
// cannot be accounted for: an opaque Command, dynamic SQL in a variable, or a
// node no rule reads. Class names the sqlglot node class.
type UnsupportedSQLError struct{ Class string }

func (e *UnsupportedSQLError) Error() string {
	return "unsupported SQL statement: " + e.Class
}

// tableRef is a real table or routine as written: catalog (database), schema
// and name.
type tableRef struct{ catalog, schema, name string }

func (r tableRef) object(defaultSchema string) Object {
	schema := r.schema
	if schema == "" {
		schema = defaultSchema
	}
	return Object{Database: r.catalog, Schema: schema, Name: r.name}
}

func (r tableRef) key(kind Kind, defaultSchema string) string {
	return strings.ToLower(string(kind) + "\x00" + r.object(defaultSchema).String())
}
