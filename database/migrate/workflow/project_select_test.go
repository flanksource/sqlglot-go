package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing/fstest"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	scriptplan "github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const selectorConfig = `version: 1
mappings:
  - {match: 'a.sql', labels: {country: [BW, SZ], scope: [tenant]}}
  - {match: 'b.sql', labels: {country: [ZW]}}
  - {match: 'c.sql'}
  - {match: 'v.hcl', labels: {scope: [vendor]}}
  - {match: 't.hcl', labels: {scope: [tenant]}}
  - {match: 'm.sql', labels: {country: [ZW, MW], scope: [vendor, tenant]}}
`

func fileIDs(project *Project) []string {
	ids := []string{}
	for _, file := range project.Files() {
		ids = append(ids, file.ID)
	}
	return ids
}

func selectorProject(config string) *Project {
	project, err := LoadProject(fstest.MapFS{
		ProjectConfigName: {Data: []byte(config)},
		"a.sql":           {Data: []byte("SELECT 1")},
		"b.sql":           {Data: []byte("SELECT 2")},
		"c.sql":           {Data: []byte("SELECT 3")},
		"m.sql":           {Data: []byte("SELECT 4")},
		"t.hcl":           {Data: []byte(`schema "public" {}`)},
		"v.hcl":           {Data: []byte(`schema "public" {}`)},
	})
	Expect(err).NotTo(HaveOccurred())
	return project
}

var _ = Describe("project label selection", func() {
	DescribeTable("keeps the sources whose labels satisfy every key's selectors", func(selectors []string, want ...string) {
		selected, err := selectorProject(selectorConfig).Select(selectors)
		Expect(err).NotTo(HaveOccurred())
		Expect(fileIDs(selected)).To(Equal(want))
	},
		Entry("an include", []string{"scope=tenant"}, "a.sql", "m.sql", "t.hcl"),
		Entry("an exclude keeps unlabelled sources and removes any source holding the value", []string{"!scope=vendor"}, "a.sql", "b.sql", "c.sql", "t.hcl"),
		Entry("a suffix wildcard", []string{"country=Z*"}, "b.sql", "m.sql"),
		Entry("a prefix wildcard", []string{"country=*W"}, "a.sql", "b.sql", "m.sql"),
		Entry("several includes of one key are alternatives", []string{"country=BW", "country=ZW"}, "a.sql", "b.sql", "m.sql"),
		Entry("several keys must all pass", []string{"country=ZW", "!scope=vendor"}, "b.sql"),
		Entry("an include of an absent key fails the source lacking it", []string{"country=*"}, "a.sql", "b.sql", "m.sql"),
		Entry("an exclude of an absent key passes the source lacking it", []string{"!country=ZW"}, "a.sql", "c.sql", "t.hcl", "v.hcl"),
		Entry("an exclude beats an include of the same key", []string{"scope=tenant", "!scope=vendor"}, "a.sql", "t.hcl"),
		Entry("values match case-insensitively", []string{"scope=Tenant"}, "a.sql", "m.sql", "t.hcl"),
	)

	DescribeTable("rejects an invalid selector naming the entry", func(entry string) {
		_, err := selectorProject(selectorConfig).Select([]string{entry})
		Expect(err).To(MatchError(ContainSubstring(`"` + entry + `"`)))
		_, err = ParseProjectConfig([]byte(selectorConfig + "select: ['" + entry + "']\n"))
		Expect(err).To(MatchError(ContainSubstring(`"` + entry + `"`)))
	},
		Entry("no equals sign", "scope"),
		Entry("an empty key", "=tenant"),
		Entry("an empty pattern", "scope="),
		Entry("an empty excluded key", "!=tenant"),
		Entry("a list in one entry", "scope=a,b"),
		Entry("a wildcard inside the pattern", "scope=a*b"),
	)

	It("fails when the selection leaves no source, naming the selectors", func() {
		_, err := selectorProject(selectorConfig).Select([]string{"scope=nothing", "!country=ZW"})
		Expect(err).To(MatchError(ContainSubstring(`"scope=nothing"`)))
		Expect(err).To(MatchError(ContainSubstring(`"!country=ZW"`)))
		Expect(err).To(MatchError(ContainSubstring("select no migration sources")))
	})

	It("applies the config select by default, replaces it with explicit selectors and reports what it left out", func() {
		project := selectorProject(selectorConfig + "select: ['!scope=vendor']\n")
		Expect(fileIDs(project)).To(Equal([]string{"a.sql", "b.sql", "c.sql", "t.hcl"}))
		Expect(project.Preview()).To(Equal(ProjectPreview{Mapped: 4, Deselected: 2}))
		Expect(project.Select(nil)).To(BeIdenticalTo(project))

		vendor, err := project.Select([]string{"scope=vendor"})
		Expect(err).NotTo(HaveOccurred())
		Expect(fileIDs(vendor)).To(Equal([]string{"m.sql", "v.hcl"}), "an explicit selector replaces the default, so the default exclude no longer applies")
		Expect(vendor.Preview()).To(Equal(ProjectPreview{Mapped: 2, Deselected: 4}))
	})

	It("refuses to bundle a project whose default selection is empty", func() {
		project := selectorProject(selectorConfig + "select: ['scope=nothing']\n")
		_, _, _, err := project.Bundle()
		Expect(err).To(MatchError(ContainSubstring(`"scope=nothing"`)))
	})

	It("drops only the vendor HCL of the scope fixture under its default select", func() {
		project, err := LoadProject(os.DirFS("testdata/scope"))
		Expect(err).NotTo(HaveOccurred())
		Expect(fileIDs(project)).To(Equal([]string{
			"schema/tenant/customers.hcl", "schema/tenant/orders.hcl",
			"scripts/base.sql", "scripts/mw_seed.sql", "scripts/zw_seed.sql",
		}))
		Expect(project.Preview()).To(Equal(ProjectPreview{Mapped: 5, Deselected: 1}))
		tenant, err := project.Select([]string{"scope=tenant"})
		Expect(err).NotTo(HaveOccurred())
		Expect(fileIDs(tenant)).To(Equal([]string{"schema/tenant/customers.hcl", "schema/tenant/orders.hcl"}))
		zw, err := project.Select([]string{"country=ZW"})
		Expect(err).NotTo(HaveOccurred())
		Expect(fileIDs(zw)).To(Equal([]string{"scripts/zw_seed.sql"}))
	})

	Describe("planning", func() {
		var db *sql.DB
		BeforeEach(func() {
			var err error
			db, err = sql.Open("sqlite", filepath.Join(GinkgoT().TempDir(), "target.db"))
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(db.Close)
		})
		const config = "version: 1\nmappings:\n  - {match: 'schema/tenant/*.hcl', labels: {scope: [tenant]}}\n  - {match: 'schema/vendor/*.hcl', labels: {scope: [vendor]}}\nselect: ['!scope=vendor']\n"
		bundle := fstest.MapFS{
			ProjectConfigName:        {Data: []byte(config)},
			"schema/tenant/base.hcl": {Data: []byte("schema \"public\" {}\ntable \"orders\" {\n schema = schema.public\n column \"id\" { type = int }\n}")},
			"schema/vendor/base.hcl": {Data: []byte("schema \"public\" {}\ntable \"policy\" {\n schema = schema.public\n column \"id\" { type = int }\n}")},
		}

		It("plans only the selected sources by default and lists only them", func() {
			plan, err := Plan(context.Background(), db, Options{Dialect: "sqlite", Schema: "main", Bundle: bundle})
			Expect(err).NotTo(HaveOccurred())
			Expect(plan.SQL).To(ContainSubstring("orders"))
			Expect(plan.SQL).NotTo(ContainSubstring("policy"))
			Expect(plan.Sources).To(Equal([]ProjectSource{{Path: "schema/tenant/base.hcl", Kind: HCLContent, ID: "schema/tenant/base.hcl", Labels: map[string][]string{"scope": {"tenant"}}}}))
		})

		It("plans a project whose HCL sources each carry the same schema block as one schema", func() {
			table := func(name string) []byte {
				return []byte("schema \"public\" {}\ntable \"" + name + "\" {\n schema = schema.public\n column \"id\" { type = int }\n}")
			}
			perFile := fstest.MapFS{
				ProjectConfigName:        {Data: []byte(config)},
				"schema/tenant/a.hcl":    {Data: table("alpha")},
				"schema/tenant/b.hcl":    {Data: table("beta")},
				"schema/tenant/c.hcl":    {Data: table("gamma")},
				"schema/vendor/base.hcl": {Data: table("policy")},
			}
			plan, err := Plan(context.Background(), db, Options{Dialect: "sqlite", Schema: "main", Bundle: perFile})
			Expect(err).NotTo(HaveOccurred())
			Expect(plan.SQL).To(SatisfyAll(ContainSubstring("alpha"), ContainSubstring("beta"), ContainSubstring("gamma"), Not(ContainSubstring("policy"))))
			Expect(plan.Sources).To(HaveLen(3))
		})

		It("lets Options.Labels replace the config select", func() {
			plan, err := Plan(context.Background(), db, Options{Dialect: "sqlite", Schema: "main", Bundle: bundle, Labels: []string{"scope=vendor"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(plan.SQL).To(ContainSubstring("policy"))
			Expect(plan.SQL).NotTo(ContainSubstring("orders"))
		})

		It("fails when the labels select nothing, and when there is no project to select from", func() {
			_, err := Plan(context.Background(), db, Options{Dialect: "sqlite", Schema: "main", Bundle: bundle, Labels: []string{"scope=none"}})
			Expect(err).To(MatchError(ContainSubstring(`"scope=none"`)))
			_, err = Plan(context.Background(), db, Options{Dialect: "sqlite", Schema: "main", Source: Source{Kind: "bundle", Files: []File{{Path: "a.hcl", Content: `schema "public" {}`}}}, Labels: []string{"scope=tenant"}})
			Expect(err).To(MatchError(ContainSubstring("label selectors require " + ProjectConfigName)))
			_, err = Plan(context.Background(), db, Options{Dialect: "sqlite", Schema: "main", Source: Source{Kind: "hcl", Content: `schema "main" {}`}, Labels: []string{"scope=tenant"}})
			Expect(err).To(MatchError(ContainSubstring("label selectors require " + ProjectConfigName)))
		})
	})
})

var _ = Describe("project labels in the script plan", func() {
	const config = `version: 1
mappings:
  - match: 'tagged/*.sql'
    labels: {scope: [tenant], country: [BW]}
  - match: 'override/*.sql'
    labels: {scope: [tenant], country: [BW]}
    transform: '{"sql": file.content, "labels": {"country": ["ZW"]}}'
  - match: 'removed/*.sql'
    labels: {scope: [tenant], country: [BW]}
    transform: '{"sql": file.content, "labels": {"country": []}}'
  - match: 'plain/*.sql'
`
	load := func() []scripts.Script {
		project, err := LoadProject(fstest.MapFS{
			ProjectConfigName: {Data: []byte(config)},
			"tagged/a.sql":    {Data: []byte("SELECT 1")},
			"override/b.sql":  {Data: []byte("SELECT 2")},
			"removed/c.sql":   {Data: []byte("SELECT 3")},
			"plain/d.sql":     {Data: []byte("SELECT 4")},
		})
		Expect(err).NotTo(HaveOccurred())
		fsys, sources, _, err := project.Bundle()
		Expect(err).NotTo(HaveOccurred())
		items, err := scripts.Load(scripts.LoadOptions{FS: fsys, Sources: sources})
		Expect(err).NotTo(HaveOccurred())
		return items
	}

	It("carries mapping labels, transform overrides and removals onto each loaded script", func() {
		labels := map[string]map[string][]string{}
		for _, item := range load() {
			labels[item.ID] = item.Labels
		}
		Expect(labels).To(Equal(map[string]map[string][]string{
			"tagged/a.sql":   {"scope": {"tenant"}, "country": {"BW"}},
			"override/b.sql": {"scope": {"tenant"}, "country": {"ZW"}},
			"removed/c.sql":  {"scope": {"tenant"}},
			"plain/d.sql":    nil,
		}))
	})

	It("serialises labels on both the bundled script and its planned action", func() {
		items := load()
		file := scriptplan.PlanFile{Bundle: scriptplan.PlanBundle{Scripts: items}, Actions: []scripts.Pending{{Script: items[2]}}}
		data, err := json.Marshal(file)
		Expect(err).NotTo(HaveOccurred())
		var decoded struct {
			Bundle struct {
				Scripts []struct {
					ID, Path string
					Labels   map[string][]string
				}
			}
			Actions []struct {
				Script struct {
					ID     string
					Labels map[string][]string
				}
			}
		}
		Expect(json.Unmarshal(data, &decoded)).To(Succeed())
		Expect(decoded.Bundle.Scripts[2].Labels).To(Equal(items[2].Labels))
		Expect(decoded.Actions[0].Script.Labels).To(Equal(items[2].Labels))
		Expect(items[2].Labels).NotTo(BeEmpty())
	})

	It("serialises a label-less script exactly as before labels existed, so its plan hash is unchanged", func() {
		project, err := LoadProject(fstest.MapFS{
			ProjectConfigName: {Data: []byte("version: 1\nmappings:\n  - match: 'sql/*.sql'\n")},
			"sql/a.sql":       {Data: []byte("SELECT 1")},
		})
		Expect(err).NotTo(HaveOccurred())
		fsys, sources, _, err := project.Bundle()
		Expect(err).NotTo(HaveOccurred())
		items, err := scripts.Load(scripts.LoadOptions{FS: fsys, Sources: sources})
		Expect(err).NotTo(HaveOccurred())
		// Version 2 is the last format without an engine artifact; a file of it without a schema step still reads.
		file := scriptplan.PlanFile{Kind: scriptplan.PlanFileKind, Version: 2,
			Target: scriptplan.PlanTarget{Server: "srv", Database: "db", Scope: "scope"},
			Bundle: scriptplan.PlanBundle{Scripts: items}, Ledger: map[string]scripts.State{}, Observations: map[string]string{},
			Actions: []scripts.Pending{{Script: items[0], Selected: true, Reasons: []string{"new"}}}}
		data, err := json.Marshal(file)
		Expect(err).NotTo(HaveOccurred())
		// Captured from the code before Labels existed.
		Expect(string(data)).To(Equal(`{"kind":"sqlglot-go/script-plan","version":2,"target":{"server":"srv","database":"db","scope":"scope"},"bundle":{"scripts":[{"id":"sql/a.sql","path":"sql/a.sql","name":"sql/a.sql","sql":"SELECT 1","phase":"post"}]},"ledger":{},"observations":{},"actions":[{"script":{"id":"sql/a.sql","path":"sql/a.sql","name":"sql/a.sql","sql":"SELECT 1","phase":"post"},"state":{"version":0,"sourceHash":"","policyHash":"","hash":"","inputs":null},"selected":true,"reasons":["new"]}],"hash":""}`))
		file.Hash = "0954ea8ca866abde0430950208de19732cab234f83030cbc9dc93f7542830a69"
		Expect(file.Verify()).To(Succeed())
	})
})
