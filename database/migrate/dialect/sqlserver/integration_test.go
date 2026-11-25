//go:build integration

package sqlserver

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/flanksource/sqlglot-go/database/inspect/snapshot"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// One disposable server at compatibility level 100 serves every spec; each
// spec works in a schema of its own.
var _ = Describe("the SQL Server dialect against a live database", Label("integration"), func() {
	Describe("capabilities", func() {
		It("reports transactional DDL and no TRY_CONVERT at level 100", func(ctx SpecContext) {
			caps, err := Dialect{}.Capabilities(ctx, db)
			Expect(err).NotTo(HaveOccurred())
			Expect(caps.Fingerprint).NotTo(BeEmpty())
			Expect(caps.TransactionalDDL).To(BeTrue())
			Expect(caps.Features[planner.FeatureTryConvert]).To(BeFalse())
		})
	})

	Describe("retyping GUID text to uniqueidentifier under its dependents", Ordered, func() {
		accounts := migrate.Name{Schema: "cap", Name: "accounts"}
		orders := migrate.Name{Schema: "cap", Name: "orders"}
		var live migrate.Schema
		var desired migrate.Schema
		var artifact engine.Artifact

		setup := func(ctx SpecContext) {
			mustExec(ctx, db,
				"CREATE SCHEMA cap",
				"CREATE TABLE cap.accounts (id varchar(36) NOT NULL PRIMARY KEY CLUSTERED, region varchar(20) NULL, note nvarchar(50) NULL)",
				"ALTER TABLE cap.accounts ADD CONSTRAINT DF_accounts_id DEFAULT (CONVERT(varchar(36), NEWID())) FOR id",
				"CREATE UNIQUE NONCLUSTERED INDEX UX_accounts_id_ff ON cap.accounts (id) WITH (PAD_INDEX = ON, FILLFACTOR = 70, IGNORE_DUP_KEY = ON, STATISTICS_NORECOMPUTE = ON, ALLOW_PAGE_LOCKS = OFF, DATA_COMPRESSION = PAGE)",
				"CREATE NONCLUSTERED INDEX IX_accounts_region ON cap.accounts (region) INCLUDE (id) WHERE region IS NOT NULL",
				"ALTER TABLE cap.accounts ADD CONSTRAINT CK_accounts_id CHECK (id <> '00000000-0000-0000-0000-000000000000')",
				"CREATE STATISTICS ST_accounts_id ON cap.accounts (id, region)",
				"CREATE TABLE cap.orders (order_id int IDENTITY(1,1) PRIMARY KEY, account_id varchar(36) NOT NULL)",
				"ALTER TABLE cap.orders ADD CONSTRAINT FK_orders_accounts FOREIGN KEY (account_id) REFERENCES cap.accounts (id) ON DELETE CASCADE",
				"CREATE INDEX IX_orders_account ON cap.orders (account_id)",
				"INSERT INTO cap.accounts (id, region, note) VALUES ('0A1B2C3D-0000-4000-8000-00000000AAAA', 'east', 'one'), ('0a1b2c3d-0000-4000-8000-00000000bbbb', NULL, 'two')",
				"INSERT INTO cap.orders (account_id) VALUES ('0A1B2C3D-0000-4000-8000-00000000AAAA'), ('0a1b2c3d-0000-4000-8000-00000000bbbb')",
			)
			live = inspect(ctx, db, declared("cap", "accounts", "orders")).Schema
			desired = cloneSchema(live)
			for _, ref := range []struct {
				table  migrate.Name
				column string
			}{{accounts, "id"}, {orders, "account_id"}} {
				c := column(tableOf(desired, ref.table), ref.column)
				c.Type, c.Collation = "uniqueidentifier", ""
				if c.Default != nil {
					c.Default.Expression = "(newid())"
				}
			}
		}

		It("plans every dependent as a rebuild with its cause, and applies it keeping rows, options and trust", func(ctx SpecContext) {
			setup(ctx)
			var err error
			artifact, err = planLive(ctx, db, declared("cap", "accounts", "orders"), desired)
			Expect(err).NotTo(HaveOccurred())
			Expect(changeIDs(artifact, "rebuild")).To(HaveLen(7))
			for _, c := range artifact.Changes {
				if c.Action == "rebuild" {
					Expect(c.Cause).NotTo(BeEmpty(), c.ID)
				}
			}
			kinds := map[string]int{}
			for _, d := range artifact.Dependents {
				kinds[string(d.Kind)]++
			}
			Expect(kinds).To(Equal(map[string]int{"primary-key": 1, "index": 3, "foreign-key": 1, "check": 1, "statistic": 1}))

			result, err := applyArtifact(ctx, db, artifact)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Outcome).To(Equal(engine.Applied))

			Expect(scalar[string](ctx, db, "SELECT t.name FROM sys.columns c JOIN sys.types t ON t.user_type_id = c.user_type_id WHERE c.object_id = OBJECT_ID('cap.accounts') AND c.name = 'id'")).To(Equal("uniqueidentifier"))
			Expect(scalar[string](ctx, db, "SELECT CAST(MIN(id) AS varchar(36)) FROM cap.accounts")).To(Equal("0A1B2C3D-0000-4000-8000-00000000AAAA"))
			Expect(scalar[int](ctx, db, "SELECT COUNT(*) FROM cap.orders o JOIN cap.accounts a ON a.id = o.account_id")).To(Equal(2))
		})

		It("brings back every index option, the statistic and the constraint names, and the key stays trusted", func(ctx SpecContext) {
			idx := `SELECT i.fill_factor, i.is_padded, i.ignore_dup_key, i.allow_page_locks, st.no_recompute,
				(SELECT MAX(p.data_compression_desc) FROM sys.partitions p WHERE p.object_id = i.object_id AND p.index_id = i.index_id)
				FROM sys.indexes i JOIN sys.stats st ON st.object_id = i.object_id AND st.stats_id = i.index_id
				WHERE i.object_id = OBJECT_ID('cap.accounts') AND i.name = 'UX_accounts_id_ff'`
			var fill int
			var padded, ignore, pageLocks, noRecompute bool
			var compression string
			Expect(db.QueryRowContext(ctx, idx).Scan(&fill, &padded, &ignore, &pageLocks, &noRecompute, &compression)).To(Succeed())
			Expect([]any{fill, padded, ignore, pageLocks, noRecompute, compression}).To(Equal([]any{70, true, true, false, true, "PAGE"}))
			Expect(scalar[string](ctx, db, "SELECT filter_definition FROM sys.indexes WHERE object_id = OBJECT_ID('cap.accounts') AND name = 'IX_accounts_region'")).To(ContainSubstring("[region] IS NOT NULL"))
			Expect(scalar[int](ctx, db, "SELECT COUNT(*) FROM sys.stats WHERE object_id = OBJECT_ID('cap.accounts') AND name = 'ST_accounts_id' AND user_created = 1")).To(Equal(1))
			Expect(scalar[int](ctx, db, "SELECT COUNT(*) FROM sys.check_constraints WHERE parent_object_id = OBJECT_ID('cap.accounts') AND name = 'CK_accounts_id' AND is_not_trusted = 0")).To(Equal(1))
			Expect(scalar[int](ctx, db, "SELECT COUNT(*) FROM sys.default_constraints WHERE parent_object_id = OBJECT_ID('cap.accounts') AND name = 'DF_accounts_id'")).To(Equal(1))
			Expect(scalar[int](ctx, db, "SELECT COUNT(*) FROM sys.key_constraints WHERE parent_object_id = OBJECT_ID('cap.accounts') AND type = 'PK'")).To(Equal(1))
			var untrusted, disabled bool
			var deleteAction string
			Expect(db.QueryRowContext(ctx, "SELECT is_not_trusted, is_disabled, delete_referential_action_desc FROM sys.foreign_keys WHERE name = 'FK_orders_accounts'").Scan(&untrusted, &disabled, &deleteAction)).To(Succeed())
			Expect([]any{untrusted, disabled, deleteAction}).To(Equal([]any{false, false, "CASCADE"}))
		})

		It("reads back exactly the dependents the plan recorded, and a second apply of the same artifact is already applied", func(ctx SpecContext) {
			columns := []engine.ColumnRef{{Table: accounts, Column: "id"}, {Table: orders, Column: "account_id"}}
			again, err := Dialect{}.Dependents(ctx, db, migrate.Schema{}, columns)
			Expect(err).NotTo(HaveOccurred())
			Expect(again).To(HaveLen(len(artifact.Dependents)))
			for _, want := range artifact.Dependents {
				i := indexOfDependent(again, want)
				Expect(i).To(BeNumerically(">=", 0), "%s %s", want.Kind, want.Name)
				same, err := Dialect{}.SameDependent(want, again[i])
				Expect(err).NotTo(HaveOccurred())
				Expect(same).To(BeTrue(), "%s %s", want.Kind, want.Name)
			}
			result, err := applyArtifact(ctx, db, artifact)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Outcome).To(Equal(engine.AlreadyApplied))
			states, err := engine.Check(ctx, db, artifact)
			Expect(err).NotTo(HaveOccurred())
			for id, state := range states {
				Expect(state).To(Equal(engine.UnitApplied), id)
			}
		})
	})

	Describe("negatives, each leaving the database unchanged", func() {
		retype := func(s migrate.Schema, table migrate.Name, columns ...string) migrate.Schema {
			out := cloneSchema(s)
			for _, name := range columns {
				c := column(tableOf(out, table), name)
				c.Type, c.Collation = "uniqueidentifier", ""
			}
			return out
		}
		columnType := func(ctx context.Context, table, name string) string {
			return scalar[string](ctx, db, "SELECT t.name FROM sys.columns c JOIN sys.types t ON t.user_type_id = c.user_type_id WHERE c.object_id = OBJECT_ID(@p1) AND c.name = @p2", table, name)
		}

		It("stops a value that is not a GUID before any DDL with a precondition error", func(ctx SpecContext) {
			mustExec(ctx, db, "CREATE SCHEMA neg1",
				"CREATE TABLE neg1.t (id varchar(36) NOT NULL PRIMARY KEY, label varchar(36) NULL)",
				"CREATE INDEX IX_t_label ON neg1.t (label)",
				"INSERT INTO neg1.t (id, label) VALUES ('0A1B2C3D-0000-4000-8000-00000000AAAA', 'not-a-guid')")
			name := migrate.Name{Schema: "neg1", Name: "t"}
			scope := declared("neg1", "t")
			artifact, err := planLive(ctx, db, scope, retype(inspect(ctx, db, scope).Schema, name, "label"))
			Expect(err).NotTo(HaveOccurred())
			_, err = applyArtifact(ctx, db, artifact)
			var failed *engine.PreconditionError
			Expect(errors.As(err, &failed)).To(BeTrue(), "%v", err)
			Expect(columnType(ctx, "neg1.t", "label")).To(Equal("varchar"))
			Expect(scalar[int](ctx, db, "SELECT COUNT(*) FROM sys.indexes WHERE object_id = OBJECT_ID('neg1.t') AND name = 'IX_t_label'")).To(Equal(1))
		})

		It("refuses an untrusted foreign key at plan time, naming it", func(ctx SpecContext) {
			mustExec(ctx, db, "CREATE SCHEMA neg2",
				"CREATE TABLE neg2.p (id varchar(36) NOT NULL PRIMARY KEY)",
				"CREATE TABLE neg2.c (id int NOT NULL PRIMARY KEY, p_id varchar(36) NOT NULL)",
				"INSERT INTO neg2.p VALUES ('0A1B2C3D-0000-4000-8000-00000000AAAA')",
				"INSERT INTO neg2.c VALUES (1, '0A1B2C3D-0000-4000-8000-00000000AAAA')",
				"ALTER TABLE neg2.c WITH NOCHECK ADD CONSTRAINT FK_c_p FOREIGN KEY (p_id) REFERENCES neg2.p (id)")
			scope := declared("neg2", "p", "c")
			current := inspect(ctx, db, scope)
			Expect(current.Refusals).To(ContainElement(engine.Refusal{Object: migrate.Name{Schema: "neg2", Name: "c"}, Member: "FK_c_p", Reason: "foreign key is not trusted"}))
			desired := retype(retype(current.Schema, migrate.Name{Schema: "neg2", Name: "p"}, "id"), migrate.Name{Schema: "neg2", Name: "c"}, "p_id")
			_, err := planLive(ctx, db, scope, desired)
			Expect(err).To(MatchError(And(ContainSubstring("FK_c_p"), ContainSubstring("not trusted"))))
			Expect(columnType(ctx, "neg2.p", "id")).To(Equal("varchar"))
		})

		It("refuses an inbound foreign key from a table outside the plan, naming the child column", func(ctx SpecContext) {
			mustExec(ctx, db, "CREATE SCHEMA neg3",
				"CREATE TABLE neg3.p (id varchar(36) NOT NULL PRIMARY KEY)",
				"CREATE TABLE neg3.c (id int NOT NULL PRIMARY KEY, p_id varchar(36) NOT NULL CONSTRAINT FK_c_p FOREIGN KEY REFERENCES neg3.p (id))")
			scope := declared("neg3", "p")
			desired := retype(inspect(ctx, db, scope).Schema, migrate.Name{Schema: "neg3", Name: "p"}, "id")
			_, err := planLive(ctx, db, scope, desired)
			Expect(err).To(MatchError(And(ContainSubstring("neg3.c.p_id"), ContainSubstring("same plan"))))
			Expect(columnType(ctx, "neg3.p", "id")).To(Equal("varchar"))
		})

		It("times out on a held migration lock and on a held table lock, changing nothing", func(ctx SpecContext) {
			mustExec(ctx, db, "CREATE SCHEMA neg4", "CREATE TABLE neg4.t (id int NOT NULL PRIMARY KEY)")
			scope := declared("neg4", "t")
			desired := cloneSchema(inspect(ctx, db, scope).Schema)
			t := tableOf(desired, migrate.Name{Schema: "neg4", Name: "t"})
			t.Columns = append(t.Columns, migrate.Column{Name: "extra", Type: "int", Nullable: true})
			artifact, err := planLive(ctx, db, scope, desired)
			Expect(err).NotTo(HaveOccurred())

			holder, err := db.Conn(ctx)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(holder.Close)
			_, err = holder.ExecContext(ctx, "DECLARE @s int; EXEC @s = sys.sp_getapplock @Resource=N'"+planner.LockResource+"', @LockMode='Exclusive', @LockOwner='Session', @LockTimeout=0")
			Expect(err).NotTo(HaveOccurred())
			_, err = engine.Apply(ctx, db, engine.ApplyRequest{Artifact: artifact, Hash: artifact.Hash, Acknowledgements: engine.Acknowledgements(artifact), LockTimeout: 300 * time.Millisecond})
			Expect(err).To(MatchError(ContainSubstring("lock migration")))
			_, err = holder.ExecContext(ctx, "EXEC sys.sp_releaseapplock @Resource=N'"+planner.LockResource+"', @LockOwner='Session'")
			Expect(err).NotTo(HaveOccurred())

			_, err = holder.ExecContext(ctx, "BEGIN TRANSACTION; SELECT 1 FROM neg4.t WITH (TABLOCKX, HOLDLOCK)")
			Expect(err).NotTo(HaveOccurred())
			_, err = engine.Apply(ctx, db, engine.ApplyRequest{Artifact: artifact, Hash: artifact.Hash, Acknowledgements: engine.Acknowledgements(artifact), LockTimeout: 300 * time.Millisecond})
			Expect(err).To(MatchError(ContainSubstring("Lock request time out period exceeded")))
			_, err = holder.ExecContext(ctx, "ROLLBACK TRANSACTION")
			Expect(err).NotTo(HaveOccurred())

			Expect(scalar[int](ctx, db, "SELECT COUNT(*) FROM sys.columns WHERE object_id = OBJECT_ID('neg4.t') AND name = 'extra'")).To(Equal(0))
			result, err := applyArtifact(ctx, db, artifact)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Outcome).To(Equal(engine.Applied))
		})
	})

	Describe("narrowed inspection", func() {
		setup := func(ctx SpecContext) {
			mustExec(ctx, db, "CREATE SCHEMA nar",
				"CREATE TABLE nar.ok (id int NOT NULL PRIMARY KEY, name nvarchar(20) NULL)",
				"CREATE TABLE nar.sparse_t (id int NOT NULL PRIMARY KEY, s int SPARSE NULL)",
				"CREATE TYPE nar.id_list AS TABLE (id int NOT NULL PRIMARY KEY)",
				"CREATE SYNONYM nar.ok_alias FOR nar.ok")
		}

		It("returns the declared table without error although the schema holds an unsupported object", func(ctx SpecContext) {
			setup(ctx)
			got := inspect(ctx, db, declared("nar", "ok", "missing"))
			Expect(got.Refusals).To(BeEmpty())
			Expect(got.Schema.Tables).To(HaveLen(1))
			Expect(got.Schema.Tables[0].Name).To(Equal(migrate.Name{Schema: "nar", Name: "ok"}))
		})

		It("does not refuse a table type in the whole-schema inspector either, since sys.objects holds no TT rows", func(ctx SpecContext) {
			mustExec(ctx, db, "CREATE SCHEMA ttonly", "CREATE TYPE ttonly.id_list AS TABLE (id int NOT NULL PRIMARY KEY)")
			_, err := snapshot.Inspect(ctx, db, snapshot.Options{Schemas: []string{"ttonly"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(scalar[int](ctx, db, "SELECT COUNT(*) FROM sys.objects WHERE type = 'TT' AND schema_id = SCHEMA_ID('ttonly')")).To(Equal(0))
		})

		It("reads a table type, which is modelled and so is not refused", func(ctx SpecContext) {
			got := inspect(ctx, db, declared("nar", "id_list"))
			Expect(got.Refusals).To(BeEmpty())
			Expect(got.Schema.TableTypes).To(HaveLen(1))
		})

		It("refuses a declared table with sparse columns instead of failing the inspection", func(ctx SpecContext) {
			got := inspect(ctx, db, declared("nar", "ok", "sparse_t"))
			Expect(got.Schema.Tables).To(HaveLen(1))
			Expect(got.Refusals).To(HaveLen(1))
			Expect(got.Refusals[0].Object.Name).To(Equal("sparse_t"))
			Expect(got.Refusals[0].Reason).To(ContainSubstring("special column settings"))
		})

		It("returns each unsupported object as a refusal in complete mode", func(ctx SpecContext) {
			got := inspect(ctx, db, engine.Scope{Mode: engine.ScopeComplete, Schemas: []string{"nar"}})
			var refused []string
			for _, r := range got.Refusals {
				refused = append(refused, r.Object.Name)
			}
			Expect(refused).To(ConsistOf("sparse_t", "ok_alias"))
			Expect(got.Schema.Tables).To(HaveLen(1))
			Expect(got.Schema.TableTypes).To(HaveLen(1))
		})
	})
})

func indexOfDependent(deps []engine.Dependent, want engine.Dependent) int {
	for i, d := range deps {
		if d.Kind == want.Kind && d.Table == want.Table && strings.EqualFold(d.Name, want.Name) {
			return i
		}
	}
	return -1
}
