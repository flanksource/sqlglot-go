package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/flanksource/sqlglot-go/database/compare"
	"github.com/flanksource/sqlglot-go/database/connections"
	"github.com/flanksource/sqlglot-go/database/history"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ui server", func() {
	const spaBody = "<!doctype html><title>sqlglot</title>"
	var (
		dir    string
		server *httptest.Server
	)
	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		GinkgoT().Setenv("XDG_CONFIG_HOME", GinkgoT().TempDir())
		writeConnectionFile(dir, "orders.yaml", sqliteConnection(filepath.Join(dir, "orders.db")))
		spa := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, spaBody) })
		handler, err := newUIHandler(uiHandlerOptions{Root: newRootCommand(strings.NewReader("")), ConnectionsDir: dir, UI: spa})
		Expect(err).NotTo(HaveOccurred())
		server = httptest.NewServer(handler)
		DeferCleanup(server.Close)
	})

	request := func(method, path string, body any) (int, string) {
		var reader io.Reader
		if body != nil {
			data, err := json.Marshal(body)
			Expect(err).NotTo(HaveOccurred())
			reader = bytes.NewReader(data)
		}
		req, err := http.NewRequest(method, server.URL+path, reader)
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		resp, err := server.Client().Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(resp.Body.Close()).To(Succeed()) }()
		data, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		return resp.StatusCode, string(data)
	}

	It("serves a writable connection dashboard listing the YAML catalog", func() {
		status, body := request(http.MethodGet, "/api/v1/connections/dashboard", nil)
		Expect(status).To(Equal(http.StatusOK), body)
		var dashboard struct {
			ReadOnly    bool `json:"readOnly"`
			Connections []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"connections"`
		}
		Expect(json.Unmarshal([]byte(body), &dashboard)).To(Succeed())
		Expect(dashboard.ReadOnly).To(BeFalse())
		Expect(dashboard.Connections).To(HaveLen(1))
		Expect(dashboard.Connections[0].Name).To(Equal("orders"))
		Expect(dashboard.Connections[0].Type).To(Equal("sqlite"))
	})

	It("creates a typed connection file in sqlglot's own layer through POST /api/v1/connection", func() {
		status, body := request(http.MethodPost, "/api/v1/connection", map[string]any{
			"name": "billing", "type": "sqlite", "url": "sqlite://" + filepath.Join(dir, "billing.db"),
		})
		Expect(status).To(BeNumerically("<", 300), body)
		catalog, err := connections.Open(dir)
		Expect(err).NotTo(HaveOccurred())
		entry, err := catalog.Find("connection://billing")
		Expect(err).NotTo(HaveOccurred())
		Expect(entry).NotTo(BeNil())
		Expect(entry.Layer.Name).To(Equal("app"))
		Expect(entry.Path).To(Equal(filepath.Join(dir, "billing.yaml")))
		Expect(os.ReadFile(entry.Path)).To(ContainSubstring("kind: Connection"))
	})

	DescribeTable("refuses connections outside sqlglot's policy",
		func(connection map[string]any, code string) {
			status, body := request(http.MethodPost, "/api/v1/connection", connection)
			Expect(status).To(Equal(http.StatusBadRequest), body)
			Expect(body).To(ContainSubstring(code))
		},
		Entry("a type sqlglot cannot open", map[string]any{"name": "cache", "type": "redis", "url": "redis://cache.example"}, "invalid_type"),
		Entry("a literal password", map[string]any{"name": "billing", "type": "postgres", "url": "postgres://$(username):$(password)@db.example/billing", "username": "secret://db/username", "password": "hunter2-example"}, "literal_secret"),
	)

	It("serves a connection form schema offering only SQL types and secret references", func() {
		req, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/connection", nil)
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Accept", "application/schema+json")
		resp, err := server.Client().Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(resp.Body.Close()).To(Succeed()) }()
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var schema struct {
			Properties struct {
				Type struct {
					Enum []string `json:"enum"`
				} `json:"type"`
			} `json:"properties"`
			SecretSources []string `json:"x-clicky-secret-sources"`
			URLSources    []string `json:"x-clicky-url-sources"`
		}
		Expect(json.NewDecoder(resp.Body).Decode(&schema)).To(Succeed())
		Expect(schema.Properties.Type.Enum).To(ConsistOf(connections.SQLTypes))
		Expect(schema.SecretSources).To(Equal([]string{"secret", "configmap", "helm"}))
		Expect(schema.URLSources).To(Equal([]string{"secret", "configmap", "helm", "value"}))
	})

	It("serves the secret picker endpoints the connection form loads", func() {
		status, body := request(http.MethodGet, "/api/v1/connections/namespaces", nil)
		Expect(status).NotTo(Equal(http.StatusNotFound), body)
	})

	It("compares and patches connections named from the served directory", func() {
		for name, ddl := range map[string]string{
			"reference": "CREATE TABLE items (id integer NOT NULL); CREATE TABLE tags (id integer NOT NULL)",
			"target":    "CREATE TABLE items (id integer NOT NULL)",
		} {
			db, err := sql.Open("sqlite", filepath.Join(dir, name+".db"))
			Expect(err).NotTo(HaveOccurred())
			_, err = db.Exec(ddl)
			Expect(err).NotTo(HaveOccurred())
			Expect(db.Close()).To(Succeed())
			writeConnectionFile(dir, name+".yaml", sqliteConnection(filepath.Join(dir, name+".db")))
		}
		comparison := uuid.NewString()
		sides := map[string]any{"reference": "connection://reference", "target": "connection://target", "run": comparison}
		status, body := request(http.MethodPost, "/api/v1/schema/compare", sides)
		Expect(status).To(Equal(http.StatusOK), body)
		var result compare.Result
		Expect(json.Unmarshal([]byte(body), &result)).To(Succeed())
		addTags := `["add","table","main","tags",""]`
		Expect(result.Changes).To(HaveLen(1))
		Expect(result.Changes[0].ID).To(Equal(addTags))

		status, body = request(http.MethodGet, "/api/v1/tasks/"+comparison, nil)
		Expect(status).To(Equal(http.StatusOK), body)
		Expect(body).To(ContainSubstring(`"Plan all differences"`))

		patchRun := uuid.NewString()
		sides["select"], sides["diff-hash"], sides["run"], sides["comparison"] = []string{addTags}, result.DiffHash, patchRun, comparison
		status, body = request(http.MethodPost, "/api/v1/schema/patch", sides)
		Expect(status).To(Equal(http.StatusOK), body)
		var patch compare.Patch
		Expect(json.Unmarshal([]byte(body), &patch)).To(Succeed())
		Expect(patch.Selection).To(Equal([]string{addTags}))
		Expect(patch.SQL).To(ContainSubstring("CREATE TABLE `tags`"))

		By("reviewing both runs with the databases gone")
		Expect(os.Remove(filepath.Join(dir, "reference.db"))).To(Succeed())
		Expect(os.Remove(filepath.Join(dir, "target.db"))).To(Succeed())
		status, body = request(http.MethodGet, "/api/v1/history/"+comparison, nil)
		Expect(status).To(Equal(http.StatusOK), body)
		var recorded history.Record
		Expect(json.Unmarshal([]byte(body), &recorded)).To(Succeed())
		var replayed compare.Result
		Expect(json.Unmarshal(recorded.Payload, &replayed)).To(Succeed())
		Expect([]any{recorded.Kind, replayed}).To(Equal([]any{history.KindComparison, result}))

		status, body = request(http.MethodGet, "/api/v1/history?"+url.Values{"kind": {"patch"}}.Encode(), nil)
		Expect(status).To(Equal(http.StatusOK), body)
		var patches []history.Record
		Expect(json.Unmarshal([]byte(body), &patches)).To(Succeed())
		Expect(patches).To(HaveLen(1))
		Expect([]string{patches[0].ID, patches[0].Parent}).To(Equal([]string{patchRun, comparison}))

		status, body = request(http.MethodGet, "/api/v1/history/never-recorded", nil)
		Expect(status).To(Equal(http.StatusNotFound), body)
	})

	It("plans and applies uploaded content and reopens the recorded result", func() {
		db, err := sql.Open("sqlite", filepath.Join(dir, "orders.db"))
		Expect(err).NotTo(HaveOccurred())
		Expect(db.Ping()).To(Succeed())
		Expect(db.Close()).To(Succeed())
		planned := uuid.NewString()
		status, body := request(http.MethodPost, "/api/v1/migrate/plan", map[string]any{
			"dsn": "connection://orders", "run": planned,
			"source": map[string]any{"kind": "hcl", "content": "schema \"main\" {}\ntable \"items\" {\n schema = schema.main\n column \"id\" { type = integer }\n}"},
		})
		Expect(status).To(Equal(http.StatusOK), body)
		var plan struct {
			Hash       string          `json:"hash"`
			Artifact   json.RawMessage `json:"artifact"`
			Applicable bool            `json:"applicable"`
		}
		Expect(json.Unmarshal([]byte(body), &plan)).To(Succeed())
		Expect(plan.Applicable).To(BeTrue())
		status, body = request(http.MethodPost, "/api/v1/migrate/plan", map[string]any{"source": map[string]any{"kind": "artifact", "content": string(plan.Artifact)}})
		Expect(status).To(Equal(http.StatusOK), body)
		Expect(body).To(ContainSubstring(plan.Hash))
		applied := uuid.NewString()
		status, body = request(http.MethodPost, "/api/v1/migrate/apply", map[string]any{"dsn": "connection://orders", "run": applied, "parent": planned, "artifactContent": plan.Artifact, "hash": plan.Hash, "ack": []string{}})
		Expect(status).To(Equal(http.StatusOK), body)
		Expect(body).To(ContainSubstring("applied"))
		status, body = request(http.MethodGet, "/api/v1/history/"+applied, nil)
		Expect(status).To(Equal(http.StatusOK), body)
		Expect(body).To(ContainSubstring(planned))
		status, body = request(http.MethodPost, "/api/v1/schema/inspect", map[string]any{"dsn": "connection://orders", "snapshot": true})
		Expect(status).To(Equal(http.StatusOK), body)
		Expect(body).To(ContainSubstring("items"))
	})

	It("routes the root command's clicky operations under /api/v1", func() {
		status, body := request(http.MethodGet, "/api/v1/connections?"+url.Values{"connections-dir": {dir}}.Encode(), nil)
		Expect(status).To(Equal(http.StatusOK), body)
		Expect(body).To(ContainSubstring(`"orders"`))
	})

	It("accepts SQL content and returns source diagnostics to the editor", func() {
		status, body := request(http.MethodPost, "/api/v1/parse", map[string]any{"sql": "SELECT 1"})
		Expect(status).To(Equal(http.StatusOK), body)
		Expect(body).To(ContainSubstring(`"sql"`))
		status, body = request(http.MethodPost, "/api/v1/parse", map[string]any{"sql": "PRINT\nGO", "dialect": "tsql"})
		Expect(status).To(Equal(http.StatusBadRequest), body)
		Expect(body).To(ContainSubstring(`"diagnostics"`))
		Expect(body).To(ContainSubstring(`"line":1`))
	})

	It("filters and renders supplied data without a database", func() {
		status, body := request(http.MethodPost, "/api/v1/render", map[string]any{"value": []map[string]any{{"name": "keep", "count": 2}, {"name": "omit", "count": 0}}, "output-format": "json", "expression": "count > 0"})
		Expect(status).To(Equal(http.StatusOK), body)
		var result struct {
			Data []byte `json:"data"`
		}
		Expect(json.Unmarshal([]byte(body), &result)).To(Succeed())
		Expect(string(result.Data)).To(ContainSubstring("keep"))
		Expect(string(result.Data)).NotTo(ContainSubstring("omit"))
	})
	It("rejects server paths and unknown fields in content requests before opening a database", func() {
		for path, body := range map[string]map[string]any{
			"/api/v1/parse":          {"file": "/server/schema.sql"},
			"/api/v1/migrate/plan":   {"desired": "/server/schema.hcl"},
			"/api/v1/migrate/review": {"artifactContent": map[string]any{}, "unexpected": true},
		} {
			status, response := request(http.MethodPost, path, body)
			Expect(status).To(Equal(http.StatusBadRequest), response)
		}
	})

	It("publishes schema compare and patch as POST operations taking every flag, embedded ones included", func() {
		status, body := request(http.MethodGet, "/api/openapi.json", nil)
		Expect(status).To(Equal(http.StatusOK), body)
		var spec struct {
			Paths map[string]map[string]struct {
				Parameters []struct {
					Name string `json:"name"`
				} `json:"parameters"`
				RequestBody struct {
					Content map[string]struct {
						Schema struct {
							Properties map[string]any `json:"properties"`
						} `json:"schema"`
					} `json:"content"`
				} `json:"requestBody"`
			} `json:"paths"`
		}
		Expect(json.Unmarshal([]byte(body), &spec)).To(Succeed())
		inputs := func(path string) []string {
			operation, ok := spec.Paths[path]["post"]
			Expect(ok).To(BeTrue(), "POST %s is not routed in %v", path, slices.Collect(maps.Keys(spec.Paths)))
			var names []string
			for _, parameter := range operation.Parameters {
				names = append(names, parameter.Name)
			}
			for name := range operation.RequestBody.Content["application/json"].Schema.Properties {
				names = append(names, name)
			}
			return names
		}
		compareFlags := []string{"reference", "target", "schemas", "connections-dir"}
		Expect(inputs("/api/v1/schema/compare")).To(ContainElements(compareFlags))
		Expect(inputs("/api/v1/schema/patch")).To(ContainElements(append(compareFlags, "select", "diff-hash")))
		Expect(inputs("/api/v1/migrate/plan")).To(ContainElements("source", "artifactContent"))
		Expect(inputs("/api/v1/migrate/apply")).To(ContainElements("artifactContent", "hash", "ack"))
	})

	It("serves the SPA outside /api and 404s unknown API paths", func() {
		status, body := request(http.MethodGet, "/compare?cmp.stage=setup", nil)
		Expect(status).To(Equal(http.StatusOK))
		Expect(body).To(Equal(spaBody))
		status, _ = request(http.MethodGet, "/api/v1/no-such-operation", nil)
		Expect(status).To(Equal(http.StatusNotFound))
	})

	It("refuses cross-origin API requests", func() {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/connection", strings.NewReader(`{"name":"evil","type":"sqlite","url":"sqlite:///evil.db"}`))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://attacker.example")
		resp, err := server.Client().Do(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Body.Close()).To(Succeed())
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
		Expect(filepath.Join(dir, "evil.yaml")).NotTo(BeAnExistingFile())
	})
})

var _ = DescribeTable("ui API origin guard",
	func(origin string, allowed []string, refused bool) {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9150/api/v1/schema/compare", nil)
		req.RemoteAddr = "127.0.0.1:50000"
		req.Header.Set("Origin", origin)
		reason := refuseUIRequest(req, uiHandlerOptions{AllowedOrigins: allowed})
		if refused {
			Expect(reason).To(ContainSubstring("cross-origin"))
		} else {
			Expect(reason).To(BeEmpty())
		}
	},
	Entry("same origin", "http://127.0.0.1:9150", nil, false),
	Entry("vite dev origin when allowed", "http://localhost:5173", []string{"localhost:5173"}, false),
	Entry("vite dev origin when not in dev mode", "http://localhost:5173", nil, true),
	Entry("other port of an allowed host", "http://localhost:5174", []string{"localhost:5173"}, true),
)

var _ = DescribeTable("ui listen address",
	func(addr string, listenAny bool, message string) {
		err := checkUIAddr(addr, listenAny)
		if message == "" {
			Expect(err).NotTo(HaveOccurred())
			return
		}
		Expect(err).To(MatchError(ContainSubstring(message)))
	},
	Entry("loopback IPv4", "127.0.0.1:9150", false, ""),
	Entry("localhost", "localhost:9150", false, ""),
	Entry("loopback IPv6", "[::1]:9150", false, ""),
	Entry("all interfaces without --listen-any", ":9150", false, "--listen-any"),
	Entry("routable address without --listen-any", "0.0.0.0:9150", false, "--listen-any"),
	Entry("routable address with --listen-any", "0.0.0.0:9150", true, ""),
	Entry("missing port", "127.0.0.1", false, "missing port"),
)

var _ = Describe("ui command flags", func() {
	It("listens on loopback port 9150 by default and leaves the Vite port to the OS", func() {
		ui, _, err := newRootCommand(strings.NewReader("")).Find([]string{"ui"})
		Expect(err).NotTo(HaveOccurred())
		Expect(ui.Flags().Lookup("addr").DefValue).To(Equal("127.0.0.1:9150"))
		Expect(ui.Flags().Lookup("dev-port")).To(BeNil())
	})
})

var _ = Describe("freeLoopbackPort", func() {
	It("returns a loopback port that is free to bind", func() {
		port, err := freeLoopbackPort()
		Expect(err).NotTo(HaveOccurred())
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		Expect(err).NotTo(HaveOccurred())
		Expect(listener.Close()).To(Succeed())
	})
})

func writeConnectionFile(dir, file, body string) {
	Expect(os.WriteFile(filepath.Join(dir, file), []byte(body), 0o600)).To(Succeed())
}

// sqliteConnection is a typed Connection resource opening the SQLite file at path.
func sqliteConnection(path string) string {
	return "apiVersion: mission-control.flanksource.com/v1\nkind: Connection\nspec: {type: sqlite, url: {value: 'sqlite://" + path + "'}}\n"
}
