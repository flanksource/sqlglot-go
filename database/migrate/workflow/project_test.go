package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"testing/fstest"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("migration project source mapping", func() {
	It("infers direct and transformed kinds, merges labels and ignores matching sources", func() {
		project, err := LoadProject(fstest.MapFS{
			ProjectConfigName: {Data: []byte(`version: 1
mappings:
  - match: 'schema/**/*.hcl'
    labels: {component: [billing]}
  - match: 'legacy/baseline.txt'
    labels: {component: [inventory], country: [BW]}
    transform: '{"hcl": file.content, "id": "schema/baseline.hcl", "labels": {"country": ["ZW"]}}'
  - match: 'migrations/**/*.sql'
ignore: ['migrations/archive/**']
`)},
			"schema/tables.hcl":          {Data: []byte(`schema "public" {}`)},
			"legacy/baseline.txt":        {Data: []byte(`schema "public" {}`)},
			"migrations/seed.sql":        {Data: []byte(`SELECT 1`)},
			"migrations/archive/old.sql": {Data: []byte(`SELECT 2`)},
		})
		Expect(err).NotTo(HaveOccurred())
		var sources []ProjectSource
		for _, file := range project.files {
			sources = append(sources, file.ProjectSource)
		}
		Expect(sources).To(Equal([]ProjectSource{
			{Path: "legacy/baseline.txt", Kind: HCLContent, ID: "schema/baseline.hcl", Labels: map[string][]string{"component": {"inventory"}, "country": {"ZW"}}},
			{Path: "migrations/seed.sql", Kind: SQLContent, ID: "migrations/seed.sql"},
			{Path: "schema/tables.hcl", Kind: HCLContent, ID: "schema/tables.hcl", Labels: map[string][]string{"component": {"billing"}}},
		}))
	})

	It("maps OIPA identities, phases and countries while preserving SQL bytes", func() {
		config, err := os.ReadFile("testdata/oipa/.sqlglot-go.yaml")
		Expect(err).NotTo(HaveOccurred())
		const body = "SELECT N'café';\r\nGO\r\n"
		const guid = "2C6FB790-39C6-4DFF-853D-427F5B53FB23"
		for _, example := range []struct {
			folder, phase string
			labels        map[string][]string
		}{
			{"PreDeployment_Install_MW", "pre", nil},
			{"PostDeployment_Seed", "post", nil},
			{"PostDeployment_Renamed_BW_SZ", "post", map[string][]string{"country": {"BW", "SZ"}}},
			{"PostDeployment_ZW_Seed", "post", nil},
		} {
			dir := "scripts/" + example.folder
			project, err := LoadProject(fstest.MapFS{
				ProjectConfigName: {Data: config},
				dir + "/data.xml": {Data: []byte(fmt.Sprintf(`<root>
 <entry name="SQLSCRIPTGUID"> %s </entry>
 <entry name="SCRIPTNAME">Readable Script</entry>
 <entry name="SQLDATA" file="SQLData.xml"/>
</root>`, guid))},
				dir + "/SQLData.xml": {Data: []byte(body)},
				"tests/AsEnv.sql":    {Data: []byte("SELECT 2")},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(project.files).To(Equal([]projectFile{{
				ProjectSource: ProjectSource{Path: dir + "/data.xml", Kind: SQLContent, ID: "2c6fb790-39c6-4dff-853d-427f5b53fb23",
					Name: "Readable Script", Phase: example.phase, Labels: example.labels},
				bundlePath: dir + "/data.sql", content: body,
			}}))
		}
	})

	It("ignores a transform's ignored body and permits an explicit kind override", func() {
		project, err := LoadProject(fstest.MapFS{
			ProjectConfigName: {Data: []byte(`version: 1
mappings:
  - match: 'scripts/data.xml'
    transform: '{"sql": read("ignored/body.sql")}'
  - match: 'override.hcl'
    kind: sql
ignore: ['scripts/ignored']
`)},
			"scripts/data.xml": {Data: []byte("unused")},
			"override.hcl":     {Data: []byte("SELECT 1")},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(project.files).To(Equal([]projectFile{{
			ProjectSource: ProjectSource{Path: "override.hcl", ID: "override.sql", Kind: SQLContent},
			bundlePath:    "override.sql", content: "SELECT 1",
		}}))
	})

	It("reports the original source when a SQL header conflicts with the mapped phase", func() {
		project, err := LoadProject(fstest.MapFS{
			ProjectConfigName: {Data: []byte(`version: 1
mappings:
  - match: 'deploy.txt'
    transform: '{"sql": file.content, "phase": "post"}'
`)},
			"deploy.txt": {Data: []byte("-- phase: pre\nSELECT 1")},
		})
		Expect(err).NotTo(HaveOccurred())
		fsys, sources, _, err := project.bundle()
		Expect(err).NotTo(HaveOccurred())
		_, err = scripts.Load(scripts.LoadOptions{FS: fsys, Sources: sources})
		Expect(err).To(MatchError(ContainSubstring(`script deploy.txt: SQL header phase "pre" conflicts with mapped phase "post"`)))
	})

	It("confines a companion read that follows a symlink", func() {
		dir := GinkgoT().TempDir()
		outside := filepath.Join(GinkgoT().TempDir(), "outside.sql")
		Expect(os.WriteFile(outside, []byte("SELECT 1"), 0600)).To(Succeed())
		Expect(os.Symlink(outside, filepath.Join(dir, "body.sql"))).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "source.xml"), []byte("<source/>"), 0600)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, ProjectConfigName), []byte(`version: 1
mappings:
  - match: 'source.xml'
    transform: '{"sql": read("body.sql")}'
`), 0600)).To(Succeed())
		root, err := os.OpenRoot(dir)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(root.Close)
		_, err = LoadProject(root.FS())
		Expect(err).To(MatchError(ContainSubstring("source.xml: transform")))
		Expect(err).To(MatchError(ContainSubstring("escapes from parent")))
	})

	DescribeTable("reports invalid mappings with the affected source", func(rules, message string) {
		_, err := LoadProject(fstest.MapFS{
			ProjectConfigName: {Data: []byte("version: 1\nmappings:\n" + rules)},
			"a.sql":           {Data: []byte("SELECT 1")},
			"b.sql":           {Data: []byte("SELECT 2")},
		})
		Expect(err).To(MatchError(ContainSubstring(message)))
	},
		Entry("an overlap", "  - match: '*.sql'\n  - match: 'a.sql'\n", `a.sql matches both "*.sql" and "a.sql"`),
		Entry("duplicate IDs", "  - match: '*.sql'\n    transform: '{\"sql\": file.content, \"id\": \"same\"}'\n", `a.sql and b.sql have duplicate source ID "same"`),
		Entry("an escaping read", "  - match: 'a.sql'\n    transform: '{\"sql\": read(\"../body.sql\")}'\n", `read path "../body.sql" must be relative`),
		Entry("invalid XML", "  - match: 'a.sql'\n    transform: '{\"sql\": xmlpath(\"<root>\", \"/root\")}'\n", `xmlpath: invalid XML`),
		Entry("missing XML node", "  - match: 'a.sql'\n    transform: '{\"sql\": xmlpath(\"<root/>\", \"/missing\")}'\n", `expected one match, got 0`),
		Entry("a mismatched content kind", "  - match: 'a.sql'\n    transform: '{\"hcl\": file.content}'\n", `a.sql: transform returns hcl for a sql mapping`),
		Entry("invalid labels", "  - match: 'a.sql'\n    transform: '{\"sql\": file.content, \"labels\": {\"country\": 1}}'\n", `a.sql: invalid transform result`),
	)
})
