package sqlserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"

	"github.com/flanksource/sqlglot-go/migrate/internal/sqlx"
)

// DefaultPlan generates SQL Server migration statements without a database connection.
var DefaultPlan = Planner{}

var errUnsupported = errors.New("not supported by the SQL Server driver")

// Planner generates SQL for schema changes without a database connection.
type Planner struct{}

// PlanChanges returns a migration plan for the given schema changes. Each
// statement is its own change, so a routine definition runs as its own batch.
// The statements stay valid at compatibility level 100.
func (Planner) PlanChanges(_ context.Context, name string, changes []schema.Change, opts ...migrate.PlanOption) (*migrate.Plan, error) {
	s, err := planned(name, changes, opts)
	if err != nil {
		return nil, err
	}
	return &s.Plan, nil
}

// Phase says where a statement belongs when a caller orders statements across
// changes: every drop, then every alter, then every create.
type Phase int

const (
	PhaseDrop Phase = iota
	PhaseAlter
	PhaseCreate
)

// PhasedChange is one planned statement and its phase.
type PhasedChange struct {
	*migrate.Change
	Phase Phase
}

// PlanPhased is PlanChanges with each statement's phase.
func (Planner) PlanPhased(_ context.Context, name string, changes []schema.Change, opts ...migrate.PlanOption) ([]PhasedChange, error) {
	s, err := planned(name, changes, opts)
	if err != nil {
		return nil, err
	}
	out := make([]PhasedChange, len(s.Changes))
	for i, c := range s.Changes {
		out[i] = PhasedChange{Change: c, Phase: s.phases[i]}
	}
	return out, nil
}

func planned(name string, changes []schema.Change, opts []migrate.PlanOption) (*state, error) {
	s := &state{Plan: migrate.Plan{Name: name}}
	for _, o := range opts {
		o(&s.PlanOptions)
	}
	if err := s.plan(changes); err != nil {
		return nil, err
	}
	return s, nil
}

// state is one planning run.
type state struct {
	migrate.Plan
	migrate.PlanOptions
	// phases holds the phase of each entry of Changes.
	phases []Phase
}

// settle gives every statement planned since the last call the phase.
func (s *state) settle(phase Phase) {
	for len(s.phases) < len(s.Changes) {
		s.phases = append(s.phases, phase)
	}
}

// changePhase is the phase of the statements a top-level change plans. Modules
// are created or altered after the tables they read, so their modification
// belongs to the create phase.
func changePhase(c schema.Change) Phase {
	switch c.(type) {
	case *schema.DropTable, *schema.DropProc, *schema.DropFunc, *schema.DropView, *schema.DropTrigger, *schema.DropObject:
		return PhaseDrop
	case *schema.AddTable, *schema.AddProc, *schema.ModifyProc, *schema.AddFunc, *schema.ModifyFunc,
		*schema.AddView, *schema.ModifyView, *schema.AddTrigger, *schema.ModifyTrigger, *schema.AddObject:
		return PhaseCreate
	}
	return PhaseAlter
}

func (s *state) plan(changes []schema.Change) error {
	if s.SchemaQualifier != nil {
		if err := sqlx.CheckChangesScope(s.PlanOptions, changes); err != nil {
			return err
		}
	}
	planned, err := sqlx.DetachCycles(changes)
	if err != nil {
		return err
	}
	planned, err = sqlx.SortChanges(planned, nil)
	if err != nil {
		return err
	}
	for _, c := range planned {
		if err := s.planChange(c); err != nil {
			return err
		}
		s.settle(changePhase(c))
	}
	return nil
}

func (s *state) planChange(c schema.Change) error {
	switch c := c.(type) {
	case *schema.AddTable:
		return s.addTable(c)
	case *schema.DropTable:
		s.append(c, fmt.Sprintf("drop %q table", c.T.Name), s.build("DROP TABLE").Table(c.T))
		return nil
	case *schema.ModifyTable:
		return s.modifyTable(c)
	case *schema.AddProc:
		return s.createOrAlter(c, c.P.Name, c.P.Attrs)
	case *schema.ModifyProc:
		return s.createOrAlter(c, c.To.Name, c.To.Attrs)
	case *schema.AddFunc:
		return s.createOrAlter(c, c.F.Name, c.F.Attrs)
	case *schema.ModifyFunc:
		return s.createOrAlter(c, c.To.Name, c.To.Attrs)
	case *schema.DropProc:
		s.append(c, fmt.Sprintf("drop %q procedure", c.P.Name), s.build("DROP PROCEDURE").Proc(c.P))
		return nil
	case *schema.DropFunc:
		s.append(c, fmt.Sprintf("drop %q function", c.F.Name), s.build("DROP FUNCTION").Func(c.F))
		return nil
	}
	if handled, err := s.tableTypeChange(c); handled {
		return err
	}
	if handled, err := s.sequenceChange(c); handled {
		return err
	}
	return s.moduleChange(c)
}

// createOrAlter runs the declared definition itself, as CREATE OR ALTER.
func (s *state) createOrAlter(c schema.Change, name string, attrs []schema.Attr) error {
	definition, err := definitionOf(name, attrs)
	if err != nil {
		return err
	}
	cmd, err := CreateOrAlter(definition.Text)
	if err != nil {
		return fmt.Errorf("sqlserver: routine %s: %w", name, err)
	}
	if quotedIdentifierOff(attrs) {
		cmd = quotedIdentifierOffBatch(cmd)
	}
	s.Changes = append(s.Changes, &migrate.Change{Cmd: cmd, Source: c, Comment: fmt.Sprintf("create or alter %q", name)})
	return nil
}

// quotedIdentifierOffBatch creates a module under QUOTED_IDENTIFIER OFF. The
// setting is captured when the module text is parsed, and a module must start
// its own batch, so the definition runs as a nested batch after the SET; the
// trailing SET restores the default for the rest of the session.
func quotedIdentifierOffBatch(definition string) string {
	return "SET QUOTED_IDENTIFIER OFF;\nEXEC sys.sp_executesql N'" + strings.ReplaceAll(definition, "'", "''") + "';\nSET QUOTED_IDENTIFIER ON;"
}

func (s *state) build(phrases ...string) *sqlx.Builder {
	b := &sqlx.Builder{QuoteOpening: '[', QuoteClosing: ']', Schema: s.SchemaQualifier, Indent: s.Indent}
	return b.P(phrases...)
}

func (s *state) append(source schema.Change, comment string, b *sqlx.Builder) {
	s.Changes = append(s.Changes, &migrate.Change{Cmd: b.String(), Source: source, Comment: comment})
}
