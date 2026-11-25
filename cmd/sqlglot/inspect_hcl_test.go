package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"

	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("schema inspect --hcl", func() {
	var dbPath string
	BeforeEach(func() {
		dbPath = filepath.Join(GinkgoT().TempDir(), "cli.db")
		db, err := sql.Open("sqlite", dbPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(db.Ping()).To(Succeed())
		Expect(db.Close()).To(Succeed())
	})
	inspect := func(args ...string) (string, error) {
		root := newRootCommand(strings.NewReader(""))
		out := &bytes.Buffer{}
		root.SetOut(out)
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(append([]string{"schema", "inspect", "--dsn", dbPath, "--no-color"}, args...))
		err := root.Execute()
		return out.String(), err
	}

	DescribeTable("refuses what cannot be written, before reading anything",
		func(args []string, message string) {
			_, err := inspect(args...)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("a database that is not SQL Server", []string{"--hcl"}, "--hcl writes SQL Server HCL; the target is sqlite"),
		Entry("an HCL option without --hcl", []string{"--table", "items"}, "--table writes HCL: add --hcl"),
		Entry("--out without --hcl", []string{"--out", "db"}, "--out writes HCL: add --hcl"),
		Entry("--omit-unsupported without --hcl", []string{"--omit-unsupported"}, "--omit-unsupported writes HCL: add --hcl"),
	)

	DescribeTable("reads TABLE=COLUMN,COLUMN",
		func(entries []string, want map[string][]string, message string) {
			got, err := parseColumns(entries)
			if message != "" {
				Expect(err).To(MatchError(ContainSubstring(message)))
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want))
		},
		Entry("none", nil, map[string][]string(nil), ""),
		Entry("two tables", []string{"a=x,y", "b=z"}, map[string][]string{"a": {"x", "y"}, "b": {"z"}}, ""),
		Entry("no equals sign", []string{"a"}, nil, `"a" must be TABLE=COLUMN,COLUMN`),
		Entry("an empty column", []string{"a=x,"}, nil, `"a=x," must be TABLE=COLUMN,COLUMN`),
		Entry("a table twice", []string{"a=x", "a=y"}, nil, `names table "a" twice`),
	)

	It("writes one file per table into --out and lists them", func() {
		dir := filepath.Join(GinkgoT().TempDir(), "hcl")
		files := []planner.HCLFile{{Name: "a.hcl", Content: "schema \"dbo\" {}\n"}, {Name: "b.hcl", Content: "schema \"dbo\" {}\n"}}
		written, err := writeHCLFiles(dir, files)
		Expect(err).NotTo(HaveOccurred())
		Expect(written).To(Equal([]string{filepath.Join(dir, "a.hcl"), filepath.Join(dir, "b.hcl")}))
		root, err := os.OpenRoot(dir)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(root.Close)
		Expect(root.ReadFile("b.hcl")).To(Equal([]byte("schema \"dbo\" {}\n")))
	})

	It("documents the HCL flags and what the export refuses in its help", func() {
		root := newRootCommand(strings.NewReader(""))
		out := &bytes.Buffer{}
		root.SetOut(out)
		root.SetErr(&bytes.Buffer{})
		root.SetArgs([]string{"schema", "inspect", "--help", "--no-color"})
		Expect(root.Execute()).To(Succeed())
		for _, want := range []string{"--hcl", "--table", "--columns", "--out", "--document", "--omit-unsupported", "ROWGUIDCOL", "# omitted:"} {
			Expect(out.String()).To(ContainSubstring(want))
		}
	})
})
