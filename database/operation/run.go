package operation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/task"
	commonscontext "github.com/flanksource/commons/context"
	"github.com/flanksource/sqlglot-go/database/history"
	"github.com/google/uuid"
)

type Options struct {
	Run    string
	Parent string
	Kind   history.Kind
	Target string
	Name   string
	Store  *history.Store
}

func Run[T any](ctx context.Context, opts Options, fn func(context.Context, *history.Record, *task.Task) (T, error)) (T, error) {
	var zero T
	if opts.Store == nil {
		return zero, fmt.Errorf("history store is required")
	}
	if opts.Run == "" {
		opts.Run = uuid.NewString()
	}
	if err := opts.Store.Claim(ctx, opts.Run); err != nil {
		return zero, err
	}
	group := clicky.StartGroup[T](opts.Name, task.WithGroupID(opts.Run), task.WithKind("sqlglot-"+string(opts.Kind)))
	run := group.Add(opts.Name, func(taskCtx commonscontext.Context, t *task.Task) (T, error) {
		record := history.Record{ID: opts.Run, Parent: opts.Parent, Kind: opts.Kind, Target: opts.Target, CreatedAt: time.Now().UTC()}
		value, err := fn(taskCtx, &record, t)
		if completeErr := record.Complete(value, err); completeErr != nil {
			err = errors.Join(err, completeErr)
		}
		err = errors.Join(err, opts.Store.Save(context.WithoutCancel(taskCtx), record))
		return value, err
	}, task.WithContext(ctx), task.WithCancellationDrain(), task.WithRetryConfig(task.RetryConfig{MaxRetries: 0}))
	value, err := run.GetResult()
	group.WaitFor()
	return value, err
}
