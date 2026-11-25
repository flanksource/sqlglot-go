package sqlserver

import (
	"context"
	"fmt"
	"regexp"
	"slices"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
	atlas "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// Lower plans the selected changes in one Atlas batch, so Atlas sorts them and
// detaches cycles, then re-creates each forced rebuild from its live
// definition. A nil selection lowers every change; a non-nil empty one lowers
// none (the engine passes an empty selection when every change is preserved).
func (p *planDiff) Lower(selection []string, rebuilds []engine.Rebuild) ([]engine.Op, error) {
	selected, err := p.lowerSelected(selection)
	if err != nil {
		return nil, err
	}
	rebuilt, err := p.lowerRebuilds(selected.ops, rebuilds)
	if err != nil {
		return nil, err
	}
	all := selected.concat(rebuilt)
	linkClusteredFirst(all)
	linkNewForeignKeys(all, rebuilds)
	return all.ops, nil
}

func (p *planDiff) lowerSelected(selection []string) (loweredOps, error) {
	if selection != nil && len(selection) == 0 {
		return loweredOps{}, nil
	}
	selected, err := p.set.Select(p.diff, selection)
	if err != nil {
		return loweredOps{}, err
	}
	steps, err := atlas.DefaultPlan.PlanPhased(context.Background(), "migration", selected)
	if err != nil {
		return loweredOps{}, err
	}
	g := &opGrouper{set: p, count: map[string]int{}, lastOf: map[string]int{}, created: map[migrate.Name]int{}}
	for _, step := range steps {
		if err := g.add(step); err != nil {
			return loweredOps{}, err
		}
	}
	return g.finish(), nil
}

// opGrouper turns planned statements into ops: consecutive statements of one
// change and phase share an op, so Atlas's own statement order survives.
type opGrouper struct {
	set *planDiff
	loweredOps
	count   map[string]int
	lastOf  map[string]int // change -> its latest op
	created map[migrate.Name]int
}

func (g *opGrouper) add(step atlas.PhasedChange) error {
	if len(step.Args) > 0 {
		return fmt.Errorf("sqlserver: planned statement %q binds arguments, which a reviewed plan cannot hold", step.Cmd)
	}
	ids, err := g.set.set.Lookup(step.Source)
	if err != nil {
		return err
	}
	if len(ids) != 1 {
		return fmt.Errorf("sqlserver: planned statement %q implements %d changes", step.Cmd, len(ids))
	}
	change, _ := g.set.set.Change(ids[0])
	phase := enginePhase(step.Phase)
	i := g.opFor(change, phase)
	g.ops[i].Statements = append(g.ops[i].Statements, engine.Statement{SQL: step.Cmd, SizeOfData: readsEveryRow(step.Cmd)})
	creates, refs := sourceTables(step.Source)
	if creates != (migrate.Name{}) {
		g.created[creates] = i
	}
	if phase == engine.PhaseCreate {
		g.refs[i] = append(g.refs[i], refs...)
	}
	if role, ok := indexRoleOf(step.Source, phase); ok {
		g.roles[i] = append(g.roles[i], role)
	}
	return nil
}

// opFor returns the op the next statement of change in phase joins.
func (g *opGrouper) opFor(change migrate.Change, phase engine.Phase) int {
	if n := len(g.ops); n > 0 && g.ops[n-1].Change == change.ID && g.ops[n-1].Phase == phase {
		return n - 1
	}
	key := string(phase) + ":" + change.ID
	g.count[key]++
	id := key
	if g.count[key] > 1 {
		id = fmt.Sprintf("%s#%d", key, g.count[key])
	}
	op := engine.Op{ID: id, Change: change.ID, Phase: phase, Object: change.Object}
	if prev, ok := g.lastOf[change.ID]; ok {
		op.Requires = []string{g.ops[prev].ID}
	}
	g.ops = append(g.ops, op)
	g.refs = append(g.refs, nil)
	g.roles = append(g.roles, nil)
	g.lastOf[change.ID] = len(g.ops) - 1
	return len(g.ops) - 1
}

// finish makes each op that creates a foreign key wait for the ops that create
// the tables it references.
func (g *opGrouper) finish() loweredOps {
	for i, refs := range g.refs {
		for _, ref := range refs {
			j, ok := g.created[ref]
			if ok && j != i && !slices.Contains(g.ops[i].Requires, g.ops[j].ID) {
				g.ops[i].Requires = append(g.ops[i].Requires, g.ops[j].ID)
			}
		}
	}
	return g.loweredOps
}

func enginePhase(p atlas.Phase) engine.Phase {
	switch p {
	case atlas.PhaseDrop:
		return engine.PhaseDrop
	case atlas.PhaseCreate:
		return engine.PhaseCreate
	}
	return engine.PhaseAlter
}

// sourceTables names the table a source change creates and those its foreign
// keys reference.
func sourceTables(source schema.Change) (creates migrate.Name, refs []migrate.Name) {
	switch c := source.(type) {
	case *schema.AddTable:
		for _, fk := range c.T.ForeignKeys {
			refs = append(refs, tableNameOf(fk.RefTable))
		}
		return tableNameOf(c.T), refs
	case *schema.AddForeignKey:
		return migrate.Name{}, []migrate.Name{tableNameOf(c.F.RefTable)}
	case *schema.ModifyForeignKey:
		return migrate.Name{}, []migrate.Name{tableNameOf(c.To.RefTable)}
	}
	return migrate.Name{}, nil
}

func tableNameOf(t *schema.Table) migrate.Name {
	if t == nil || t.Schema == nil {
		return migrate.Name{}
	}
	return migrate.Name{Schema: t.Schema.Name, Name: t.Name}
}

const bracketed = `\[(?:[^\]]|\]\])*\]`

// everyRow matches the statements that read or rewrite each row of a table:
// ALTER COLUMN, building an index or key, and validating a new constraint.
var everyRow = regexp.MustCompile(`(?i)^(` +
	`ALTER TABLE ` + bracketed + `\.` + bracketed + ` ALTER COLUMN ` +
	`|ALTER TABLE ` + bracketed + `\.` + bracketed + ` (WITH CHECK )?ADD CONSTRAINT ` + bracketed + ` (PRIMARY KEY|UNIQUE|FOREIGN KEY|CHECK)\b` +
	`|CREATE (UNIQUE )?(CLUSTERED |NONCLUSTERED )?INDEX ` +
	`|ALTER INDEX )`)

func readsEveryRow(sql string) bool { return everyRow.MatchString(sql) }
