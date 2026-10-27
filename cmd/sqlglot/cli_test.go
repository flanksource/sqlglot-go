package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCLI(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SQL CLI")
}

// Every spec records its runs to its own history file, never the user's.
var _ = BeforeEach(func() { historyPath = filepath.Join(GinkgoT().TempDir(), "history.db") })

var _ = Describe("SQL commands", func() {
	const query = "SELECT a FROM t"

	It("reads exactly one source and rejects empty input", func() {
		path := filepath.Join(GinkgoT().TempDir(), "query.sql")
		Expect(os.WriteFile(path, []byte(query), 0600)).To(Succeed())
		Expect(readSQL(InputOptions{File: path}, strings.NewReader(""))).To(Equal(query))
		Expect(readSQL(InputOptions{}, strings.NewReader(query))).To(Equal(query))
		_, err := readSQL(InputOptions{SQL: query, File: path}, strings.NewReader(""))
		Expect(err).To(MatchError(ContainSubstring("one SQL source")))
		_, err = readSQL(InputOptions{}, strings.NewReader("  "))
		Expect(err).To(MatchError(ContainSubstring("empty SQL")))
	})

	It("returns a reference-shaped parse tree", func() {
		result, err := parseSQL(InputOptions{SQL: query}, strings.NewReader(""))
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Class).To(Equal("Select"))
		Expect(result.Tree).NotTo(BeEmpty())
		Expect(result.SQL).To(Equal("SELECT a\nFROM t"))
		Expect(result.Pretty().ANSI()).To(ContainSubstring("FROM"))
		Expect(result.Pretty().ANSI()).NotTo(ContainSubstring("from_"))
	})

	It("accepts T-SQL PRINT as a parsed statement", func() {
		result, err := parseSQL(InputOptions{SQL: "PRINT N'hello' + @name", Dialect: "tsql"}, strings.NewReader(""))
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Class).To(Equal("Print"))
		Expect(result.Tree).To(ContainSubstring("National"))
	})

	It("keeps GO and block boundaries on separate lines in parsed SQL", func() {
		result, err := parseSQL(InputOptions{SQL: "SET ANSI_NULLS ON\nGO\nCREATE OR ALTER PROCEDURE p AS\nBEGIN\nSET NOCOUNT ON\nEND", Dialect: "tsql"}, strings.NewReader(""))
		Expect(err).NotTo(HaveOccurred())
		Expect(result.SQL).To(ContainSubstring("SET ANSI_NULLS ON\nGO\nCREATE OR ALTER PROCEDURE"))
		Expect(result.SQL).To(ContainSubstring("BEGIN\nSET NOCOUNT ON\nEND"))
	})

	It("resets clause formatting at a GO batch boundary", func() {
		result, err := parseSQL(InputOptions{SQL: "SET ANSI_NULLS ON\nGO\nCREATE TABLE t (id INT) ON [PRIMARY]", Dialect: "tsql"}, strings.NewReader(""))
		Expect(err).NotTo(HaveOccurred())
		Expect(result.SQL).To(ContainSubstring("CREATE TABLE t (id INT)\n  ON [PRIMARY]"))
	})

	It("formats PRINT and following END across CRLF lines", func() {
		sql := "BEGIN\r\nIF 1 = 0\r\nBEGIN\r\nSELECT 1\r\nEND\r\nELSE\r\nBEGIN\r\nPRINT 'hello'\r\nEND\r\nEND"
		result, err := parseSQL(InputOptions{SQL: sql, Dialect: "tsql"}, strings.NewReader(""))
		Expect(err).NotTo(HaveOccurred())
		Expect(result.SQL).To(ContainSubstring("PRINT 'hello'\nEND\nEND"))
		Expect(result.SQL).NotTo(ContainSubstring("\r"))
	})

	It("rejects malformed SQL and accepts a supported query", func() {
		result, err := validateSQL(InputOptions{SQL: query}, strings.NewReader(""))
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(ValidationResult{Valid: true, Class: "Select"}))
		_, err = validateSQL(InputOptions{SQL: "SELECT FROM"}, strings.NewReader(""))
		Expect(err).To(HaveOccurred())
	})

	It("rejects commands, writes, and batches as queries", func() {
		for _, input := range []InputOptions{
			{SQL: "DROP TABLE t"},
			{SQL: "SELECT 1; SELECT 2"},
			{SQL: "PRINT 'hello'", Dialect: "tsql"},
		} {
			_, err := validateSQL(input, strings.NewReader(""))
			Expect(err).To(MatchError(ContainSubstring("single query")), input.SQL)
		}
	})

	It("formats without changing the SQL tokens", func() {
		result, err := formatSQL(InputOptions{SQL: "select a,b from t", Dialect: ""}, strings.NewReader(""))
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal("select a,\n  b\nfrom t"))
	})

	It("lists usable dialect names", func() {
		result := availableDialects()
		Expect(result[0]).To(Equal(DialectResult{Name: "neutral"}))
		Expect(result).To(ContainElement(DialectResult{Name: "tsql"}))
	})

	It("labels the tokenizer column as the token end", func() {
		tokens, err := tokenizeSQL(InputOptions{SQL: "SELECT a"}, strings.NewReader(""))
		Expect(err).NotTo(HaveOccurred())
		Expect(tokens[0].EndColumn).To(Equal(len("SELECT")))
	})

	It("rejects extra positional SQL arguments instead of truncating them", func() {
		root := newRootCommand(strings.NewReader(""))
		for _, name := range []string{"parse", "validate", "format", "tokens"} {
			command, _, err := root.Find([]string{name})
			Expect(err).NotTo(HaveOccurred())
			Expect(command.Args(command, []string{"SELECT", "a"})).To(HaveOccurred(), name)
		}
	})

	It("prefixes SQL command errors with the requested dialect", func() {
		commands := []struct {
			name string
			run  func(InputOptions) error
		}{
			{"parse", func(opts InputOptions) error {
				_, err := parseSQL(opts, strings.NewReader(""))
				return err
			}},
			{"validate", func(opts InputOptions) error {
				_, err := validateSQL(opts, strings.NewReader(""))
				return err
			}},
			{"format", func(opts InputOptions) error {
				_, err := formatSQL(opts, strings.NewReader(""))
				return err
			}},
			{"tokens", func(opts InputOptions) error {
				_, err := tokenizeSQL(opts, strings.NewReader(""))
				return err
			}},
		}
		for _, command := range commands {
			err := command.run(InputOptions{SQL: query, Dialect: "unknown_dialect"})
			Expect(err).To(MatchError(HavePrefix("unknown_dialect: ")), command.name)
		}
		_, err := validateSQL(InputOptions{SQL: "DROP TABLE t"}, strings.NewReader(""))
		Expect(err).To(MatchError(HavePrefix("neutral: expected a single query")))
		_, err = formatSQL(InputOptions{File: filepath.Join(GinkgoT().TempDir(), "missing.sql"), Dialect: "tsql"}, strings.NewReader(""))
		Expect(err).To(MatchError(HavePrefix("tsql: read SQL file")))
		Expect(errors.Is(err, os.ErrNotExist)).To(BeTrue())
	})

	It("shows every unsupported statement with its source location", func() {
		path := filepath.Join(GinkgoT().TempDir(), "script.sql")
		sql := "PRINT\r\nGO\r\nSELECT 1;\r\nPRINT\r\nGO"
		Expect(os.WriteFile(path, []byte(sql), 0600)).To(Succeed())
		_, err := parseSQL(InputOptions{File: path, Dialect: "tsql"}, strings.NewReader(""))
		var failure *ParseFailure
		Expect(errors.As(err, &failure)).To(BeTrue())
		Expect(failure.Diagnostics).To(HaveLen(2))
		Expect(failure.Diagnostics[0].Line).To(Equal(1))
		Expect(failure.Diagnostics[1].Line).To(Equal(4))
		output := failure.Pretty().ANSI()
		Expect(output).To(ContainSubstring("tsql: " + path + ":1:"))
		Expect(output).To(ContainSubstring("tsql: " + path + ":4:"))
		Expect(failure.Diagnostics[0].Excerpt).To(ContainSubstring("PRINT"))
		Expect(failure.Diagnostics[0].Excerpt).To(ContainSubstring("^"))
	})

	It("aligns the caret after tabs and wide runes and clips long lines", func() {
		clipped, caret := clipSourceLine([]rune("a\t界!"), 3)
		Expect(clipped).To(Equal("a   界!"))
		Expect(caret).To(Equal(6))
		clipped, caret = clipSourceLine([]rune(strings.Repeat("a", 200)+"Z"), 200)
		Expect(clipped).To(HavePrefix("…"))
		Expect(clipped).To(HaveSuffix("Z"))
		Expect(len([]rune(clipped))).To(BeNumerically("<=", 162))
		Expect(caret).To(BeNumerically("<", len([]rune(clipped))))
	})
})
