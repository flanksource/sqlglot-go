package workflow

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("bundle file content detection", func() {
	DescribeTable("detects the kind of a file from its content when its extension does not name one",
		func(name, content string, want ContentKind, ok bool) {
			kind, detected := DetectContent(name, content)
			Expect([]any{kind, detected}).To(Equal([]any{want, ok}))
		},
		Entry("T-SQL behind a banner comment", "SQLData.xml", "/****** Object: proc ******/\r\nSET ANSI_NULLS ON\r\nGO", SQLContent, true),
		Entry("a block opened with BEGIN", "SQLData.xml", "\n\nBEGIN\n  UPDATE dbo.t SET a = 1\nEND", SQLContent, true),
		Entry("a phase header then a statement", "seed", "-- phase: pre\nINSERT INTO t VALUES (1)", SQLContent, true),
		Entry("a lower-case statement", "query.txt", "  delete from t", SQLContent, true),
		Entry("an Atlas schema", "schema.txt", "schema \"dbo\" {}\ntable \"t\" {\n  schema = schema.dbo\n}", HCLContent, true),
		Entry("HCL behind a comment", "tables", "# managed\ntable \"t\" {\n  schema = schema.dbo\n}", HCLContent, true),
		Entry("an XML document", "data.xml", "<?xml version=\"1.0\"?><SQLSCRIPT/>", ContentKind(""), false),
		Entry("prose", "README", "This folder holds scripts.", ContentKind(""), false),
		Entry("a comment-only file", "empty.txt", "-- nothing yet\n/* later */", ContentKind(""), false),
		Entry("a .sql file is SQL whatever it holds", "draft.sql", "", SQLContent, true),
		Entry("a .hcl file is HCL whatever it holds", "broken.HCL", "table {", HCLContent, true),
	)

	It("names each file by its detected kind so the bundle loaders read it, keeping .sql and .hcl paths", func() {
		files, err := ClassifyFiles([]File{
			{Path: "PostDeployment_Fix/SQLData.xml", Content: "DELETE FROM dbo.t"},
			{Path: "schema", Content: "table \"t\" {\n  schema = schema.dbo\n}"},
			{Path: "pre/010_seed.sql", Content: "-- phase: pre\nSELECT 1"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(files).To(Equal([]File{
			{Path: "PostDeployment_Fix/SQLData.sql", Content: "DELETE FROM dbo.t"},
			{Path: "schema.hcl", Content: "table \"t\" {\n  schema = schema.dbo\n}"},
			{Path: "pre/010_seed.sql", Content: "-- phase: pre\nSELECT 1"},
		}))
	})

	It("refuses a file that holds neither SQL nor HCL, naming it", func() {
		_, err := ClassifyFiles([]File{{Path: "scripts/A/data.xml", Content: "<SQLSCRIPT/>"}})
		Expect(err).To(MatchError(ContainSubstring(`scripts/A/data.xml holds neither SQL nor HCL`)))
	})

	It("refuses two files that are named alike once classified", func() {
		_, err := ClassifyFiles([]File{{Path: "a.sql", Content: "SELECT 1"}, {Path: "a.txt", Content: "SELECT 2"}})
		Expect(err).To(MatchError(ContainSubstring(`a.txt and a.sql are both read as a.sql`)))
	})
})
