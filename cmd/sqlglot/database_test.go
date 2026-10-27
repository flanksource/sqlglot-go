package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/sqlglot-go/database/history"
	"github.com/flanksource/sqlglot-go/database/migrate/review"
	"github.com/flanksource/sqlglot-go/database/migrate/workflow"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/zclconf/go-cty/cty"
)

var _ = Describe("standalone database commands", func() {
	It("parses typed bundle variables and rejects duplicate or invalid values", func() {
		values, err := workflow.Variables([]string{`prefix="app"`, `limit=10`, `enabled=true`})
		Expect(err).NotTo(HaveOccurred())
		Expect(cty.ObjectVal(values).RawEquals(cty.ObjectVal(map[string]cty.Value{"prefix": cty.StringVal("app"), "limit": cty.NumberIntVal(10), "enabled": cty.BoolVal(true)}))).To(BeTrue())
		_, err = workflow.Variables([]string{`prefix="app"`, `prefix="other"`})
		Expect(err).To(MatchError(ContainSubstring("duplicate variable")))
		_, err = workflow.Variables([]string{`limit=unknown`})
		Expect(err).To(MatchError(ContainSubstring("evaluate variable limit")))
	})
	It("inspects, plans, applies and repeats a saved SQLite artifact", func() {
		dir := GinkgoT().TempDir()
		dbPath := filepath.Join(dir, "cli.db")
		db, err := sql.Open("sqlite", dbPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(db.Ping()).To(Succeed())
		Expect(db.Close()).To(Succeed())
		desired := filepath.Join(dir, "desired.hcl")
		Expect(os.WriteFile(desired, []byte("schema \"main\" {}\ntable \"items\" {\n schema = schema.main\n column \"id\" { type = integer }\n}"), 0600)).To(Succeed())
		run := func(args ...string) (string, error) {
			root := newRootCommand(strings.NewReader(""))
			out := &bytes.Buffer{}
			root.SetOut(out)
			root.SetErr(out)
			root.SetArgs(args)
			err := root.Execute()
			return out.String(), err
		}
		output, err := run("schema", "inspect", "--dsn", dbPath, "--snapshot")
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(ContainSubstring("\"dialect\": \"sqlite\""))
		output, err = run("migrate", "plan", "--dsn", dbPath, "--desired", desired)
		Expect(err).NotTo(HaveOccurred())
		var artifact review.Artifact
		Expect(json.Unmarshal([]byte(output), &artifact)).To(Succeed())
		Expect(artifact.Hash).NotTo(BeEmpty())
		artifactPath := filepath.Join(dir, "reviewed.json")
		Expect(os.WriteFile(artifactPath, []byte(output), 0600)).To(Succeed())
		output, err = run("migrate", "apply", "--dsn", dbPath, "--artifact", artifactPath, "--hash", artifact.Hash)
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(ContainSubstring("\"outcome\": \"applied\""))
		output, err = run("migrate", "apply", "--dsn", dbPath, "--artifact", artifactPath, "--hash", artifact.Hash)
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(ContainSubstring("already-applied"))

		store, err := history.Open(historyPath)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(store.Close)
		recorded, err := store.List(GinkgoT().Context(), history.Filter{Connection: dbPath})
		Expect(err).NotTo(HaveOccurred())
		modes := []string{}
		for _, r := range recorded {
			var payload struct {
				Mode string `json:"mode"`
			}
			full, err := store.Get(GinkgoT().Context(), r.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(json.Unmarshal(full.Payload, &payload)).To(Succeed())
			modes = append(modes, string(r.Kind)+" "+payload.Mode+" "+string(r.Status))
		}
		Expect(modes).To(ConsistOf("inspection  succeeded", "migration plan succeeded", "migration apply succeeded", "migration apply succeeded"))
	})
	It("inspects and lists a named connection from the connections directory", func() {
		dir := GinkgoT().TempDir()
		dbPath := filepath.Join(dir, "named.db")
		db, err := sql.Open("sqlite", dbPath)
		Expect(err).NotTo(HaveOccurred())
		_, err = db.Exec("CREATE TABLE widgets (id integer PRIMARY KEY)")
		Expect(err).NotTo(HaveOccurred())
		Expect(db.Close()).To(Succeed())
		GinkgoT().Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
		connectionsDir := filepath.Join(dir, "connections")
		Expect(os.Mkdir(connectionsDir, 0700)).To(Succeed())
		local := "apiVersion: mission-control.flanksource.com/v1\nkind: Connection\nspec: {type: sqlite, url: {value: '" + dbPath + "'}}\n"
		Expect(os.WriteFile(filepath.Join(connectionsDir, "local.yaml"), []byte(local), 0600)).To(Succeed())
		run := func(args ...string) (string, error) {
			root := newRootCommand(strings.NewReader(""))
			out := &bytes.Buffer{}
			root.SetOut(out)
			root.SetErr(out)
			root.SetArgs(args)
			err := root.Execute()
			return out.String(), err
		}
		output, err := run("schema", "inspect", "--dsn", "connection://local", "--connections-dir", connectionsDir, "--snapshot")
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(ContainSubstring("widgets"))
		summaries, err := listConnections(ConnectionListOptions{Dir: connectionsDir})
		Expect(err).NotTo(HaveOccurred())
		Expect(summaries).To(Equal([]ConnectionSummary{{Name: "local", Type: "sqlite", Layer: "app", Source: filepath.Join(connectionsDir, "local.yaml")}}))
	})
	It("requires an explicit reviewed hash before connecting", func() {
		root := newRootCommand(strings.NewReader(""))
		root.SetArgs([]string{"migrate", "apply", "--dsn", "missing.db", "--artifact", "missing.json"})
		Expect(root.Execute()).To(MatchError(ContainSubstring("hash")))
	})
})
