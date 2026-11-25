package workflow

import (
	"encoding/json"
	"io/fs"
	"os"
	"testing/fstest"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("project API for other modules", func() {
	It("parses a config from bytes and builds the same one in code", func() {
		parsed, err := ParseProjectConfig([]byte(`version: 1
mappings:
  - match: 'schema/**/*.hcl'
    kind: hcl
    labels: {scope: [tenant]}
  - match: 'scripts/*.xml'
    transform: '{"sql": file.content}'
select: ['!scope=vendor', 'country=ZW']
ignore: ['scripts/old/**']
`))
		Expect(err).NotTo(HaveOccurred())
		Expect(parsed).To(Equal(ProjectConfig{
			Version: 1,
			Mappings: []ProjectMapping{
				{Match: "schema/**/*.hcl", Kind: HCLContent, Labels: map[string][]string{"scope": {"tenant"}}},
				{Match: "scripts/*.xml", Transform: `{"sql": file.content}`},
			},
			Select: []string{"!scope=vendor", "country=ZW"},
			Ignore: []string{"scripts/old/**"},
		}))

		built := ProjectConfig{Version: 1, Mappings: []ProjectMapping{{Match: "*.sql", Labels: map[string][]string{"scope": {"tenant"}}}}}
		project, err := built.Load(fstest.MapFS{"a.sql": {Data: []byte("SELECT 1")}})
		Expect(err).NotTo(HaveOccurred())
		Expect(project.Files()).To(Equal([]ProjectFile{{
			ProjectSource: ProjectSource{Path: "a.sql", Kind: SQLContent, ID: "a.sql", Labels: map[string][]string{"scope": {"tenant"}}},
			BundlePath:    "a.sql", Content: "SELECT 1", Inputs: []string{"a.sql"},
		}}))
	})

	It("rejects a config built in code with an invalid select", func() {
		built := ProjectConfig{Version: 1, Mappings: []ProjectMapping{{Match: "*.sql"}}, Select: []string{"scope"}}
		_, err := built.Load(fstest.MapFS{"a.sql": {Data: []byte("SELECT 1")}})
		Expect(err).To(MatchError(ContainSubstring(`"scope"`)))
	})

	It("exposes the content and bundle path of an HCL and a transformed SQL source in order", func() {
		project, err := LoadProject(fstest.MapFS{
			ProjectConfigName: {Data: []byte(`version: 1
mappings:
  - match: 'schema/*.hcl'
  - match: 'scripts/*/data.xml'
    transform: '{"sql": read("body.sql"), "id": "guid-1", "name": "Seed", "phase": "pre", "labels": {"country": ["ZW"]}}'
`)},
			"schema/tables.hcl":    {Data: []byte(`schema "dbo" {}`)},
			"scripts/one/data.xml": {Data: []byte("<root/>")},
			"scripts/one/body.sql": {Data: []byte("SELECT 1\r\nGO\r\n")},
		})
		Expect(err).NotTo(HaveOccurred())
		files := project.Files()
		Expect(files).To(Equal([]ProjectFile{
			{ProjectSource: ProjectSource{Path: "schema/tables.hcl", Kind: HCLContent, ID: "schema/tables.hcl"}, BundlePath: "schema/tables.hcl", Content: `schema "dbo" {}`, Inputs: []string{"schema/tables.hcl"}},
			{ProjectSource: ProjectSource{Path: "scripts/one/data.xml", Kind: SQLContent, ID: "guid-1", Name: "Seed", Phase: "pre", Labels: map[string][]string{"country": {"ZW"}}},
				BundlePath: "scripts/one/data.sql", Content: "SELECT 1\r\nGO\r\n", Inputs: []string{"scripts/one/data.xml", "scripts/one/body.sql"}},
		}))
		files[1].Labels["country"][0] = "changed"
		files[1].Inputs[1] = "changed"
		Expect(project.Files()[1].Labels["country"]).To(Equal([]string{"ZW"}), "Files returns copies")
		Expect(project.Files()[1].Inputs).To(Equal([]string{"scripts/one/data.xml", "scripts/one/body.sql"}), "Files returns copies")
	})

	It("lists each file a transform read once, after the mapped file, in path order", func() {
		project, err := LoadProject(fstest.MapFS{
			ProjectConfigName: {Data: []byte(`version: 1
mappings:
  - match: 'scripts/*/data.xml'
    transform: '{"sql": read("z.sql") + read("a.sql") + read("z.sql")}'
`)},
			"scripts/one/data.xml": {Data: []byte("<root/>")},
			"scripts/one/z.sql":    {Data: []byte("Z;")},
			"scripts/one/a.sql":    {Data: []byte("A;")},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(project.Files()[0].Content).To(Equal("Z;A;Z;"))
		Expect(project.Files()[0].Inputs).To(Equal([]string{"scripts/one/data.xml", "scripts/one/a.sql", "scripts/one/z.sql"}))
	})

	It("omits inputs from JSON when empty and keeps the other fields", func() {
		data, err := json.Marshal(ProjectFile{Inputs: []string{"a.sql"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(ContainSubstring(`"inputs":["a.sql"]`))
		data, err = json.Marshal(ProjectFile{})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).NotTo(ContainSubstring("inputs"))
	})

	It("bundles the OIPA fixture into the same files, script sources and inventory", func() {
		config, err := os.ReadFile("testdata/oipa/.sqlglot-go.yaml")
		Expect(err).NotTo(HaveOccurred())
		project, err := LoadProject(fstest.MapFS{
			ProjectConfigName: {Data: config},
			"scripts/PostDeployment_Renamed_BW_SZ/data.xml":    {Data: []byte(`<root><entry name="SQLSCRIPTGUID">AA</entry><entry name="SCRIPTNAME">Renamed</entry><entry name="SQLDATA" file="SQLData.xml"/></root>`)},
			"scripts/PostDeployment_Renamed_BW_SZ/SQLData.xml": {Data: []byte("SELECT 1")},
			"scripts/PreDeployment_Install/data.xml":           {Data: []byte(`<root><entry name="SQLSCRIPTGUID">bb</entry><entry name="SCRIPTNAME">Install</entry><entry name="SQLDATA" file="SQLData.xml"/></root>`)},
			"scripts/PreDeployment_Install/SQLData.xml":        {Data: []byte("SELECT 2")},
		})
		Expect(err).NotTo(HaveOccurred())
		fsys, sources, inventory, err := project.Bundle()
		Expect(err).NotTo(HaveOccurred())
		Expect(sources).To(Equal(map[string]scripts.Source{
			"scripts/PostDeployment_Renamed_BW_SZ/data.sql": {ID: "aa", Name: "Renamed", Path: "scripts/PostDeployment_Renamed_BW_SZ/data.xml", Phase: scripts.Post, Labels: map[string][]string{"country": {"BW", "SZ"}}},
			"scripts/PreDeployment_Install/data.sql":        {ID: "bb", Name: "Install", Path: "scripts/PreDeployment_Install/data.xml", Phase: scripts.Pre},
		}))
		Expect(inventory).To(HaveLen(2))
		body, err := fs.ReadFile(fsys, "scripts/PreDeployment_Install/data.sql")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(body)).To(Equal("SELECT 2"))
	})
})
