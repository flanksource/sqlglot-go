package access_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/sqlglot-go/sqlglot/access"
)

var _ = Describe("ReadTSQL", func() {
	const (
		customerExists = "EXISTS (SELECT 1 FROM dbo.Customers WHERE CustomerId = @CustomerId)"
		linesToReserve = "EXISTS (SELECT 1 FROM dbo.OrderLines WHERE OrderId = @OrderId AND Reserved = 0)"
	)
	call, read, write := access.Call, access.Read, access.Write

	It("reads every routine of the fixture with its calls, reads and writes, guarded by IF and WHILE", func() {
		file := readFixture("tsql_orders.sql")

		Expect(file.Diagnostics).To(BeEmpty())
		Expect(file.Script).To(BeNil())
		Expect(routineViews(file)).To(Equal([]routineView{
			{Name: "dbo.SubmitOrder", Line: 2, Params: []string{"@OrderId", "@CustomerId"}, Edges: []edge{
				{Kind: read, To: "dbo.Customers", Line: 8, Guards: []string{}},
				{Kind: call, To: "dbo.ValidateOrder", Line: 10, Guards: []string{customerExists}},
				{Kind: write, To: "dbo.Orders", Line: 11, Guards: []string{customerExists}},
				{Kind: call, To: "dbo.RejectOrder", Line: 15, Guards: []string{"!" + customerExists}},
				{Kind: read, To: "dbo.OrderLines", Line: 16, Guards: []string{}},
				{Kind: write, To: "dbo.OrderLines", Line: 17, Guards: []string{linesToReserve}},
			}},
			{Name: "dbo.ValidateOrder", Line: 21, Params: []string{"@OrderId"}, Edges: []edge{
				{Kind: read, To: "dbo.Orders", Line: 23, Guards: []string{}},
				{Kind: read, To: "dbo.Customers", Line: 23, Guards: []string{}},
				{Kind: call, To: "dbo.OrderTotal", Line: 23, Guards: []string{}},
			}},
			{Name: "dbo.RejectOrder", Line: 29, Params: []string{"@OrderId"}, Edges: []edge{
				{Kind: write, To: "dbo.RejectedOrders", Line: 31, Guards: []string{}},
				{Kind: read, To: "dbo.Orders", Line: 31, Guards: []string{}},
			}},
			{Name: "dbo.OrderTotal", Line: 35, Params: []string{"@OrderId"}, Edges: []edge{
				{Kind: read, To: "dbo.OrderLines", Line: 38, Guards: []string{}},
			}},
			{Name: "dbo.Ping", Line: 42, Params: []string{}, Edges: []edge{}},
		}))
	})

	It("gives each statement its start line and its text", func() {
		submit := readFixture("tsql_orders.sql").Routines[0]

		insert := submit.Body[0].Then[1]
		Expect([]any{insert.Kind, insert.Line, insert.Text}).To(Equal([]any{access.StatementNode, 11,
			"INSERT INTO dbo.Orders (OrderId, CustomerId, Status)\n        VALUES (@OrderId, @CustomerId, 'submitted')"}))
	})

	It("reads an anonymous T-SQL block as the file's script with guarded table access", func() {
		file := readTSQL(`BEGIN
DECLARE @RecordCount INT
SELECT @RecordCount = COUNT(*) FROM dbo.Code
IF @RecordCount = 0
BEGIN
INSERT INTO dbo.Code (Value) VALUES ('new')
END
ELSE
BEGIN
PRINT 'already exists'
END
END`)

		Expect(file.Diagnostics).To(BeEmpty())
		Expect(file.Routines).To(BeEmpty())
		Expect(file.Script.Line).To(Equal(1))
		Expect(edges(file.Script.Body)).To(Equal([]edge{
			{Kind: read, To: "dbo.Code", Line: 3, Guards: []string{}},
			{Kind: write, To: "dbo.Code", Line: 6, Guards: []string{"@RecordCount = 0"}},
		}))
	})

	It("reports an unsupported anonymous statement and continues with the next one", func() {
		file := readTSQL("BULK INSERT dbo.Code FROM 'code.csv';\nSELECT * FROM dbo.Code")

		Expect(targets(file.Script.Body)).To(Equal([]string{"read dbo.Code"}))
		Expect(file.Diagnostics).To(HaveLen(1))
		Expect([]any{file.Diagnostics[0].Line, file.Diagnostics[0].Statement}).To(Equal([]any{1, "BULK INSERT dbo.Code FROM 'code.csv'"}))
		Expect(file.Diagnostics[0].Message).To(ContainSubstring("unsupported"))
	})

	It("has no script for setup batches beside a routine", func() {
		routine, diagnostics := readOne("SET ANSI_NULLS ON\nGO\nCREATE PROCEDURE dbo.Load AS SELECT * FROM dbo.Code")

		Expect(diagnostics).To(BeEmpty())
		Expect(routine.Object).To(Equal(access.Object{Schema: "dbo", Name: "Load"}))
		Expect(targets(routine.Body)).To(Equal([]string{"read dbo.Code"}))
	})

	It("keeps data access outside a routine as the file's script", func() {
		file := readTSQL("INSERT INTO dbo.Code (Value) VALUES ('new')\nGO\nCREATE PROCEDURE dbo.Load AS SELECT * FROM dbo.Code")

		Expect(file.Diagnostics).To(BeEmpty())
		Expect(file.Routines).To(HaveLen(1))
		Expect(targets(file.Routines[0].Body)).To(Equal([]string{"read dbo.Code"}))
		Expect(targets(file.Script.Body)).To(Equal([]string{"write dbo.Code"}))
	})

	DescribeTable("reads only real objects, as their schema-qualified names",
		func(body string, expected []string) {
			routine, diagnostics := readOne("CREATE PROCEDURE dbo.P @Id int AS\n" + body)
			Expect(diagnostics).To(BeEmpty())
			Expect(targets(routine.Body)).To(Equal(expected))
		},
		Entry("an unqualified table is in dbo", "SELECT * FROM Orders", []string{"read dbo.Orders"}),
		Entry("a CTE is no table", "WITH o AS (SELECT * FROM sales.Orders) SELECT * FROM o", []string{"read sales.Orders"}),
		Entry("temp tables and table variables are local",
			"DECLARE @t TABLE (id int)\nINSERT INTO #tmp SELECT id FROM @t\nSELECT * FROM #tmp", []string{}),
		Entry("UPDATE by alias writes the FROM table it names",
			"UPDATE o SET Status = 1 FROM dbo.Orders o JOIN dbo.Customers c ON c.Id = o.CustomerId",
			[]string{"write dbo.Orders", "read dbo.Customers"}),
		Entry("MERGE writes its target and reads its source",
			"MERGE INTO dbo.Orders AS t USING staging.Orders AS s ON t.Id = s.Id WHEN MATCHED THEN UPDATE SET t.Status = s.Status;",
			[]string{"write dbo.Orders", "read staging.Orders"}),
		Entry("DELETE reads the tables its subquery names",
			"DELETE FROM dbo.Orders WHERE CustomerId IN (SELECT Id FROM dbo.Customers)",
			[]string{"write dbo.Orders", "read dbo.Customers"}),
		Entry("a table-valued function in FROM is a call", "SELECT * FROM dbo.OpenOrders(@Id)", []string{"call dbo.OpenOrders"}),
		Entry("an xml method is no call", "SELECT Data.value('(/a)[1]', 'int') FROM dbo.Docs", []string{"read dbo.Docs"}),
		Entry("a bare builtin is no call", "SELECT ISNUMERIC(Code) FROM dbo.Codes", []string{"read dbo.Codes"}),
		Entry("EXEC of a three-part name keeps its database", "EXEC Archive.dbo.Purge @Id", []string{"call Archive.dbo.Purge"}),
		Entry("a cross-database table keeps its database", "SELECT * FROM Archive.dbo.Orders", []string{"read Archive.dbo.Orders"}),
		Entry("CREATE TABLE writes the table and reads what its foreign keys reference, a temp table nothing",
			"CREATE TABLE dbo.Codes (Id int REFERENCES dbo.Kinds (Id))\nCREATE TABLE #t (Id int)", []string{"write dbo.Codes", "read dbo.Kinds"}),
		Entry("CREATE INDEX writes the table it indexes", "CREATE INDEX IX_Codes_Kind ON dbo.Codes (Kind)", []string{"write dbo.Codes"}),
		Entry("ALTER, TRUNCATE and DROP write the table they name",
			"ALTER TABLE Codes ADD Kind int\nTRUNCATE TABLE dbo.Stage\nDROP TABLE IF EXISTS dbo.Old",
			[]string{"write dbo.Codes", "write dbo.Stage", "write dbo.Old"}),
		Entry("EXEC of a string literal runs the statements it holds",
			"EXEC('UPDATE dbo.Codes SET Kind = ''a'' WHERE Id IN (SELECT Id FROM dbo.Kinds)')",
			[]string{"write dbo.Codes", "read dbo.Kinds"}),
		Entry("sp_executesql of a string literal runs the statements it holds",
			"EXEC sp_executesql N'DELETE FROM dbo.Stage'", []string{"write dbo.Stage"}),
	)

	DescribeTable("reports dynamic SQL it cannot read",
		func(body string) {
			routine, diagnostics := readOne("CREATE PROCEDURE dbo.P @sql nvarchar(max) AS\n" + body)
			Expect(targets(routine.Body)).To(BeEmpty())
			Expect(diagnostics).To(Equal([]access.Diagnostic{{Line: 2, Statement: body, Message: "unsupported SQL statement: dynamic SQL"}}))
			Expect(routine.Diagnostics).To(Equal(diagnostics))
		},
		Entry("EXEC of a variable", "EXEC (@sql)"),
		Entry("sp_executesql of a variable", "EXEC sp_executesql @sql"),
	)

	It("reads a routine an EXEC literal defines as a routine of the file", func() {
		file := readTSQL("EXEC('CREATE OR ALTER FUNCTION dbo.Country() RETURNS varchar(2) AS\nBEGIN\n  RETURN (SELECT TOP 1 Code FROM dbo.AsEnv WHERE EnvKey = ''current'')\nEND')")

		Expect(file.Diagnostics).To(BeEmpty())
		Expect(routineViews(file)).To(Equal([]routineView{
			{Name: "dbo.Country", Line: 1, Params: []string{}, Edges: []edge{{Kind: read, To: "dbo.AsEnv", Line: 3, Guards: []string{}}}},
		}))
		Expect(*file.Script).To(Equal(access.Script{Line: 1}))
	})

	It("writes a view it creates and does not read what the view's query reads", func() {
		file := readTSQL("CREATE VIEW dbo.OpenCodes AS SELECT * FROM dbo.Codes\nGO\nALTER VIEW dbo.OpenCodes AS SELECT * FROM dbo.Codes")

		Expect(file.Diagnostics).To(BeEmpty())
		Expect(targets(file.Script.Body)).To(Equal([]string{"write dbo.OpenCodes", "write dbo.OpenCodes"}))
	})

	It("reads non-breaking spaces as the whitespace SQL Server takes them for, keeping lines", func() {
		nbsp := "  "
		file := readTSQL("IF @n = 0\n" + nbsp + "BEGIN\n" + nbsp + "INSERT INTO dbo.Code VALUES ('a b')\n" + nbsp + "END\nELSE\n" + nbsp + "PRINT 'present'")

		Expect(file.Diagnostics).To(BeEmpty())
		Expect(edges(file.Script.Body)).To(Equal([]edge{{Kind: write, To: "dbo.Code", Line: 3, Guards: []string{"@n = 0"}}}))
	})

	It("records a statement that does not parse as a diagnostic and reads the others", func() {
		routine, diagnostics := readOne(`CREATE PROCEDURE dbo.Load AS
BEGIN
    BULK INSERT dbo.Orders FROM 'orders.csv';
    EXEC dbo.Notify;
END`)

		Expect(targets(routine.Body)).To(Equal([]string{"call dbo.Notify"}))
		Expect(diagnostics).To(HaveLen(1))
		Expect([]any{diagnostics[0].Line, diagnostics[0].Statement}).To(Equal([]any{3, "BULK INSERT dbo.Orders FROM 'orders.csv'"}))
		Expect(diagnostics[0].Message).NotTo(BeEmpty())
	})

	It("guards an ELSE IF chain and a TRY block like the statements they hold", func() {
		routine, diagnostics := readOne(`CREATE PROCEDURE dbo.Route @Kind int AS
IF @Kind = 1
    EXEC dbo.One;
ELSE IF @Kind = 2
BEGIN TRY
    EXEC dbo.Two;
END TRY
BEGIN CATCH
    EXEC dbo.Failed;
END CATCH
EXEC dbo.Always;`)

		Expect(diagnostics).To(BeEmpty())
		Expect(edges(routine.Body)).To(Equal([]edge{
			{Kind: call, To: "dbo.One", Line: 3, Guards: []string{"@Kind = 1"}},
			{Kind: call, To: "dbo.Two", Line: 6, Guards: []string{"!@Kind = 1", "@Kind = 2"}},
			{Kind: call, To: "dbo.Failed", Line: 9, Guards: []string{"!@Kind = 1", "@Kind = 2"}},
			{Kind: call, To: "dbo.Always", Line: 11, Guards: []string{}},
		}))
	})

	It("marks an ELSE and a CATCH that access nothing as written, and leaves absent ones unmarked", func() {
		routine, diagnostics := readOne(`CREATE PROCEDURE dbo.Quiet @Kind int AS
IF @Kind = 1
    EXEC dbo.One;
ELSE
    PRINT 'skipped';
BEGIN TRY
    EXEC dbo.Two;
END TRY
BEGIN CATCH
    PRINT 'failed';
END CATCH
IF @Kind = 3
    EXEC dbo.Three;`)

		Expect(diagnostics).To(BeEmpty())
		Expect(routine.Body).To(HaveLen(3))
		marks := func(n access.Node) []any { return []any{n.Kind, n.HasElse, len(n.Else), n.HasCatch, len(n.Catch)} }
		Expect([][]any{marks(routine.Body[0]), marks(routine.Body[1]), marks(routine.Body[2])}).To(Equal([][]any{
			{access.IfNode, true, 0, false, 0},
			{access.TryNode, false, 0, true, 0},
			{access.IfNode, false, 0, false, 0},
		}))
	})

	It("reads a comment-only file as nothing", func() {
		Expect(readTSQL("-- nothing to deploy\n/* yet */")).To(Equal(access.File{}))
	})
})

var _ = Describe("ReadStatement", func() {
	DescribeTable("reads one statement in the dialect it is written in",
		func(text string, opts access.StatementOptions, expected []access.Access) {
			accesses, err := access.ReadStatement(text, opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(accesses).To(Equal(expected))
		},
		Entry("a bare PostgreSQL function is a call",
			"SELECT refresh(id) FROM orders", access.StatementOptions{Dialect: "postgres", DefaultSchema: "public", Line: 4},
			[]access.Access{{Kind: access.Read, Object: access.Object{Schema: "public", Name: "orders"}, Line: 4},
				{Kind: access.Call, Object: access.Object{Schema: "public", Name: "refresh"}, Line: 4}}),
		Entry("SELECT … INTO variables writes no table",
			"SELECT total INTO v_total FROM orders", access.StatementOptions{Dialect: "postgres", DefaultSchema: "public", IntoVariables: true, Line: 1},
			[]access.Access{{Kind: access.Read, Object: access.Object{Schema: "public", Name: "orders"}, Line: 1}}),
	)

	It("refuses options that name no dialect or default schema", func() {
		_, err := access.ReadStatement("SELECT 1", access.StatementOptions{Dialect: "tsql"})
		Expect(err).To(MatchError(ContainSubstring("default schema")))
	})

	It("reports dynamic SQL as unsupported, as a single statement follows no EXEC", func() {
		_, err := access.ReadStatement("EXEC('DELETE FROM dbo.X')", access.StatementOptions{Dialect: "tsql", DefaultSchema: "dbo"})
		var unsupported *access.UnsupportedSQLError
		Expect(err).To(BeAssignableToTypeOf(unsupported))
		Expect(err).To(MatchError("unsupported SQL statement: dynamic SQL"))
	})
})
