package csvdata

import (
	"github.com/DATA-DOG/go-sqlmock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL Server bulk staging", func() {
	It("streams one sequence with a stable record ID and preserves omitted fields", func(ctx SpecContext) {
		db, mock, err := sqlmock.New()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			Expect(db.Close()).To(Succeed())
			Expect(mock.ExpectationsWereMet()).To(Succeed())
		})
		mock.ExpectBegin()
		mock.ExpectExec("SELECT TOP\\(0\\)").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(`CREATE TABLE \[#sqlglot_csv_raw\]`).WillReturnResult(sqlmock.NewResult(0, 0))
		stream := mock.ExpectPrepare("^INSERTBULK ").WillBeClosed()
		stream.ExpectExec().WithArgs(1, "7", "").WillReturnResult(sqlmock.NewResult(0, 0))
		stream.ExpectExec().WithArgs(2, "8", nil).WillReturnResult(sqlmock.NewResult(0, 0))
		stream.ExpectExec().WithoutArgs().WillReturnResult(sqlmock.NewResult(0, 2))
		mock.ExpectExec(`INSERT INTO \[#sqlglot_csv\].*SELECT.*FROM \[#sqlglot_csv_raw\]`).WillReturnResult(sqlmock.NewResult(0, 2))
		mock.ExpectExec(`DROP TABLE \[#sqlglot_csv_raw\]`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectRollback()
		mock.ExpectClose()

		tx, err := db.BeginTx(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		work := tableWork{
			meta:   metadata{Name: "dbo.Example", Columns: []column{{Name: "ID", Type: "int"}, {Name: "Label", Type: "nvarchar"}}},
			fields: []string{"ID", "Label"},
			rows: []inputRow{
				{source: "Example.csv record 2", values: map[string]any{"ID": "7", "Label": ""}},
				{source: "Example.csv record 3", values: map[string]any{"ID": "8"}},
			},
		}
		err = work.stage(ctx, tx)
		Expect(tx.Rollback()).To(Succeed())
		Expect(err).NotTo(HaveOccurred())
	})
})
