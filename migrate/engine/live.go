package engine

import (
	"context"
	"fmt"
	"slices"

	"github.com/flanksource/sqlglot-go/migrate"
)

type PlanLiveRequest struct {
	Dialect   string
	Scope     Scope
	Desired   migrate.Schema
	Selection []string
	Atomic    bool
}

// PlanLive reads capabilities, the current schema and the live dependents of
// the columns the selected changes force, then plans. Dependents that sit on
// objects outside the scope are inspected again and carried as context, so the
// baseline holds everything a unit will touch. Inspection refusals go to Plan,
// which fails only when a planned change or rebuild touches one.
func PlanLive(ctx context.Context, q Querier, req PlanLiveRequest) (Artifact, error) {
	d, err := Lookup(req.Dialect)
	if err != nil {
		return Artifact{}, err
	}
	caps, err := d.Capabilities(ctx, q)
	if err != nil {
		return Artifact{}, fmt.Errorf("engine: %s capabilities: %w", req.Dialect, err)
	}
	scope := req.Scope
	if scope.Mode == ScopeDeclared && len(scope.Objects) == 0 {
		scope.Objects = canonicalNames(schemaObjects(req.Desired))
	}
	insp, err := d.Inspect(ctx, q, scope)
	if err != nil {
		return Artifact{}, fmt.Errorf("engine: %s inspect: %w", req.Dialect, err)
	}
	desired, err := d.Reconcile(ctx, q, insp.Schema, req.Desired)
	if err != nil {
		return Artifact{}, fmt.Errorf("engine: %s reconcile: %w", req.Dialect, err)
	}
	plan := PlanRequest{
		Dialect: req.Dialect, Scope: scope, Current: insp.Schema, Desired: desired,
		Selection: req.Selection, Refusals: insp.Refusals, Capabilities: caps, Atomic: req.Atomic,
	}
	columns, err := selectedForcingColumns(d, plan)
	if err != nil {
		return Artifact{}, err
	}
	if len(columns) > 0 {
		if err = readDependents(ctx, d, q, &plan, columns); err != nil {
			return Artifact{}, err
		}
		if len(plan.Context) > 0 {
			if plan.Desired, err = d.Reconcile(ctx, q, plan.Current, req.Desired); err != nil {
				return Artifact{}, fmt.Errorf("engine: %s reconcile: %w", req.Dialect, err)
			}
		}
	}
	return Plan(plan)
}

func readDependents(ctx context.Context, d Dialect, q Querier, plan *PlanRequest, columns []ColumnRef) error {
	var err error
	if plan.Dependents, err = d.Dependents(ctx, q, plan.Current, columns); err != nil {
		return fmt.Errorf("engine: %s dependents: %w", plan.Dialect, err)
	}
	return addContext(ctx, d, q, plan)
}

// addContext inspects the tables and views the live dependents sit on (and the
// tables their foreign keys reference) when the scope's inspection did not
// return them, and merges what it finds into plan.Current as context.
func addContext(ctx context.Context, d Dialect, q Querier, plan *PlanRequest) error {
	have := schemaObjects(plan.Current)
	var missing []migrate.Name
	for _, dep := range plan.Dependents {
		names := []migrate.Name{dep.Table}
		if dep.ForeignKey != nil {
			names = append(names, dep.ForeignKey.References)
		}
		for _, n := range names {
			if !slices.Contains(have, n) && !slices.Contains(missing, n) {
				missing = append(missing, n)
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	insp, err := d.Inspect(ctx, q, Scope{Mode: ScopeDeclared, Schemas: plan.Scope.Schemas, Objects: missing})
	if err != nil {
		return fmt.Errorf("engine: %s inspect dependent objects: %w", plan.Dialect, err)
	}
	found := narrow(insp.Schema, missing)
	plan.Current.Tables = append(plan.Current.Tables, found.Tables...)
	plan.Current.Definitions = append(plan.Current.Definitions, found.Definitions...)
	plan.Current.Sequences = append(plan.Current.Sequences, found.Sequences...)
	plan.Current.TableTypes = append(plan.Current.TableTypes, found.TableTypes...)
	plan.Context = schemaObjects(found)
	plan.Refusals = append(plan.Refusals, insp.Refusals...)
	return nil
}

func selectedForcingColumns(d Dialect, req PlanRequest) ([]ColumnRef, error) {
	req, err := canonicalRequest(req)
	if err != nil {
		return nil, err
	}
	diff, err := d.Diff(req.Current, req.Desired)
	if err != nil {
		return nil, fmt.Errorf("engine: %s diff: %w", req.Dialect, err)
	}
	res, err := resolveChanges(diff, req)
	if err != nil {
		return nil, err
	}
	forcings, err := selectedForcings(diff, res.selected)
	if err != nil {
		return nil, err
	}
	return forcingColumns(forcings), nil
}

func forcingColumns(forcings []Forcing) []ColumnRef {
	var columns []ColumnRef
	for _, f := range forcings {
		columns = append(columns, f.Columns...)
	}
	slices.SortFunc(columns, compareColumnRef)
	return slices.Compact(columns)
}
