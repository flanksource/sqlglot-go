package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/sqlglot-go/database/history"
	"github.com/spf13/cobra"
)

// historyPath is the run history every command records to and the ui serves;
// it is set by the root --history flag.
var historyPath = history.DefaultPath()

type HistoryListOptions struct {
	Kind       string `json:"kind,omitempty" flag:"kind" help:"Only runs of this kind: inspection, comparison, patch or migration"`
	Connection string `json:"connection,omitempty" flag:"connection" help:"Only runs that touched this connection (DSN or connection:// reference)"`
	Limit      int    `json:"limit,omitempty" flag:"limit" help:"Most runs to list (default 200)"`
}

type HistoryGetOptions struct {
	ID string `json:"id" args:"true" help:"Run id"`
}

// withHistory opens the run history for one call.
func withHistory[T any](fn func(*history.Store) (T, error)) (_ T, returnErr error) {
	store, err := history.Open(historyPath)
	if err != nil {
		var zero T
		return zero, err
	}
	defer func() { returnErr = errors.Join(returnErr, store.Close()) }()
	return fn(store)
}

func addHistoryCommands(root *cobra.Command) {
	root.PersistentFlags().StringVar(&historyPath, "history", historyPath, "Run history file recording every inspection, comparison, patch and migration")
	historyCmd := &cobra.Command{Use: "history", Short: "Review recorded inspections, comparisons, patches and migrations, offline"}
	root.AddCommand(historyCmd)
	list := clicky.AddNamedCommandWithContext("list", historyCmd, HistoryListOptions{}, func(ctx context.Context, opts HistoryListOptions) ([]history.Record, error) {
		kind := history.Kind(opts.Kind)
		switch kind {
		case "", history.KindInspection, history.KindComparison, history.KindPatch, history.KindMigration:
		default:
			return nil, fmt.Errorf("unknown run kind %q", opts.Kind)
		}
		return withHistory(func(store *history.Store) ([]history.Record, error) {
			return store.List(ctx, history.Filter{Kind: kind, Connection: opts.Connection, Limit: opts.Limit})
		})
	})
	list.Short = "List recorded runs, newest first"
	get := clicky.AddNamedCommandWithContext("get", historyCmd, HistoryGetOptions{}, func(ctx context.Context, opts HistoryGetOptions) (history.Record, error) {
		if opts.ID == "" {
			return history.Record{}, fmt.Errorf("a run id is required")
		}
		return withHistory(func(store *history.Store) (history.Record, error) {
			record, err := store.Get(ctx, opts.ID)
			if errors.Is(err, history.ErrNotFound) {
				return record, entity.NewStatusErrorf(http.StatusNotFound, "not_found", "%v", err)
			}
			return record, err
		})
	})
	// Naming the positional id in Use routes it as GET /history/{id}.
	get.Use, get.Short, get.Args = "get <id>", "Show one recorded run with its result", cobra.ExactArgs(1)
}
