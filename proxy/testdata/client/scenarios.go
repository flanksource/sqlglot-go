package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-sql/civil"
	"github.com/govalues/decimal"
	mssql "github.com/microsoft/go-mssqldb"
)

// scenario is one connection's workload; its name becomes the fixture file name.
type scenario struct {
	name       string
	packetSize int
	clientTLS  bool
	run        func(context.Context, *sql.DB) error
}

var scenarios = []scenario{
	{name: "login_default", packetSize: 4096, run: ping},
	{name: "login_512", packetSize: 512, run: ping},
	{name: "login_32767", packetSize: 32767, run: ping},
	{name: "login_tls", packetSize: 4096, clientTLS: true, run: ping},
	{name: "batch_literals", packetSize: 4096, run: batchLiterals},
	{name: "rpc_executesql", packetSize: 4096, run: rpcExecutesql},
	{name: "rpc_types", packetSize: 4096, run: rpcTypes},
	{name: "result_types", packetSize: 4096, run: resultTypes},
	{name: "prepared", packetSize: 4096, run: prepared},
	{name: "errors_info", packetSize: 4096, run: errorsInfo},
	{name: "transactions", packetSize: 4096, run: transactions},
	{name: "attention", packetSize: 4096, run: attention},
	{name: "temp_set", packetSize: 4096, run: tempSet},
	{name: "result_storage_types", packetSize: 4096, run: resultStorageTypes},
}

func ping(ctx context.Context, db *sql.DB) error { return db.PingContext(ctx) }

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func drain(ctx context.Context, db querier, statement string, args ...any) error {
	rows, err := db.QueryContext(ctx, statement, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for {
		for rows.Next() {
			continue
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if !rows.NextResultSet() {
			return rows.Err()
		}
	}
}

func drainAll(ctx context.Context, db querier, statements ...string) error {
	for _, statement := range statements {
		if err := drain(ctx, db, statement); err != nil {
			return fmt.Errorf("%s: %w", statement, err)
		}
	}
	return nil
}

func batchLiterals(ctx context.Context, db *sql.DB) error {
	return drainAll(ctx, db,
		"select * from tbl where a = 'a' and b = 'b'",
		"select * from tbl where c = 1 and c > 0",
		"select N'שלום', NULL, CAST('2025-01-02' AS date)",
		"select c from tbl where c = 1; select c from tbl where c = 2",
	)
}

func rpcExecutesql(ctx context.Context, db *sql.DB) error {
	return drain(ctx, db, "select * from tbl where a = @p1 and b = @p2", "a", "b")
}

func rpcTypes(ctx context.Context, db *sql.DB) error {
	at := time.Date(2025, 1, 2, 3, 4, 5, 123456700, time.FixedZone("", 2*3600))
	day, clock := civil.Date{Year: 2025, Month: 1, Day: 2}, civil.Time{Hour: 3, Minute: 4, Second: 5, Nanosecond: 123456700}
	guid := mssql.UniqueIdentifier{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	// The fixture test asserts these values by position; append rather than reorder.
	values := []any{
		int32(123), int64(1234567890123), int16(12), uint8(7), true,
		float64(1.25), float32(2.5), decimal.MustParse("-123.45"),
		mssql.Money[decimal.Decimal]{Decimal: decimal.MustParse("-123.45")},
		day, clock, civil.DateTime{Date: day, Time: clock}, mssql.DateTime1(at), mssql.DateTimeOffset(at),
		guid, "example", mssql.NVarCharMax(strings.Repeat("x", 5000)), mssql.VarChar("example"), []byte(strings.Repeat("a", 5000)),
		sql.NullInt16{}, sql.NullInt32{}, sql.NullInt64{}, sql.NullByte{}, sql.NullBool{}, sql.NullFloat64{}, sql.NullString{},
		sql.NullTime{}, (*mssql.UniqueIdentifier)(nil), []byte(nil), decimal.NullDecimal{}, mssql.Money[decimal.NullDecimal]{},
		mssql.NullDate{}, mssql.NullTime{}, mssql.NullDateTime{},
	}
	for i, value := range values {
		if err := drain(ctx, db, "select @p1", value); err != nil {
			return fmt.Errorf("RPC value %d (%T): %w", i, value, err)
		}
	}
	return nil
}

func resultTypes(ctx context.Context, db *sql.DB) error {
	return drainAll(ctx, db,
		"SELECT CAST(1 AS int) AS i, CAST(2 AS bigint) AS bi, CAST(3 AS smallint) AS si, CAST(4 AS tinyint) AS ti, CAST(1 AS bit) AS flag, CAST(1.25 AS float) AS f, CAST(2.5 AS real) AS r, CAST(12.34 AS decimal(10,2)) AS d, CAST(56.78 AS numeric(10,2)) AS n, CAST(9.25 AS money) AS m",
		"SELECT CAST('2025-01-02' AS date) AS d, CAST('03:04:05' AS time) AS t, CAST('2025-01-02T03:04:05' AS datetime) AS dt, CAST('2025-01-02T03:04:05' AS datetime2) AS dt2, CAST('2025-01-02T03:04:05+02:00' AS datetimeoffset) AS dto, CAST('00000000-0000-0000-0000-000000000001' AS uniqueidentifier) AS uid",
		"SELECT CAST(N'example' AS nvarchar(10)) AS nv, CAST(REPLICATE(N'x',5000) AS nvarchar(max)) AS nvmax, CAST('example' AS varchar(10)) AS v, CAST(REPLICATE('a',5000) AS varbinary(max)) AS vb",
		"SELECT CAST(NULL AS int) AS i, CAST(NULL AS nvarchar(10)) AS n; SELECT 1 AS i WHERE 1=0; SELECT 2 AS i",
		"SELECT CAST(REPLICATE(CAST(N'x' AS nvarchar(max)),60000) AS nvarchar(max)) AS large_value",
	)
}

// resultStorageTypes covers every other way SQL Server delimits a value: NOT NULL
// fixed-size columns, padded strings, text pointers, variants, XML and CLR types.
func resultStorageTypes(ctx context.Context, db *sql.DB) error {
	return drainAll(ctx, db,
		"CREATE TABLE storage_types (id int NOT NULL, ti tinyint NOT NULL, flag bit NOT NULL, si smallint NOT NULL, bi bigint NOT NULL, r real NOT NULL, f float NOT NULL, m money NOT NULL, sm smallmoney NOT NULL, dt datetime NOT NULL, sdt smalldatetime NOT NULL, c char(4) NULL, nc nchar(4) NULL, bin binary(4) NULL, t text NULL, nt ntext NULL, img image NULL, x xml NULL, v sql_variant NULL, h hierarchyid NULL)",
		"INSERT INTO storage_types VALUES (1, 2, 1, 3, 4, 2.5, 1.25, 9.25, 8.5, '2025-01-02T03:04:05', '2025-01-02T03:04:00', 'ab', N'cd', 0x0102, 'text', N'ntext', 0x0304, '<a>1</a>', CAST(42 AS int), hierarchyid::Parse('/1/')), (2, 0, 0, 0, 0, 0, 0, 0, 0, '2025-01-02', '2025-01-02', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL)",
		"SELECT * FROM storage_types ORDER BY id",
	)
}

func prepared(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	var handle int32
	if _, err := conn.ExecContext(ctx, "sp_prepare", sql.Named("handle", sql.Out{Dest: &handle}), sql.Named("params", "@p1 int"), sql.Named("stmt", "select @p1")); err != nil {
		return err
	}
	if err := drain(ctx, conn, "sp_execute", handle, 7); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "sp_unprepare", handle); err != nil {
		return err
	}
	handle = 0
	return drain(ctx, conn, "sp_prepexec", sql.Named("handle", sql.Out{Dest: &handle}), sql.Named("params", "@p1 int"), sql.Named("stmt", "select @p1"), sql.Named("p1", 8))
}

func errorsInfo(ctx context.Context, db *sql.DB) error {
	for _, statement := range []string{"SELECT FROM tbl", "CREATE TABLE capture_unique (id int CONSTRAINT pk_capture_unique PRIMARY KEY); INSERT INTO capture_unique VALUES (1), (1)"} {
		if err := drain(ctx, db, statement); err == nil {
			return fmt.Errorf("expected a SQL error for %q", statement)
		}
	}
	if err := drain(ctx, db, "PRINT N'example info'; RAISERROR(N'example nowait', 10, 1) WITH NOWAIT; SELECT 1"); err != nil {
		return err
	}
	if err := drain(ctx, db, "SELECT CONVERT(int, @p1)", "invalid"); err == nil {
		return errors.New("expected an RPC conversion error")
	}
	return nil
}

func transactions(ctx context.Context, db *sql.DB) error {
	for _, finish := range []func(*sql.Tx) error{(*sql.Tx).Commit, (*sql.Tx).Rollback} {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE tbl SET c=c WHERE c=1"); err != nil {
			return errors.Join(err, tx.Rollback())
		}
		if err := finish(tx); err != nil {
			return err
		}
	}
	return nil
}

func attention(ctx context.Context, db *sql.DB) error {
	cancelled, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if err := drain(cancelled, db, "WAITFOR DELAY '00:00:05'; SELECT 1"); !errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("expected cancellation: %w", err)
	}
	return nil
}

func tempSet(ctx context.Context, db *sql.DB) error {
	return drain(ctx, db, "SET NOCOUNT ON; CREATE TABLE #capture_tmp (id int); INSERT INTO #capture_tmp VALUES (1); SELECT id FROM #capture_tmp; DROP TABLE #capture_tmp; SET NOCOUNT OFF")
}
