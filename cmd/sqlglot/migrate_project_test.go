package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const localProjectSchema = `schema "public" {}
table "items" {
  schema = schema.public
  column "id" { type = integer }
}
`

var _ = Describe("migration project discovery", func() {
	It("prefers the current folder's config over the working tree root", func() {
		dir := GinkgoT().TempDir()
		Expect(os.Mkdir(filepath.Join(dir, ".git"), 0700)).To(Succeed())
		current := filepath.Join(dir, "nested")
		Expect(os.Mkdir(current, 0700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, ".sqlglot-go.yaml"), []byte("version: 1\nconnections: {dev: 'connection://root-dev'}\n"), 0600)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(current, ".sqlglot-go.yaml"), []byte("version: 1\nconnections: {dev: 'connection://nested-dev'}\n"), 0600)).To(Succeed())
		project, err := discoverMigrationProject(current)
		Expect(err).NotTo(HaveOccurred())
		Expect(project.dir).To(Equal(current))
		opts := MigrationOptions{Target: "dev"}
		Expect(project.resolveAlias(&opts)).To(Succeed())
		Expect(opts.DSN).To(Equal("connection://nested-dev"))
	})

	It("plans the Git root project and resolves a target relative to its YAML from a subdirectory", func() {
		dir := GinkgoT().TempDir()
		Expect(os.Mkdir(filepath.Join(dir, ".git"), 0700)).To(Succeed())
		nested := filepath.Join(dir, "nested")
		Expect(os.Mkdir(nested, 0700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, ".sqlglot-go.yaml"), []byte("version: 1\nmappings: [{match: 'schema.hcl'}]\nconnections: {dev: './dev.db'}\n"), 0600)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "schema.hcl"), []byte(localProjectSchema), 0600)).To(Succeed())
		db, err := sql.Open("sqlite", filepath.Join(dir, "dev.db"))
		Expect(err).NotTo(HaveOccurred())
		Expect(db.Ping()).To(Succeed())
		Expect(db.Close()).To(Succeed())
		cwd, err := os.Getwd()
		Expect(err).NotTo(HaveOccurred())
		Expect(os.Chdir(nested)).To(Succeed())
		DeferCleanup(os.Chdir, cwd)

		root := newRootCommand(strings.NewReader(""))
		var stdout bytes.Buffer
		root.SetOut(&stdout)
		root.SetErr(&bytes.Buffer{})
		root.SetArgs([]string{"migrate", "plan", "--target", "dev", "--out", filepath.Join(dir, "plan.json")})
		Expect(root.Execute()).To(Succeed())
		Expect(stdout.String()).To(SatisfyAll(ContainSubstring("CREATE TABLE"), ContainSubstring(`"path": "schema.hcl"`)))
	})
})
