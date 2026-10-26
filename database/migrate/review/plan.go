package review

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"ariga.io/atlas/schemahcl"
	atlas "ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/postgres"
	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlite"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/changeset"
	"github.com/hashicorp/hcl/v2/hclparse"
)

type backend struct {
	driver  atlas.Driver
	marshal schemahcl.Marshaler
	eval    schemahcl.Evaluator
	dialect string
}

func open(q schema.ExecQuerier, dialect string) (backend, error) {
	b := backend{dialect: dialect}
	var err error
	switch dialect {
	case "sqlite":
		b.driver, err = sqlite.Open(q)
		b.marshal, b.eval = sqlite.MarshalHCL, sqlite.EvalHCL
	case "postgres":
		b.driver, err = postgres.Open(q)
		b.marshal, b.eval = postgres.MarshalHCL, postgres.EvalHCL
	default:
		return b, fmt.Errorf("unsupported reviewed schema dialect %q", dialect)
	}
	return b, err
}

func (b backend) decode(snapshot Snapshot) (*schema.Schema, error) {
	p := hclparse.NewParser()
	if _, diag := p.ParseHCL([]byte(snapshot.HCL), "snapshot.hcl"); diag.HasErrors() {
		return nil, fmt.Errorf("parse desired schema: %s", diag.Error())
	}
	realm := &schema.Realm{}
	if err := b.eval.Eval(p, realm, nil); err != nil {
		return nil, fmt.Errorf("evaluate snapshot: %w", err)
	}
	if len(realm.Schemas) != 1 || realm.Schemas[0].Name != snapshot.Schema {
		return nil, fmt.Errorf("snapshot must declare exactly schema %q", snapshot.Schema)
	}
	s := realm.Schemas[0]
	if len(s.Views)+len(s.Funcs)+len(s.Procs) > 0 || len(realm.Objects) > 0 {
		return nil, fmt.Errorf("unsupported non-table schema objects")
	}
	return s, nil
}

func (b backend) encode(s *schema.Schema, scope Snapshot) (Snapshot, error) {
	canonicalize(s)
	data, err := b.marshal.MarshalSpec(s)
	if err != nil {
		return Snapshot{}, fmt.Errorf("serialize schema: %w", err)
	}
	scope.HCL = string(data)
	roundtrip, err := b.decode(scope)
	if err != nil {
		return Snapshot{}, err
	}
	changes, err := b.diff(s, roundtrip)
	if err != nil {
		return Snapshot{}, err
	}
	if len(changes) > 0 {
		return Snapshot{}, fmt.Errorf("unsupported lossy schema snapshot: %d round-trip changes", len(changes))
	}
	return scope, nil
}

func (b backend) diff(current, desired *schema.Schema) ([]schema.Change, error) {
	changes, err := b.driver.SchemaDiff(current, desired)
	if err != nil {
		return nil, err
	}
	// Atlas ignores edited check expressions on some backends; bind them explicitly.
	for _, table := range desired.Tables {
		before, ok := current.Table(table.Name)
		if !ok {
			continue
		}
		var checks []schema.Change
		for _, check := range table.Checks() {
			if check.Name == "" {
				continue
			}
			var old *schema.Check
			for _, candidate := range before.Checks() {
				if candidate.Name == check.Name {
					old = candidate
					break
				}
			}
			if old == nil {
				continue
			}
			equal, err := sameCheck(old.Expr, check.Expr, b.dialect)
			if err != nil {
				return nil, err
			}
			if !equal {
				checks = append(checks, &schema.DropCheck{C: old}, &schema.AddCheck{C: check})
			}
		}
		if len(checks) == 0 {
			continue
		}
		var modified *schema.ModifyTable
		for _, change := range changes {
			if m, ok := change.(*schema.ModifyTable); ok && m.T.Name == table.Name {
				modified = m
				break
			}
		}
		if modified == nil {
			modified = &schema.ModifyTable{T: before}
			changes = append(changes, modified)
		}
		modified.Changes = append(modified.Changes, checks...)
	}
	return changes, nil
}

func Build(ctx context.Context, db *sql.DB, opts Options) (_ Artifact, returnErr error) {
	if db == nil {
		return Artifact{}, fmt.Errorf("database is nil")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return Artifact{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, conn.Close()) }()
	b, err := open(conn, opts.Dialect)
	if err != nil {
		return Artifact{}, err
	}
	scope := Snapshot{Dialect: opts.Dialect, Schema: opts.Schema}
	if err := validateScope(scope); err != nil {
		return Artifact{}, err
	}
	current, err := b.baseline(ctx, conn, scope, opts.Baseline)
	if err != nil {
		return Artifact{}, err
	}
	desired, err := b.decode(Snapshot{Dialect: opts.Dialect, Schema: opts.Schema, HCL: opts.DesiredHCL})
	if err != nil {
		return Artifact{}, err
	}
	if opts.PreserveOmitted {
		preserveOmitted(current, desired)
	}
	baseline, err := b.encode(current, scope)
	if err != nil {
		return Artifact{}, err
	}
	after, err := b.encode(desired, scope)
	if err != nil {
		return Artifact{}, err
	}
	selection := slices.Sorted(slices.Values(opts.Selection))
	steps, changes, err := b.plan(ctx, current, desired, selection)
	if err != nil {
		return Artifact{}, err
	}
	a := Artifact{Version: 2, Baseline: baseline, Desired: after, Changes: changes, Selection: selection, Steps: steps, Advisory: opts.Advisory}
	a.Hash, err = a.digest()
	return a, err
}

// baseline decodes a reviewed snapshot when one is given, and otherwise
// inspects the live schema.
func (b backend) baseline(ctx context.Context, q schema.ExecQuerier, scope Snapshot, reviewed *Snapshot) (*schema.Schema, error) {
	if reviewed == nil {
		return inspect(ctx, q, b, scope)
	}
	if reviewed.Dialect != scope.Dialect || reviewed.Schema != scope.Schema {
		return nil, fmt.Errorf("baseline snapshot scope %s/%s differs from %s/%s", reviewed.Dialect, reviewed.Schema, scope.Dialect, scope.Schema)
	}
	return b.decode(*reviewed)
}

// plan describes every change between current and desired and plans the
// selected ones (all when selection is empty).
func (b backend) plan(ctx context.Context, current, desired *schema.Schema, selection []string) ([]Step, []migrate.Change, error) {
	diff, err := b.diff(current, desired)
	if err != nil {
		return nil, nil, err
	}
	set, err := changeset.Describe(diff, b.changeDialect())
	if err != nil {
		return nil, nil, err
	}
	selected, err := set.Select(diff, selection)
	if err != nil {
		return nil, nil, err
	}
	plan, err := b.driver.PlanChanges(ctx, "reviewed-schema", selected)
	if err != nil {
		return nil, nil, fmt.Errorf("plan schema: %w", err)
	}
	if !plan.Transactional {
		return nil, nil, fmt.Errorf("nontransactional plans require phased migration artifacts")
	}
	steps := make([]Step, 0, len(plan.Changes))
	for _, change := range plan.Changes {
		if change.Cmd == "PRAGMA foreign_keys = off" || change.Cmd == "PRAGMA foreign_keys = on" {
			continue
		}
		if len(change.Args) > 0 {
			return nil, nil, fmt.Errorf("unsupported parameterized schema migration")
		}
		var ids []string
		if change.Source == nil && len(steps) > 0 {
			// SQLite's rebuild row copy has no source; it continues the rebuild before it.
			ids = steps[len(steps)-1].Changes
		} else if ids, err = set.Lookup(change.Source); err != nil {
			return nil, nil, err
		}
		id := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d:%s", len(steps), change.Cmd))))
		steps = append(steps, Step{ID: id, SQL: change.Cmd, Comment: change.Comment, Destructive: destructive(change.Source), Changes: ids})
	}
	return steps, set.Changes, nil
}

func destructive(change schema.Change) bool {
	switch c := change.(type) {
	case *schema.DropTable, *schema.DropColumn, *schema.DropObject, *schema.DropIndex, *schema.DropPrimaryKey, *schema.DropForeignKey, *schema.DropCheck:
		return true
	case *schema.ModifyColumn:
		return c.Change.Is(schema.ChangeType) || c.Change.Is(schema.ChangeNull)
	case *schema.ModifyTable:
		for _, inner := range c.Changes {
			if destructive(inner) {
				return true
			}
		}
	case *schema.ModifyObject, *schema.ModifyPrimaryKey, *schema.ModifyForeignKey, *schema.ModifyCheck:
		return true
	}
	return false
}

func sameSteps(a, b []Step) bool { return reflect.DeepEqual(a, b) }

func canonicalize(s *schema.Schema) {
	slices.SortFunc(s.Tables, func(a, b *schema.Table) int { return strings.Compare(a.Name, b.Name) })
	for _, t := range s.Tables {
		slices.SortFunc(t.Indexes, func(a, b *schema.Index) int { return strings.Compare(a.Name, b.Name) })
		slices.SortFunc(t.ForeignKeys, func(a, b *schema.ForeignKey) int { return strings.Compare(a.Symbol, b.Symbol) })
		slices.SortStableFunc(t.Attrs, func(a, b schema.Attr) int { return strings.Compare(attributeKey(a), attributeKey(b)) })
	}
	slices.SortStableFunc(s.Objects, func(a, b schema.Object) int {
		left, right := fmt.Sprintf("%T", a), fmt.Sprintf("%T", b)
		if e, ok := a.(*schema.EnumType); ok {
			left += e.T
		}
		if e, ok := b.(*schema.EnumType); ok {
			right += e.T
		}
		return strings.Compare(left, right)
	})
}

func attributeKey(a schema.Attr) string {
	if check, ok := a.(*schema.Check); ok {
		return "check:" + check.Name + ":" + check.Expr
	}
	return fmt.Sprintf("%T", a)
}

func preserveOmitted(current, desired *schema.Schema) {
	for _, before := range current.Tables {
		after, ok := desired.Table(before.Name)
		if !ok {
			desired.AddTables(before)
			continue
		}
		for _, column := range before.Columns {
			if _, ok := after.Column(column.Name); !ok {
				after.AddColumns(column)
			}
		}
	}
	for _, object := range current.Objects {
		enum, ok := object.(*schema.EnumType)
		if !ok {
			continue
		}
		found := false
		for _, candidate := range desired.Objects {
			if e, ok := candidate.(*schema.EnumType); ok && e.T == enum.T {
				found = true
				break
			}
		}
		if !found {
			desired.AddObjects(object)
		}
	}
}
