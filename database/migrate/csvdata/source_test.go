package csvdata

import (
	"testing"
	"testing/fstest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCSVData(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "CSV migration sources")
}

var _ = Describe("CSV sources", func() {
	It("routes table-named files and filters independent tags", func() {
		files := fstest.MapFS{
			"AS.csv.yaml": {Data: []byte(`version: 1
defaults:
  tablePattern: '^(.*/)?(?P<table>AsCode)(_[A-Z]{2})?\.csv$'
layers:
  country: {values: [BW, KE], match: ['**/*_${value}.csv'], valuePattern: '[A-Z]{2}'}
  env: {values: [dev, qa], match: ['overrides/${value}/**/*.csv']}
`)},
			"AsCode.csv":                  {Data: []byte("ID,Name\n1,Shared\n")},
			"AsCode_BW.csv":               {Data: []byte("ID,Name\n1,Botswana\n")},
			"AsCode_KE.csv":               {Data: []byte("ID,Name\n1,Kenya\n")},
			"overrides/dev/AsCode_BW.csv": {Data: []byte("ID,Name\n1,Development\n")},
			"overrides/qa/AsCode_BW.csv":  {Data: []byte("ID,Name\n1,QA\n")},
		}
		bundle, err := Load(files, "dbo", map[string]string{"country": "none,BW", "env": "!dev"})
		Expect(err).NotTo(HaveOccurred())
		Expect(bundle.Files).To(HaveLen(3))
		Expect(bundle.Files[2].Tags).To(Equal(map[string]string{"country": "BW", "env": "qa"}))
		Expect(bundle.Files[2].Table).To(Equal("dbo.AsCode"))
	})

	It("distinguishes NULL, quoted tokens, quoted empties and unquoted blanks", func() {
		records, err := readRecords([]byte("ID,Blank,Empty,Null,Literal,Multiline\r\n001,,\"\",\\N,\"\\N\",\"one\r\ntwo\"\r\n"), ',')
		Expect(err).NotTo(HaveOccurred())
		settings := ValueSettings{Blank: "sql-null", NullTokens: []string{`\N`}}
		values := make([]any, 0, len(records[1]))
		for _, cell := range records[1] {
			value, err := settings.decode(cell)
			Expect(err).NotTo(HaveOccurred())
			values = append(values, value)
		}
		Expect(values).To(Equal([]any{"001", nil, "", nil, `\N`, "one\ntwo"}))
	})

	It("requires explicitly typed empty values for numeric, date and identifier columns", func() {
		for _, typ := range []string{"int", "date", "uniqueidentifier"} {
			_, err := (column{Type: typ}).bind("")
			Expect(err).To(MatchError(ContainSubstring("empty string is incompatible")))
		}
		Expect((column{Type: "nvarchar"}).bind("")).To(Equal(""))
	})

	It("does not interpret sibling variants as an overwrite order", func() {
		work := tableWork{rows: []inputRow{
			{source: "base.csv", tags: map[string]string{"country": "none", "env": "none"}},
			{source: "bw.csv", tags: map[string]string{"country": "BW", "env": "none"}},
			{source: "qa.csv", tags: map[string]string{"country": "none", "env": "qa"}},
			{source: "bw-qa.csv", tags: map[string]string{"country": "BW", "env": "qa"}},
		}}
		rows := []rowState{{record: 1}, {record: 2}, {record: 3}, {record: 4}}
		_, err := work.winner(rows[:3])
		Expect(err).To(MatchError(ContainSubstring("conflicting CSV layers in bw.csv and qa.csv")))
		selected, err := work.winner(rows)
		Expect(err).NotTo(HaveOccurred())
		Expect(selected.record).To(Equal(4))
	})
})
