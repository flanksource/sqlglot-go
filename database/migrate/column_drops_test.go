package migrate

import (
	"errors"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("an unreviewed apply's selection of a plan", func() {
	items := migrate.Name{Schema: "main", Name: "items"}
	dropColumn := migrate.Change{ID: "drop-notes", Action: "drop", Kind: "column", Object: items, Member: "notes"}
	dropIndex := migrate.Change{ID: "drop-index", Action: "drop", Kind: "index", Object: items, Member: "items_notes"}
	rebuild := migrate.Change{ID: "rebuild-view", Action: "rebuild", Kind: "view", Object: items, Member: "v"}

	It("runs the plan as it is when it drops no column", func() {
		full := engine.Artifact{Changes: []migrate.Change{dropIndex}}
		got, err := selectUnreviewed(full, true, func(changeSelection) (engine.Artifact, error) {
			return engine.Artifact{}, errors.New("must not replan")
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Warnings).To(BeEmpty())
		Expect(*got.Artifact).To(Equal(full))
	})

	It("replans with every change but the column drops and the forced rebuilds, and warns once per column", func() {
		var asked changeSelection
		got, err := selectUnreviewed(engine.Artifact{Changes: []migrate.Change{dropColumn, dropIndex, rebuild}}, true, func(s changeSelection) (engine.Artifact, error) {
			asked = s
			return engine.Artifact{Selection: s.IDs}, nil
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(asked).To(Equal(changeSelection{IDs: []string{"drop-index"}}))
		Expect(got.Warnings).To(Equal([]string{"column \"notes\" of table main.items is in the database but not declared, and was not dropped: declare the column, mark the table `partial = true`, or apply with WithDrops"}))
	})

	It("selects none when only column drops are planned, and leaves WithDrops out of the advice where it is not supported", func() {
		var asked changeSelection
		got, err := selectUnreviewed(engine.Artifact{Changes: []migrate.Change{dropColumn}}, false, func(s changeSelection) (engine.Artifact, error) {
			asked = s
			return engine.Artifact{SelectNone: s.None}, nil
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(asked).To(Equal(changeSelection{None: true}))
		Expect(got.Warnings).To(ConsistOf(ContainSubstring("SQL Server bundles do not support WithDrops")))
		Expect(got.Warnings[0]).NotTo(ContainSubstring("or apply with WithDrops"))
	})

	It("fails closed, naming the columns, when leaving a drop out makes another change unappliable", func() {
		_, err := selectUnreviewed(engine.Artifact{Changes: []migrate.Change{dropColumn, dropIndex}}, true, func(changeSelection) (engine.Artifact, error) {
			return engine.Artifact{}, errors.New(`change "drop-index" requires unselected changes: drop-notes`)
		})
		Expect(err).To(MatchError(And(
			ContainSubstring("leaving out the column drops"),
			ContainSubstring(`column "notes" of table main.items`),
			ContainSubstring("requires unselected changes: drop-notes"))))
	})
})
