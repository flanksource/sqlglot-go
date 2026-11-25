package compare

import (
	"context"
	"errors"

	"github.com/flanksource/clicky/task"
	"github.com/flanksource/sqlglot-go/database/history"
)

// finish is a run's outcome: its value and listing summary, or its error.
type finish struct {
	parent  string
	value   any
	summary map[string]int
	err     error
}

// finish closes the run's connections and records the run and each side's
// inspection, so the run can be reviewed with both databases offline. A run
// that failed is recorded too. It returns the run's error, if any.
func (r *run) finish(ctx context.Context, f finish) error {
	if f.err != nil {
		f.err, f.value = r.failure(f.err), nil
	}
	ctx = context.WithoutCancel(ctx)
	main := history.Record{ID: r.opts.Run, Kind: r.kind, Parent: f.parent, Reference: r.opts.Reference, Target: r.opts.Target,
		Schemas: r.opts.Schemas, Summary: f.summary, CreatedAt: r.started}
	if err := main.Complete(f.value, f.err); err != nil {
		return errors.Join(f.err, err, r.close())
	}
	records := []history.Record{}
	for _, inspected := range []struct {
		role, dsn string
		task      task.TypedTask[any]
	}{{"reference", r.opts.Reference, r.reference}, {"target", r.opts.Target, r.target}} {
		inspection := history.Record{ID: r.opts.Run + "/" + inspected.role, Kind: history.KindInspection, Parent: r.opts.Run, Target: inspected.dsn, Schemas: r.opts.Schemas, CreatedAt: r.started}
		s, err := result[side](inspected.task)
		if err == nil {
			inspection.Dialect, inspection.Schemas = s.conn.Dialect, s.schemas
			main.Dialect, main.Schemas = s.conn.Dialect, s.schemas
			inspection.Summary = s.snapshot.Summary()
		}
		var payload any
		if err == nil {
			payload = s.snapshot
		}
		if completeErr := inspection.Complete(payload, err); completeErr != nil {
			return errors.Join(f.err, completeErr, r.close())
		}
		records = append(records, inspection)
	}
	errs := []error{f.err, r.close()}
	for _, record := range append([]history.Record{main}, records...) {
		errs = append(errs, r.opts.History.Save(ctx, record))
	}
	// The run's task progress and logs are stored with it here rather than
	// left to clicky's background writer, which a CLI process exits before.
	errs = append(errs, r.opts.History.SaveRun(ctx, r.opts.Run, task.SnapshotByID(r.opts.Run)))
	return errors.Join(errs...)
}

func comparisonSummary(r Result) map[string]int {
	summary := map[string]int{"changes": len(r.Changes), "added": 0, "dropped": 0, "modified": 0, "destructive": 0, "objects": len(r.Objects)}
	for _, c := range r.Changes {
		switch c.Action {
		case "add":
			summary["added"]++
		case "drop":
			summary["dropped"]++
		case "modify":
			summary["modified"]++
		}
		if c.Destructive {
			summary["destructive"]++
		}
	}
	return summary
}
