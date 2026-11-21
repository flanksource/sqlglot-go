package access

import (
	"errors"
	"fmt"

	"github.com/flanksource/sqlglot-go/sqlglot"
)

// StatementOptions say how ReadStatement reads one statement.
type StatementOptions struct {
	// Dialect is the sqlglot dialect the statement is written in.
	Dialect string
	// DefaultSchema is the schema an unqualified name is in: dbo in T-SQL,
	// public in PostgreSQL.
	DefaultSchema string
	// IntoVariables reads SELECT … INTO as assigning variables, as plpgsql
	// does, rather than as creating a table.
	IntoVariables bool
	// Line is the line the statement starts on, given to each access.
	Line int
}

// ReadStatement reads one statement: the writes it makes, then the reads and
// calls in the order they are met. Dynamic SQL is not followed: an EXEC of a
// string is an UnsupportedSQLError, as it is for ReadTSQL only that reads it
// as statements of their own.
func ReadStatement(text string, opts StatementOptions) ([]Access, error) {
	if opts.Dialect == "" || opts.DefaultSchema == "" {
		return nil, errors.New("access: a statement is read in a dialect with a default schema")
	}
	read, err := readStatement(text, statementContext{StatementOptions: opts})
	return read.accesses, err
}

// statementContext is how one statement is read: its options and, in a
// T-SQL file, how the SQL a string literal holds is read.
type statementContext struct {
	StatementOptions
	// dynamic reads the SQL an EXEC runs from a string literal as the
	// statements it runs. Nil means dynamic SQL is not read.
	dynamic func(sql string) ([]Node, error)
}

// statementRead is what one statement does: its accesses, then the
// statements the dynamic SQL it runs holds.
type statementRead struct {
	accesses []Access
	dynamic  []Node
}

// reader walks one parsed statement and records each table it reads or
// writes, once per kind, and each routine it calls.
type reader struct {
	ctx     statementContext
	ctes    map[string]bool
	targets map[*sqlglot.Expression]bool
	sourced map[*sqlglot.Expression]bool
	seen    map[string]bool
	writes  []Access
	reads   []Access
	dynamic []Node
}

func readStatement(text string, ctx statementContext) (statementRead, error) {
	root, err := sqlglot.ParseOne(text, ctx.Dialect)
	if err != nil {
		return statementRead{}, err
	}
	if root == nil {
		return statementRead{}, fmt.Errorf("the parser returned no statement")
	}
	if len(root.FindAll("Command")) > 0 {
		return statementRead{}, &UnsupportedSQLError{Class: "Command"}
	}
	r := &reader{ctx: ctx, ctes: map[string]bool{}, targets: map[*sqlglot.Expression]bool{},
		sourced: map[*sqlglot.Expression]bool{}, seen: map[string]bool{}}
	if err := r.statement(root); err != nil {
		return statementRead{}, err
	}
	return statementRead{accesses: append(r.writes, r.reads...), dynamic: r.dynamic}, nil
}

// statement dispatches one statement; a Block recurses into its own.
func (r *reader) statement(e *sqlglot.Expression) error {
	switch e.Class {
	case "Block":
		for _, item := range children(e) {
			if item.Class == "EndStatement" {
				continue
			}
			if err := r.statement(item); err != nil {
				return err
			}
		}
		return nil
	case "Declare", "Select", "Union", "Intersect", "Except", "Subquery", "Set":
		return r.walk(e)
	case "Execute", "ExecuteSql":
		return r.execute(e)
	case "Insert":
		return r.insert(e)
	case "Update":
		return r.update(e)
	case "Delete":
		return r.delete(e)
	case "Merge":
		return r.merge(e)
	case "Create":
		return r.create(e)
	case "Alter":
		return r.alter(e)
	case "Drop":
		return r.dropOrTruncate(e, "tables")
	case "TruncateTable":
		return r.dropOrTruncate(e, "expressions")
	}
	return &UnsupportedSQLError{Class: e.Class}
}

// table records a read or write of a real table, once per kind.
func (r *reader) table(kind Kind, ref tableRef) {
	key := ref.key(kind, r.ctx.DefaultSchema)
	if r.seen[key] {
		return
	}
	r.seen[key] = true
	access := Access{Kind: kind, Object: ref.object(r.ctx.DefaultSchema), Line: r.ctx.Line}
	if kind == Write {
		r.writes = append(r.writes, access)
		return
	}
	r.reads = append(r.reads, access)
}

// call records a call to a stored procedure or function.
func (r *reader) call(ref tableRef) {
	r.reads = append(r.reads, Access{Kind: Call, Object: ref.object(r.ctx.DefaultSchema), Line: r.ctx.Line})
}

func child(e *sqlglot.Expression, key string) *sqlglot.Expression {
	if e == nil {
		return nil
	}
	value, _ := e.Args[key].(*sqlglot.Expression)
	return value
}

func children(e *sqlglot.Expression) []*sqlglot.Expression {
	return list(e, "expressions")
}

func list(e *sqlglot.Expression, key string) []*sqlglot.Expression {
	if e == nil {
		return nil
	}
	value, _ := e.Args[key].([]*sqlglot.Expression)
	return value
}
