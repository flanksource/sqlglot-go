//go:build integration

package integration_test

import (
	"encoding/json"
	"testing/fstest"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	backend "github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver/sqlservertest"
	"github.com/flanksource/sqlglot-go/database/migrate/workflow"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = It("plans a script-only mapped project on disposable SQL Server", func(ctx SpecContext) {
	const source = "steps/install.xml"
	const statement = "SELECT 1;\r\n"
	project, err := workflow.LoadProject(fstest.MapFS{
		workflow.ProjectConfigName: {Data: []byte(`version: 1
mappings:
  - match: 'steps/install.xml'
    transform: '{"sql": read("install.sql"), "id": "setup-id", "name": "Install setup", "phase": "pre", "labels": {"component": ["setup"]}}'
`)},
		source:              {Data: []byte("<step/>")},
		"steps/install.sql": {Data: []byte(statement)},
	})
	Expect(err).NotTo(HaveOccurred())
	db := sqlservertest.OpenDisposable(ctx)
	result, err := workflow.Plan(ctx, db, workflow.Options{Dialect: "sqlserver", Schema: "dbo", Project: project})
	Expect(err).NotTo(HaveOccurred())
	Expect(result.Sources).To(Equal([]workflow.ProjectSource{{Path: source, ID: "setup-id", Kind: workflow.SQLContent,
		Name: "Install setup", Phase: "pre", Labels: map[string][]string{"component": {"setup"}}}}))
	var reviewed backend.PlanFile
	Expect(json.Unmarshal(result.Artifact, &reviewed)).To(Succeed())
	Expect(reviewed.Bundle.Scripts).To(HaveLen(1))
	got := reviewed.Bundle.Scripts[0]
	Expect([]any{got.ID, got.Name, got.Path, got.Phase, got.SQL}).To(Equal([]any{"setup-id", "Install setup", source, scripts.Pre, statement}))
	Expect(reviewed.Selected()).To(Equal([]string{"setup-id"}))
})

var _ = It("warns about a mapped comment-only script and applies only the executable script", func(ctx SpecContext) {
	const source = "scripts/PostDeployment_disabled/data.xml"
	project, err := workflow.LoadProject(fstest.MapFS{
		workflow.ProjectConfigName: {Data: []byte(`version: 1
mappings:
  - match: 'scripts/*/data.xml'
    transform: '{"id": "disabled", "sql": read("SQLData.xml")}'
  - match: 'scripts/create.sql'
`)},
		source: {Data: []byte("<step/>")},
		"scripts/PostDeployment_disabled/SQLData.xml": {Data: []byte("/*\r\nDELETE FROM dbo.example;\r\n*/")},
		"scripts/create.sql":                          {Data: []byte("-- dependsOn: disabled\nCREATE TABLE dbo.warning_test(id INT NOT NULL);")},
	})
	Expect(err).NotTo(HaveOccurred())
	db := sqlservertest.OpenDisposable(ctx)
	result, err := workflow.Plan(ctx, db, workflow.Options{Dialect: "sqlserver", Schema: "dbo", Project: project})
	Expect(err).NotTo(HaveOccurred())
	Expect(result.Warnings).To(ContainElement("SQL script " + source + " has no statements; skipped"))
	Expect(result.ScriptPlan.Selected()).To(Equal([]string{"scripts/create.sql"}))
	Expect(result.ScriptPlan.Actions[0].Script.Path).To(Equal(source))
	Expect(result.ScriptPlan.Actions[0].Blocked).To(BeEmpty())
	applied, err := backend.Apply(ctx, db, *result.ScriptPlan)
	Expect(err).NotTo(HaveOccurred())
	Expect(applied.Applied).To(Equal([]string{"scripts/create.sql"}))
})

var _ = It("propagates a failed prerequisite through a skipped SQL Server script", func(ctx SpecContext) {
	db := sqlservertest.OpenDisposable(ctx)
	file, err := backend.Plan(ctx, backend.Options{DB: db, Scope: "skipped-dependency", Scripts: []scripts.Script{
		{ID: "failed", Phase: scripts.Post, SQL: "THROW 50001, 'fixture failure', 1;"},
		{ID: "empty", Phase: scripts.Post, DependsOn: []string{"failed"}},
		{ID: "dependent", Phase: scripts.Post, DependsOn: []string{"empty"}, SQL: "CREATE TABLE dbo.must_not_run(id INT);"},
	}})
	Expect(err).NotTo(HaveOccurred())
	Expect(file.Selected()).To(Equal([]string{"failed", "dependent"}))
	result, err := backend.Apply(ctx, db, file)
	Expect(err).To(MatchError(ContainSubstring("fixture failure")))
	Expect(result.Applied).To(BeEmpty())
	Expect(result.Skipped).To(Equal([]backend.SkippedScript{{Script: "dependent", Dependency: "empty"}}))
})
