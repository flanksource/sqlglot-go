package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/sqlglot-go/database/compare"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("schema compare and patch commands", func() {
	It("compares two connections, patches a selected change and applies it to the target", func() {
		dir := GinkgoT().TempDir()
		create := func(name, ddl string) string {
			path := filepath.Join(dir, name)
			db, err := sql.Open("sqlite", path)
			Expect(err).NotTo(HaveOccurred())
			_, err = db.Exec(ddl)
			Expect(err).NotTo(HaveOccurred())
			Expect(db.Close()).To(Succeed())
			return path
		}
		reference := create("reference.db", "CREATE TABLE items (id integer NOT NULL, note text); CREATE TABLE tags (id integer NOT NULL)")
		target := create("target.db", "CREATE TABLE items (id integer NOT NULL)")
		// Typed clicky commands print to os.Stdout rather than the cobra writer.
		run := func(args ...string) (string, error) {
			root := newRootCommand(strings.NewReader(""))
			out := &bytes.Buffer{}
			root.SetOut(out)
			root.SetErr(out)
			root.SetArgs(append(args, "--format", "json"))
			read, write, err := os.Pipe()
			Expect(err).NotTo(HaveOccurred())
			stdout := os.Stdout
			os.Stdout = write
			captured := make(chan []byte)
			go func() {
				data, _ := io.ReadAll(read)
				captured <- data
			}()
			err = root.Execute()
			os.Stdout = stdout
			Expect(write.Close()).To(Succeed())
			return out.String() + string(<-captured), err
		}
		output, err := run("schema", "compare", "--reference", reference, "--target", target)
		Expect(err).NotTo(HaveOccurred())
		var result compare.Result
		Expect(json.Unmarshal([]byte(output), &result)).To(Succeed())
		Expect(result.Changes).To(HaveLen(2))

		addTags := `["add","table","main","tags",""]`
		output, err = run("schema", "patch", "--reference", reference, "--target", target, "--select", addTags, "--diff-hash", result.DiffHash)
		Expect(err).NotTo(HaveOccurred())
		var patch compare.Patch
		Expect(json.Unmarshal([]byte(output), &patch)).To(Succeed())
		Expect(patch.Selection).To(Equal([]string{addTags}))
		artifactPath := filepath.Join(dir, "artifact.json")
		Expect(os.WriteFile(artifactPath, patch.Artifact, 0600)).To(Succeed())

		output, err = run("migrate", "apply", "--dsn", target, "--artifact", artifactPath, "--hash", patch.Hash)
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(ContainSubstring("\"outcome\": \"applied\""))
		output, err = run("schema", "compare", "--reference", reference, "--target", target)
		Expect(err).NotTo(HaveOccurred())
		Expect(json.Unmarshal([]byte(output), &result)).To(Succeed())
		Expect(result.Changes).To(HaveLen(1))
		Expect(result.Changes[0].ID).To(Equal(`["add","column","main","items","note"]`))
	})
})
