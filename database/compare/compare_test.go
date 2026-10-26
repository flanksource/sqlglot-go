package compare_test

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	clickytask "github.com/flanksource/clicky/task"
	"github.com/flanksource/sqlglot-go/database/compare"
	"github.com/flanksource/sqlglot-go/database/history"
	"github.com/flanksource/sqlglot-go/database/migrate/review"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	_ "modernc.org/sqlite"
)

func TestCompare(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Connection schema comparison")
}

const (
	referenceDDL = `CREATE TABLE items (id integer NOT NULL, label text NOT NULL, note text);
CREATE INDEX idx_items_label ON items (label);
CREATE TABLE tags (id integer NOT NULL)`
	targetDDL = `CREATE TABLE items (id integer NOT NULL, label text NOT NULL)`
)

var (
	addTags  = `["add","table","main","tags",""]`
	addNote  = `["add","column","main","items","note"]`
	addIndex = `["add","index","main","items","idx_items_label"]`
)

func sqliteFile(ddl string) string {
	path := filepath.Join(GinkgoT().TempDir(), "side.db")
	db, err := sql.Open("sqlite", path)
	Expect(err).NotTo(HaveOccurred())
	_, err = db.Exec(ddl)
	Expect(err).NotTo(HaveOccurred())
	Expect(db.Close()).To(Succeed())
	return "sqlite://" + path
}

// withRun gives each comparison or patch its own run id.
func withRun(opts compare.Options) compare.Options {
	opts.Run = uuid.NewString()
	return opts
}

var _ = Describe("Comparing two connections", func() {
	var (
		opts  compare.Options
		store *history.Store
	)
	BeforeEach(func() {
		var err error
		store, err = history.Open(filepath.Join(GinkgoT().TempDir(), "history.db"))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(store.Close)
		opts = withRun(compare.Options{Reference: sqliteFile(referenceDDL), Target: sqliteFile(targetDDL), History: store})
	})
	It("records the comparison and the inspection of each side under the run id", func(ctx SpecContext) {
		result, err := compare.Compare(ctx, opts)
		Expect(err).NotTo(HaveOccurred())

		recorded, err := store.Get(ctx, opts.Run)
		Expect(err).NotTo(HaveOccurred())
		var payload compare.Result
		Expect(json.Unmarshal(recorded.Payload, &payload)).To(Succeed())
		Expect(payload).To(Equal(result))
		Expect([]any{recorded.Kind, recorded.Status, recorded.Dialect, recorded.Reference, recorded.Target, recorded.Schemas, recorded.Summary}).To(Equal([]any{
			history.KindComparison, history.StatusSucceeded, "sqlite", opts.Reference, opts.Target, []string{"main"},
			map[string]int{"changes": 3, "added": 3, "dropped": 0, "modified": 0, "destructive": 0, "objects": 2},
		}))

		inspections, err := store.List(ctx, history.Filter{Kind: history.KindInspection})
		Expect(err).NotTo(HaveOccurred())
		sides := map[string][]any{}
		for _, r := range inspections {
			sides[r.ID] = []any{r.Parent, r.Target, r.Status, r.Summary}
		}
		Expect(sides).To(Equal(map[string][]any{
			opts.Run + "/reference": {opts.Run, opts.Reference, history.StatusSucceeded, map[string]int{"table": 2}},
			opts.Run + "/target":    {opts.Run, opts.Target, history.StatusSucceeded, map[string]int{"table": 1}},
		}))
	})
	It("runs as one clicky group keyed by the run id", func(ctx SpecContext) {
		_, err := compare.Compare(ctx, opts)
		Expect(err).NotTo(HaveOccurred())
		names := map[string]string{}
		for _, s := range clickytask.SnapshotByID(opts.Run) {
			names[s.Name] = s.Type + " " + s.Status
			if s.Type == "group" {
				Expect([]string{s.GroupID, s.Kind}).To(Equal([]string{opts.Run, "sqlglot-comparison"}))
			}
		}
		Expect(names).To(HaveKeyWithValue("Inspect reference side.db", "task success"))
		Expect(names).To(HaveKeyWithValue("Inspect target side.db", "task success"))
		Expect(names).To(HaveKeyWithValue("Plan all differences", "task success"))
		stored, err := store.Snapshot(ctx, opts.Run)
		Expect(err).NotTo(HaveOccurred())
		Expect(stored).To(HaveLen(len(clickytask.SnapshotByID(opts.Run))))
	})
	It("records a failed comparison with the inspection's own error", func(ctx SpecContext) {
		opts.Target = "sqlite://" + filepath.Join(GinkgoT().TempDir(), "missing.db")
		_, err := compare.Compare(ctx, opts)
		Expect(err).To(MatchError(ContainSubstring("open target")))
		Expect(err).NotTo(MatchError(ContainSubstring("dependency failed")))
		recorded, getErr := store.Get(ctx, opts.Run)
		Expect(getErr).NotTo(HaveOccurred())
		Expect([]any{recorded.Status, recorded.Payload}).To(Equal([]any{history.StatusFailed, json.RawMessage(nil)}))
		Expect(recorded.Error).To(ContainSubstring("open target"))
		target, getErr := store.Get(ctx, opts.Run+"/target")
		Expect(getErr).NotTo(HaveOccurred())
		Expect(target.Status).To(Equal(history.StatusFailed))
	})
	It("refuses a run id that was already used, and a run without an id or history", func(ctx SpecContext) {
		_, err := compare.Compare(ctx, opts)
		Expect(err).NotTo(HaveOccurred())
		_, err = compare.Compare(ctx, opts)
		Expect(err).To(MatchError(ContainSubstring("run " + opts.Run + " already exists")))
		missingRun := opts
		missingRun.Run = ""
		_, err = compare.Compare(ctx, missingRun)
		Expect(err).To(MatchError(ContainSubstring("run id is required")))
		missingHistory := withRun(opts)
		missingHistory.History = nil
		_, err = compare.Compare(ctx, missingHistory)
		Expect(err).To(MatchError(ContainSubstring("history store is required")))
	})
	It("records a patch under its own run, linked to the reviewed comparison", func(ctx SpecContext) {
		result, err := compare.Compare(ctx, opts)
		Expect(err).NotTo(HaveOccurred())
		patchOpts := compare.PatchOptions{Options: withRun(opts), Selection: []string{addTags}, DiffHash: result.DiffHash, Comparison: opts.Run}
		patch, err := compare.BuildPatch(ctx, patchOpts)
		Expect(err).NotTo(HaveOccurred())
		recorded, err := store.Get(ctx, patchOpts.Run)
		Expect(err).NotTo(HaveOccurred())
		var payload compare.Patch
		Expect(json.Unmarshal(recorded.Payload, &payload)).To(Succeed())
		Expect(payload).To(Equal(patch))
		Expect([]any{recorded.Kind, recorded.Parent, recorded.Summary}).To(Equal([]any{history.KindPatch, opts.Run, map[string]int{"selected": 1, "acks": 0}}))
	})
	It("lists the differences by object with both definitions", func(ctx SpecContext) {
		result, err := compare.Compare(ctx, opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Dialect).To(Equal("sqlite"))
		Expect(result.Schemas).To(Equal([]string{"main"}))
		var ids []string
		for _, c := range result.Changes {
			ids = append(ids, c.ID)
		}
		Expect(ids).To(ConsistOf(addTags, addNote, addIndex))
		Expect(result.Objects).To(HaveLen(2))
		statuses := map[string]string{}
		for _, o := range result.Objects {
			statuses[o.Name.Name] = o.Status
			Expect(o.Language).To(Equal("hcl"))
		}
		Expect(statuses).To(Equal(map[string]string{"items": "changed", "tags": "added"}))
		Expect(result.DiffHash).To(MatchRegexp("^[0-9a-f]{64}$"))
	})
	It("patches only the selected changes and leaves the rest for a later compare", func(ctx SpecContext) {
		result, err := compare.Compare(ctx, opts)
		Expect(err).NotTo(HaveOccurred())
		patch, err := compare.BuildPatch(ctx, compare.PatchOptions{Options: withRun(opts), Selection: []string{addTags, addNote}, DiffHash: result.DiffHash})
		Expect(err).NotTo(HaveOccurred())
		Expect(patch.SQL).To(ContainSubstring("CREATE TABLE `tags`"))
		Expect(patch.SQL).NotTo(ContainSubstring("idx_items_label"))
		Expect(patch.Acks).To(BeEmpty())

		var artifact review.Artifact
		Expect(json.Unmarshal(patch.Artifact, &artifact)).To(Succeed())
		target, err := sql.Open("sqlite", opts.Target[len("sqlite://"):])
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(target.Close)
		applied, err := review.Apply(ctx, target, review.Request{Artifact: artifact, Hash: patch.Hash})
		Expect(err).NotTo(HaveOccurred())
		Expect(applied.Outcome).To(Equal(review.Applied))

		again, err := compare.Compare(ctx, withRun(opts))
		Expect(err).NotTo(HaveOccurred())
		Expect(again.Changes).To(HaveLen(1))
		Expect(again.Changes[0].ID).To(Equal(addIndex))
	})
	It("refuses a patch when the differences changed since review", func(ctx SpecContext) {
		result, err := compare.Compare(ctx, opts)
		Expect(err).NotTo(HaveOccurred())
		target, err := sql.Open("sqlite", opts.Target[len("sqlite://"):])
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(target.Close)
		_, err = target.Exec("CREATE TABLE intervening (id integer)")
		Expect(err).NotTo(HaveOccurred())
		_, err = compare.BuildPatch(ctx, compare.PatchOptions{Options: withRun(opts), Selection: []string{addTags}, DiffHash: result.DiffHash})
		Expect(err).To(MatchError(ContainSubstring("schema drift")))
	})
	It("refuses comparing a connection with itself", func(ctx SpecContext) {
		_, err := compare.Compare(ctx, compare.Options{Reference: opts.Target, Target: opts.Target, Run: uuid.NewString(), History: store})
		Expect(err).To(MatchError(ContainSubstring("same connection")))
	})
})
