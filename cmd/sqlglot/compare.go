package main

import (
	"context"

	"github.com/flanksource/clicky"
	"github.com/flanksource/sqlglot-go/database/compare"
	"github.com/flanksource/sqlglot-go/database/connections"
	"github.com/flanksource/sqlglot-go/database/history"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

type CompareOptions struct {
	Reference      string   `json:"reference" flag:"reference" help:"Desired-schema connection (DSN or connection://[<namespace>/]<name>)"`
	Target         string   `json:"target" flag:"target" help:"Connection to patch toward the reference (DSN or connection://[<namespace>/]<name>)"`
	Schemas        []string `json:"schemas,omitempty" flag:"schemas" help:"Schemas to compare (defaults to main, public or dbo)"`
	Run            string   `json:"run,omitempty" flag:"run" help:"Run id to record this run under and stream its tasks by (defaults to a new UUID)"`
	ConnectionsDir string   `json:"connectionsDir,omitempty" flag:"connections-dir" help:"Directory of named connection YAML files (defaults to $XDG_CONFIG_HOME/sqlglot-go/connections)"`
}

type PatchOptions struct {
	CompareOptions
	Select     []string `json:"select" flag:"select" split:"false" help:"Change ID to carry into the patch (repeat for each change)"`
	DiffHash   string   `json:"diffHash" flag:"diff-hash" help:"diffHash of the reviewed comparison; the patch fails if the differences changed"`
	Comparison string   `json:"comparison,omitempty" flag:"comparison" help:"Run id of the recorded comparison the selection was reviewed in"`
}

// compareConnectionsDir is sqlglot's own connection layer when a request names
// none; the ui server points it at the layer it serves, so compare resolves the
// same layered connections the UI lists.
var compareConnectionsDir = connections.DefaultDir()

func (o CompareOptions) options(store *history.Store) compare.Options {
	dir := o.ConnectionsDir
	if dir == "" {
		dir = compareConnectionsDir
	}
	run := o.Run
	if run == "" {
		run = uuid.NewString()
	}
	return compare.Options{Reference: o.Reference, Target: o.Target, Schemas: o.Schemas, Run: run, ConnectionsDir: dir, History: store}
}

func addCompareCommands(schemaCmd *cobra.Command) {
	cmp := clicky.AddNamedCommandWithContext("compare", schemaCmd, CompareOptions{}, func(ctx context.Context, opts CompareOptions) (compare.Result, error) {
		return withHistory(func(store *history.Store) (compare.Result, error) { return compare.Compare(ctx, opts.options(store)) })
	})
	cmp.Short = "Compare the schemas of a reference and a target connection"
	patch := clicky.AddNamedCommandWithContext("patch", schemaCmd, PatchOptions{}, func(ctx context.Context, opts PatchOptions) (compare.Patch, error) {
		return withHistory(func(store *history.Store) (compare.Patch, error) {
			return compare.BuildPatch(ctx, compare.PatchOptions{Options: opts.options(store), Selection: opts.Select, DiffHash: opts.DiffHash, Comparison: opts.Comparison})
		})
	})
	patch.Short = "Build a reviewed artifact and SQL script from selected comparison changes"
}
