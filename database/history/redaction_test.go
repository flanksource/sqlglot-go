package history

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("credential-safe recorded operations", func() {
	It("preserves artifact bytes and large integer literals when redacting a surrounding connection", func() {
		artifact := json.RawMessage(`{"version":2,"number":9007199254740993,"hash":"unchanged"}`)
		record := Record{ID: "artifact", Kind: KindMigration}
		connection := url.URL{Scheme: "postgres", User: url.UserPassword("operator", "example"), Host: "localhost", Path: "/example"}
		Expect(record.Complete(map[string]any{"artifact": artifact, "target": connection.String()}, nil)).To(Succeed())
		var result struct {
			Artifact json.RawMessage `json:"artifact"`
		}
		Expect(json.Unmarshal(record.Payload, &result)).To(Succeed())
		Expect(result.Artifact).To(Equal(artifact))
	})
	It("redacts DSNs in payloads and errors without changing schema content", func() {
		connection := url.URL{Scheme: "postgres", User: url.UserPassword("operator", "example"), Host: "localhost", Path: "/example", RawQuery: "password=private-query"}
		dsn := connection.String()
		record := Record{ID: "redaction", Kind: KindComparison, Target: dsn}
		err := record.Complete(map[string]any{"target": dsn, "nested": map[string]any{"reference": dsn}, "sql": "SELECT 'unchanged'"}, errors.New("connect "+dsn))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(record.Payload)).NotTo(ContainSubstring("operator:example"))
		Expect(string(record.Payload)).NotTo(ContainSubstring("private-query"))
		Expect(string(record.Payload)).To(ContainSubstring("SELECT 'unchanged'"))
		Expect(record.Error).NotTo(ContainSubstring("operator:example"))
	})
	It("atomically refuses reuse of a claimed run ID", func() {
		store, err := Open(filepath.Join(GinkgoT().TempDir(), "history.db"))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(store.Close)
		Expect(store.Claim(context.Background(), "one-run")).To(Succeed())
		Expect(store.Claim(context.Background(), "one-run")).To(MatchError(ContainSubstring("already exists")))
	})
})
