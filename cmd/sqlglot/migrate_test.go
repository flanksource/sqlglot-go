package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing/fstest"

	"github.com/flanksource/sqlglot-go/database/migrate/csvdata"
	"github.com/flanksource/sqlglot-go/database/migrate/workflow"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("migration command help", func() {
	runHelp := func(args ...string) string {
		root := newRootCommand(strings.NewReader(""))
		var output, stderr bytes.Buffer
		root.SetOut(&output)
		root.SetErr(&stderr)
		root.SetArgs(args)
		Expect(root.Execute()).To(Succeed())
		Expect(stderr.String()).To(BeEmpty())
		return output.String()
	}

	DescribeTable("shows command-specific documentation in plain help", func(args, snippets []string) {
		output := runHelp(append(append([]string{"migrate"}, args...), "--help", "--no-color")...)
		for _, snippet := range snippets {
			Expect(output).To(ContainSubstring(snippet))
		}
		Expect(output).NotTo(ContainSubstring("\x1b["))
		Expect(output).NotTo(ContainSubstring("```"))
		Expect(output).NotTo(Or(ContainSubstring("--country"), ContainSubstring("--env"), ContainSubstring("WithScriptPolicy")))
	},
		Entry("migrate", []string{}, []string{
			"Migration sources", "schema \"public\" {}", "-- phase: post", "-- dependsOn:", "runs: always",
			"-- transaction: true", "Parsed dependency invalidation", "sqlglot migrate review .sqlglot-go/plan.json",
			"sqlServerArtifact", "scriptPlan", "upstream-write: dbo.asenv", "plan is stale", "--ack", "SQL Server CSV imports", "AS.csv.yaml", "both phases", "csv-delete:", "Available Commands:",
		}),
		Entry("plan", []string{"plan"}, []string{"--out", ".sqlglot-go/plan.json", "stderr", "--csv-root", "sqlglot migrate --help"}),
		Entry("review", []string{"review"}, []string{"offline", "sqlglot migrate review .sqlglot-go/plan.json", "sqlglot migrate --help"}),
		Entry("apply", []string{"apply"}, []string{"sqlglot migrate apply --dsn connection://inventory .sqlglot-go/plan.json", "plan is stale", "--ack", "--lock-timeout", "sqlglot migrate --help"}),
	)

	It("documents no reviewed-hash flag", func() {
		Expect(runHelp("migrate", "apply", "--help", "--no-color")).NotTo(ContainSubstring("--hash"))
	})

	It("renders interactive help in color and honors color opt-outs", func() {
		GinkgoT().Setenv("CLICKY_FORCE_INTERACTIVE", "1")
		GinkgoT().Setenv("TERM", "xterm-256color")
		GinkgoT().Setenv("COLOR", "")
		GinkgoT().Setenv("NO_COLOR", "")
		Expect(runHelp("migrate", "--help")).To(ContainSubstring("\x1b["))
		Expect(runHelp("migrate", "--help", "--no-color")).NotTo(ContainSubstring("\x1b["))

		GinkgoT().Setenv("NO_COLOR", "1")
		Expect(runHelp("help", "migrate")).To(ContainSubstring("Parsed dependency invalidation"))
		Expect(runHelp("migrate", "--help")).NotTo(ContainSubstring("\x1b["))
	})
})

var _ = Describe("CSV migration CLI flags", func() {
	DescribeTable("validates layer flags before opening the target", func(flags []string, message string) {
		root := newRootCommand(strings.NewReader(""))
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(append([]string{"migrate", "plan", "--csv-root", GinkgoT().TempDir()}, flags...))
		Expect(root.Execute()).To(MatchError(ContainSubstring(message)))
	},
		Entry("rejects the country shortcut", []string{"--country=none,ZA"}, "unknown flag: --country"),
		Entry("rejects the environment shortcut", []string{"--env=!dev"}, "unknown flag: --env"),
		Entry("accepts repeatable layer filters with commas", []string{"--layer", "country=none,ZA", "--layer", "env=!dev"}, "database dsn is required"),
		Entry("rejects duplicate layers", []string{"--layer", "country=none,ZA", "--layer", "country=ZA"}, `duplicate CSV layer filter "country"`),
	)

	It("routes the files selected by the documented AS.csv.yaml example", func() {
		_, block, found := strings.Cut(migrationHelp, "```yaml\n")
		Expect(found).To(BeTrue())
		mapping, _, found := strings.Cut(block, "\n```")
		Expect(found).To(BeTrue())
		const csv = "ID~Label\n1~Notebook\n"
		files := fstest.MapFS{
			csvdata.ConfigName:               {Data: []byte(mapping)},
			"ItemLabel.csv":                  {Data: []byte(csv)},
			"ItemLabel_ZA.csv":               {Data: []byte(csv)},
			"overrides/dev/ItemLabel_ZA.csv": {Data: []byte(csv)},
			"overrides/qa/ItemLabel_ZA.csv":  {Data: []byte(csv)},
		}
		bundle, err := csvdata.Load(files, "dbo", map[string]string{"country": "none,ZA", "env": "!dev"})
		Expect(err).NotTo(HaveOccurred())
		routes := make(map[string]string, len(bundle.Files))
		for _, file := range bundle.Files {
			routes[file.Path] = file.Table
		}
		Expect(routes).To(Equal(map[string]string{
			"ItemLabel.csv": "dbo.ItemLabel", "ItemLabel_ZA.csv": "dbo.ItemLabel", "overrides/qa/ItemLabel_ZA.csv": "dbo.ItemLabel",
		}))
	})
})

var _ = Describe("migration filesystem confinement", func() {
	const desired = "schema \"public\" {}\ntable \"items\" {\n schema = schema.public\n column \"id\" { type = integer }\n}"
	var dir, dbPath string
	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		dbPath = filepath.Join(dir, "target.db")
		db, err := sql.Open("sqlite", dbPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(db.Ping()).To(Succeed())
		Expect(db.Close()).To(Succeed())
	})
	run := func(args ...string) error {
		root := newRootCommand(strings.NewReader(""))
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(append([]string{"migrate", "plan", "--dsn", dbPath}, args...))
		return root.Execute()
	}

	It("refuses an output symlink escaping its selected directory without modifying the destination", func() {
		outside := filepath.Join(GinkgoT().TempDir(), "keep.json")
		const original = "keep this file"
		Expect(os.WriteFile(outside, []byte(original), 0600)).To(Succeed())
		output := filepath.Join(dir, "reviewed.json")
		Expect(os.Symlink(outside, output)).To(Succeed())
		input := filepath.Join(dir, "desired.hcl")
		Expect(os.WriteFile(input, []byte(strings.ReplaceAll(desired, "public", "main")), 0600)).To(Succeed())
		Expect(run("--desired", input, "--out", output)).To(MatchError(ContainSubstring("escapes from parent")))
		root, err := os.OpenRoot(filepath.Dir(outside))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(root.Close)
		Expect(root.ReadFile("keep.json")).To(Equal([]byte(original)))
	})

	It("refuses HCL symlinks escaping the selected bundle directory", func() {
		outside := filepath.Join(GinkgoT().TempDir(), "schema.hcl")
		Expect(os.WriteFile(outside, []byte(desired), 0600)).To(Succeed())
		Expect(os.Symlink(outside, filepath.Join(dir, "schema.hcl"))).To(Succeed())
		Expect(run("--bundle", dir)).To(MatchError(ContainSubstring("escapes from parent")))
	})

	It("refuses a missing bundle directory", func() {
		Expect(run("--bundle", filepath.Join(dir, "missing"))).To(MatchError(ContainSubstring("missing")))
	})

	Describe("positional source paths", func() {
		var sources string
		BeforeEach(func() {
			sources = filepath.Join(dir, "scripts")
			Expect(os.MkdirAll(filepath.Join(sources, "PostDeployment_Seed"), 0o750)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(sources, "PostDeployment_Seed", "data.xml"), []byte("<?xml version=\"1.0\"?><SQLSCRIPT/>"), 0o600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(sources, "PostDeployment_Seed", "SQLData.xml"), []byte("/* seed */\nINSERT INTO items (id) VALUES (1)"), 0o600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(dir, "schema"), []byte(desired), 0o600)).To(Succeed())
		})

		It("plans SQL and HCL detected by their content, naming each script by its detected kind", func() {
			Expect(run("--out", filepath.Join(dir, "plan.json"), sources, filepath.Join(dir, "schema"))).To(Succeed())
			root, err := os.OpenRoot(dir)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(root.Close)
			data, err := root.ReadFile("plan.json")
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(SatisfyAll(ContainSubstring("CREATE TABLE"), ContainSubstring("PostDeployment_Seed/SQLData.sql")))
			Expect(string(data)).NotTo(ContainSubstring("data.xml"))
		})

		It("lists the directory files of neither kind it skips", func() {
			var report bytes.Buffer
			files, err := readBundleSources([]string{sources}, &report)
			Expect(err).NotTo(HaveOccurred())
			Expect(files).To(Equal([]workflow.File{{Path: "PostDeployment_Seed/SQLData.sql", Content: "/* seed */\nINSERT INTO items (id) VALUES (1)"}}))
			Expect(report.String()).To(Equal("skipped 1 files that hold neither SQL nor HCL: " + filepath.Join(sources, "PostDeployment_Seed", "data.xml") + "\n"))
		})

		It("counts a file name skipped more than three times instead of listing each", func() {
			for _, name := range []string{"A", "B", "C", "D"} {
				Expect(os.MkdirAll(filepath.Join(sources, name), 0o750)).To(Succeed())
				Expect(os.WriteFile(filepath.Join(sources, name, "data.xml"), []byte("<SQLSCRIPT/>"), 0o600)).To(Succeed())
			}
			Expect(os.WriteFile(filepath.Join(sources, "A", "SQLData.xml"), []byte("/* commented out"), 0o600)).To(Succeed())
			var report bytes.Buffer
			_, err := readBundleSources([]string{sources}, &report)
			Expect(err).NotTo(HaveOccurred())
			Expect(report.String()).To(Equal("skipped 6 files that hold neither SQL nor HCL: " + filepath.Join(sources, "A", "SQLData.xml") + ", 5 named data.xml\n"))
		})

		It("refuses a file argument that holds neither SQL nor HCL", func() {
			Expect(run(filepath.Join(sources, "PostDeployment_Seed", "data.xml"))).To(MatchError(ContainSubstring("data.xml holds neither SQL nor HCL")))
		})

		It("refuses source paths beside --bundle", func() {
			Expect(run("--bundle", sources, filepath.Join(dir, "schema"))).To(MatchError(ContainSubstring("select exactly one migration source")))
		})

		It("refuses a directory escaping symlink as --bundle does", func() {
			outside := filepath.Join(GinkgoT().TempDir(), "escape.sql")
			Expect(os.WriteFile(outside, []byte("DELETE FROM items"), 0o600)).To(Succeed())
			Expect(os.Symlink(outside, filepath.Join(sources, "escape.sql"))).To(Succeed())
			Expect(run(sources)).To(MatchError(ContainSubstring("escapes from parent")))
		})
	})

	It("plans a confined bundle and saves the reviewed artifact", func() {
		Expect(os.WriteFile(filepath.Join(dir, "schema.hcl"), []byte(desired), 0600)).To(Succeed())
		output := filepath.Join(dir, "reviewed.json")
		Expect(run("--bundle", dir, "--out", output)).To(Succeed())
		root, err := os.OpenRoot(dir)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(root.Close)
		Expect(root.ReadFile("reviewed.json")).To(ContainSubstring("CREATE TABLE"))
	})
})
