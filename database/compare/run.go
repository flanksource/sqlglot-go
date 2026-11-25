package compare

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/task"
	flanksourceContext "github.com/flanksource/commons/context"
	"github.com/flanksource/sqlglot-go/database/history"
	dialects "github.com/flanksource/sqlglot-go/database/migrate/dialect"
	dbtarget "github.com/flanksource/sqlglot-go/database/target"
)

// side is one opened connection and the schema inspected from it.
type side struct {
	conn     dbtarget.Connection
	schemas  []string
	snapshot dialects.Snapshot
}

// noRetry keeps a failed connect or inspection failing fast instead of
// clicky's default retry of connection-like errors.
var noRetry = task.WithRetryConfig(task.RetryConfig{MaxRetries: 0})

// run is one comparison or patch: a clicky group keyed by the run id, whose
// first two tasks open and inspect both sides concurrently. Every task is
// added before any finishes, so the group is terminal only once the run is.
type run struct {
	ctx               context.Context
	opts              Options
	kind              history.Kind
	started           time.Time
	group             task.TypedGroup[any]
	reference, target task.TypedTask[any]
	tasks             []task.TypedTask[any]
}

func startRun(ctx context.Context, opts Options, kind history.Kind) (*run, error) {
	switch {
	case opts.Reference == "" || opts.Target == "":
		return nil, fmt.Errorf("both a reference and a target connection are required")
	case opts.Reference == opts.Target:
		return nil, fmt.Errorf("reference and target are the same connection %s", opts.Reference)
	case opts.Run == "":
		return nil, fmt.Errorf("a run id is required")
	case opts.History == nil:
		return nil, fmt.Errorf("a history store is required")
	}
	recorded, err := opts.History.Has(ctx, opts.Run)
	if err != nil {
		return nil, err
	}
	if recorded || len(task.SnapshotByID(opts.Run)) > 0 {
		return nil, fmt.Errorf("run %s already exists", opts.Run)
	}
	labels := map[string]string{"reference": label(opts.Reference), "target": label(opts.Target)}
	r := &run{ctx: ctx, opts: opts, kind: kind, started: time.Now().UTC()}
	r.group = clicky.StartGroup[any](fmt.Sprintf("%s %s with %s", strings.ToUpper(string(kind[:1]))+string(kind[1:]), labels["reference"], labels["target"]),
		task.WithGroupID(opts.Run), task.WithKind("sqlglot-"+string(kind)), task.WithLabels(labels))
	inspect := func(role, dsn string) task.TypedTask[any] {
		return r.add(strings.TrimSpace("Inspect "+role+" "+label(dsn)), func(taskCtx context.Context, t *task.Task) (any, error) {
			return inspectSide(taskCtx, t, role, dsn, opts)
		})
	}
	r.reference, r.target = inspect("reference", opts.Reference), inspect("target", opts.Target)
	return r, nil
}

// add registers a task that starts once every task in after has finished, and
// is cancelled when one of them fails.
func (r *run) add(name string, fn func(context.Context, *task.Task) (any, error), after ...*task.Task) task.TypedTask[any] {
	t := r.group.Add(name, func(taskCtx flanksourceContext.Context, t *task.Task) (any, error) {
		return fn(taskCtx, t)
	}, task.WithContext(r.ctx), noRetry, task.WithDependencies(after...))
	r.tasks = append(r.tasks, t)
	return t
}

// sides pairs both inspections; it is called from a task that depends on both.
func (r *run) sides() (*sides, error) {
	reference, referenceErr := result[side](r.reference)
	target, targetErr := result[side](r.target)
	if err := errors.Join(referenceErr, targetErr); err != nil {
		return nil, err
	}
	if reference.conn.Dialect != target.conn.Dialect {
		return nil, fmt.Errorf("reference dialect %s differs from target dialect %s", reference.conn.Dialect, target.conn.Dialect)
	}
	if !slices.Equal(reference.schemas, target.schemas) {
		return nil, fmt.Errorf("reference schemas %v differ from target schemas %v; name the schemas to compare", reference.schemas, target.schemas)
	}
	return &sides{dialect: target.conn.Dialect, schemas: target.schemas, target: target.conn, current: target.snapshot, reference: reference.snapshot}, nil
}

// failure reports why the run failed: the errors of the tasks that failed,
// rather than the "dependency failed" their dependents were cancelled with.
func (r *run) failure(err error) error {
	var failed []error
	for _, t := range r.tasks {
		if _, taskErr := t.GetResult(); taskErr != nil && t.Status() == task.StatusFailed {
			failed = append(failed, taskErr)
		}
	}
	if len(failed) > 0 {
		return errors.Join(failed...)
	}
	return err
}

// close closes every connection the inspections opened.
func (r *run) close() error {
	var errs []error
	for _, t := range []task.TypedTask[any]{r.reference, r.target} {
		if s, err := result[side](t); err == nil {
			errs = append(errs, s.conn.DB.Close())
		}
	}
	return errors.Join(errs...)
}

// result waits for t and returns its value as T.
func result[T any](t task.TypedTask[any]) (T, error) {
	var zero T
	value, err := t.GetResult()
	if err != nil {
		return zero, err
	}
	typed, ok := value.(T)
	if !ok {
		return zero, fmt.Errorf("task %s returned %T, expected %T", t.Name(), value, zero)
	}
	return typed, nil
}

// label names a side in task output without credentials: the connection
// name, the SQLite file, or the host and path of a URL DSN.
func label(dsn string) string {
	if name, ok := strings.CutPrefix(dsn, dbtarget.ConnectionPrefix); ok {
		return name
	}
	if strings.HasPrefix(strings.ToLower(dsn), "sqlite://") {
		target, err := dbtarget.Parse(dsn)
		if err != nil {
			return ""
		}
		u, err := url.Parse(target.DSN)
		if err != nil {
			return ""
		}
		filename := u.Path
		if filename == "" {
			filename = u.Opaque
		}
		return path.Base(strings.ReplaceAll(filename, `\`, "/"))
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" || u.Opaque != "" {
		return ""
	}
	return u.Host + u.Path
}

// inspectSide opens one connection and inspects its schema scope, closing the
// connection again when inspection fails.
func inspectSide(ctx context.Context, t *task.Task, role, dsn string, opts Options) (side, error) {
	t.SetProgress(0, 0)
	t.SetDescription("connecting")
	conn, err := dbtarget.Open(ctx, dbtarget.OpenOptions{DSN: dsn, ResolveConnection: opts.ResolveConnection, ReadOnly: true, Schema: first(opts.Schemas)})
	if err != nil {
		return side{}, fmt.Errorf("open %s %s: %w", role, history.Redact(dsn), err)
	}
	s := side{conn: conn, schemas: opts.Schemas}
	if len(s.schemas) == 0 {
		s.schemas = []string{conn.Schema}
	}
	t.SetDescription("inspecting " + strings.Join(s.schemas, ", "))
	if s.snapshot, err = dialects.Inspect(ctx, conn.DB, conn.Dialect, s.schemas); err != nil {
		return side{}, errors.Join(fmt.Errorf("inspect %s: %w", role, err), conn.DB.Close())
	}
	t.SetDescription(describe(s.snapshot.Summary()))
	return s, nil
}

// describe lists a summary's counts, most numerous first, as "2 table, 1 view".
func describe(summary map[string]int) string {
	kinds := slices.Collect(maps.Keys(summary))
	slices.SortFunc(kinds, func(a, b string) int { return cmp.Or(cmp.Compare(summary[b], summary[a]), cmp.Compare(a, b)) })
	parts := make([]string, len(kinds))
	for i, kind := range kinds {
		parts[i] = fmt.Sprintf("%d %s", summary[kind], kind)
	}
	return strings.Join(parts, ", ")
}
