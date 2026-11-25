// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package sqlx

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"
)

type (
	execPlanner interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
		PlanChanges(context.Context, string, []schema.Change, ...migrate.PlanOption) (*migrate.Plan, error)
	}
	// ApplyError is an error that exposes an information for getting
	// how any changes were applied before encountering the failure.
	ApplyError struct {
		err     string
		applied int
	}
)

// Applied reports how many changes were applied before getting an error.
// In case the first change was failed, Applied() returns 0.
func (e *ApplyError) Applied() int {
	return e.applied
}

// Error implements the error interface.
func (e *ApplyError) Error() string {
	return e.err
}

// ApplyChanges is a helper used by the different drivers to apply changes.
func ApplyChanges(ctx context.Context, changes []schema.Change, p execPlanner, opts ...migrate.PlanOption) error {
	plan, err := p.PlanChanges(ctx, "apply", changes, opts...)
	if err != nil {
		return err
	}
	for i, c := range plan.Changes {
		if _, err := p.ExecContext(ctx, c.Cmd, c.Args...); err != nil {
			if c.Comment != "" {
				err = fmt.Errorf("%s: %w", c.Comment, err)
			}
			return &ApplyError{err: err.Error(), applied: i}
		}
	}
	return nil
}

// noRows implements the schema.ExecQuerier for migrate.Driver's without connections.
// This can be useful to always return no rows for queries, and block any execution.
type noRows struct{}

// QueryContext implements the sqlx.ExecQuerier interface.
func (*noRows) QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error) {
	return nil, sql.ErrNoRows
}

// ExecContext implements the sqlx.ExecQuerier interface.
func (*noRows) ExecContext(context.Context, string, ...interface{}) (sql.Result, error) {
	return nil, errors.New("cannot execute statements without a database connection. use Open to create a new Driver")
}

// NoRows to be used by differs and planners without a connection.
var NoRows schema.ExecQuerier = (*noRows)(nil)

// SameSchema reports if the given schemas are the same.
// Objects can be different as they might reside in two
// different states (current and desired).
func SameSchema(s1, s2 *schema.Schema) bool {
	if s1 == nil || s2 == nil {
		return s1 == s2
	}
	return s1.Name == s2.Name
}
