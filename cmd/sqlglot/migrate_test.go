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
