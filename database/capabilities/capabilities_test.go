package capabilities

import (
	"context"
	"database/sql/driver"
	"regexp"

	"github.com/DATA-DOG/go-sqlmock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("backend capabilities", func() {
	probe := func(backend, statement string, columns []string, values ...driver.Value) (BackendCapabilities, error) {
		db, mock, err := sqlmock.New()
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { _ = db.Close() })
		mock.ExpectQuery(regexp.QuoteMeta(statement)).WillReturnRows(sqlmock.NewRows(columns).AddRow(values...))
		capabilities, err := ProbeBackendCapabilities(context.Background(), db, backend)
		Expect(mock.ExpectationsWereMet()).To(Succeed())
		return capabilities, err
	}

	It("reads PostgreSQL's numeric server version", func() {
		capabilities, err := probe("postgres", postgresCapabilitiesQuery,
			[]string{"database", "version", "version_num"}, "warehouse", "17.4", 170004)
		Expect(err).ToNot(HaveOccurred())
		Expect(capabilities.Database).To(Equal("warehouse"))
		Expect(capabilities.Features[BackendCapabilityArrayFilters]).To(Equal(BackendCapabilityStatus{
			Supported: true, Detail: "native and JSON arrays", MinimumVersion: "9.5",
		}))
	})

	DescribeTable("classifies MySQL array filtering from live version metadata",
		func(version string, supported bool, detail, minimum string) {
			capabilities, err := probe("mysql", mysqlCapabilitiesQuery,
				[]string{"database", "version"}, "warehouse", version)
			Expect(err).ToNot(HaveOccurred())
			Expect(capabilities.Features[BackendCapabilityArrayFilters]).To(Equal(BackendCapabilityStatus{
				Supported: supported, Detail: detail, MinimumVersion: minimum,
			}))
		},
		Entry("the minimum MySQL release", "8.0.4", true, "JSON arrays via JSON_TABLE", "8.0.4"),
		Entry("an older MySQL release", "8.0.3", false, "JSON arrays via JSON_TABLE", "8.0.4"),
		Entry("the minimum MariaDB release", "10.6.0-MariaDB", true, "JSON arrays via JSON_TABLE", "10.6.0"),
		Entry("an older MariaDB release", "10.5.27-MariaDB", false, "JSON arrays via JSON_TABLE", "10.6.0"),
	)

	probeSQLServer := func(version string, compatibilityLevel, engineEdition int, edition string) (BackendCapabilities, error) {
		return probe("sqlserver", sqlServerCapabilitiesQuery,
			[]string{"database", "version", "compatibility_level", "engine_edition", "edition"},
			"warehouse", version, compatibilityLevel, engineEdition, edition)
	}

	supported := func(c BackendCapabilities) map[BackendCapability]bool {
		result := map[BackendCapability]bool{}
		for name, status := range c.Features {
			result[name] = status.Supported
		}
		return result
	}

	It("uses SQL Server database compatibility rather than the Azure product version", func() {
		capabilities, err := probeSQLServer("12.0.2000.8", 160, 5, "SQL Azure")
		Expect(err).ToNot(HaveOccurred())
		Expect(capabilities.Version).To(Equal("12.0.2000.8"))
		Expect(capabilities.CompatibilityLevel).To(HaveValue(Equal(160)))
		Expect(capabilities.EngineEdition).To(HaveValue(Equal(5)))
		Expect(capabilities.Edition).To(Equal("SQL Azure"))
		Expect(capabilities.Features[BackendCapabilityArrayFilters]).To(Equal(BackendCapabilityStatus{
			Supported: true, Detail: "JSON arrays via OPENJSON", MinimumCompatibilityLevel: new(130),
		}))
	})

	It("rejects SQL Server databases below compatibility level 130", func() {
		capabilities, err := probe("sql_server", sqlServerCapabilitiesQuery,
			[]string{"database", "version", "compatibility_level", "engine_edition", "edition"},
			"warehouse", "16.0.1000.6", 120, 2, "Standard Edition (64-bit)")
		Expect(err).ToNot(HaveOccurred())
		Expect(capabilities.Features[BackendCapabilityArrayFilters].Supported).To(BeFalse())
		Expect(capabilities.Require(BackendCapabilityArrayFilters)).To(MatchError(ContainSubstring("compatibility level 130")))
	})

	It("keeps the SQL Server probe valid at database compatibility level 100", func() {
		Expect(sqlServerCapabilitiesQuery).ToNot(MatchRegexp(`(?i)STRING_AGG|TRY_CONVERT|\bIIF\b|\bCONCAT\s*\(`))
	})

	DescribeTable("derives the SQL Server feature set from version, compatibility level and edition",
		func(version string, compatibilityLevel, engineEdition int, edition string, expected map[BackendCapability]bool) {
			capabilities, err := probeSQLServer(version, compatibilityLevel, engineEdition, edition)
			Expect(err).ToNot(HaveOccurred())
			Expect(supported(capabilities)).To(Equal(expected))
		},
		Entry("2012 Standard at compatibility 100", "11.0.6020.0", 100, 2, "Standard Edition (64-bit)", map[BackendCapability]bool{
			BackendCapabilityArrayFilters: false, BackendCapabilityTransactionalDDL: true, BackendCapabilityOnlineIndex: false,
			BackendCapabilityDataCompression: false, BackendCapabilityTryConvert: false, BackendCapabilityCreateOrAlter: false,
			BackendCapabilityDropIfExists: false, BackendCapabilityTableRebuildRequired: false,
		}),
		Entry("2016 RTM Standard", "13.0.1601.5", 130, 2, "Standard Edition (64-bit)", map[BackendCapability]bool{
			BackendCapabilityArrayFilters: true, BackendCapabilityTransactionalDDL: true, BackendCapabilityOnlineIndex: false,
			BackendCapabilityDataCompression: false, BackendCapabilityTryConvert: true, BackendCapabilityCreateOrAlter: false,
			BackendCapabilityDropIfExists: true, BackendCapabilityTableRebuildRequired: false,
		}),
		Entry("2016 RTM Enterprise", "13.0.1601.5", 130, 3, "Enterprise Edition (64-bit)", map[BackendCapability]bool{
			BackendCapabilityArrayFilters: true, BackendCapabilityTransactionalDDL: true, BackendCapabilityOnlineIndex: true,
			BackendCapabilityDataCompression: true, BackendCapabilityTryConvert: true, BackendCapabilityCreateOrAlter: false,
			BackendCapabilityDropIfExists: true, BackendCapabilityTableRebuildRequired: false,
		}),
		Entry("2016 SP1 Standard", "13.0.4001.0", 130, 2, "Standard Edition (64-bit)", map[BackendCapability]bool{
			BackendCapabilityArrayFilters: true, BackendCapabilityTransactionalDDL: true, BackendCapabilityOnlineIndex: false,
			BackendCapabilityDataCompression: true, BackendCapabilityTryConvert: true, BackendCapabilityCreateOrAlter: true,
			BackendCapabilityDropIfExists: true, BackendCapabilityTableRebuildRequired: false,
		}),
		Entry("2019 Enterprise at compatibility 100", "15.0.4123.1", 100, 3, "Enterprise Edition (64-bit)", map[BackendCapability]bool{
			BackendCapabilityArrayFilters: false, BackendCapabilityTransactionalDDL: true, BackendCapabilityOnlineIndex: true,
			BackendCapabilityDataCompression: true, BackendCapabilityTryConvert: false, BackendCapabilityCreateOrAlter: true,
			BackendCapabilityDropIfExists: true, BackendCapabilityTableRebuildRequired: false,
		}),
		Entry("2022 Express at compatibility 100", "16.0.4100.1", 100, 4, "Express Edition (64-bit)", map[BackendCapability]bool{
			BackendCapabilityArrayFilters: false, BackendCapabilityTransactionalDDL: true, BackendCapabilityOnlineIndex: false,
			BackendCapabilityDataCompression: true, BackendCapabilityTryConvert: false, BackendCapabilityCreateOrAlter: true,
			BackendCapabilityDropIfExists: true, BackendCapabilityTableRebuildRequired: false,
		}),
		Entry("2022 Standard at compatibility 100", "16.0.4100.1", 100, 2, "Standard Edition (64-bit)", map[BackendCapability]bool{
			BackendCapabilityArrayFilters: false, BackendCapabilityTransactionalDDL: true, BackendCapabilityOnlineIndex: false,
			BackendCapabilityDataCompression: true, BackendCapabilityTryConvert: false, BackendCapabilityCreateOrAlter: true,
			BackendCapabilityDropIfExists: true, BackendCapabilityTableRebuildRequired: false,
		}),
		Entry("Azure SQL Database", "12.0.2000.8", 160, 5, "SQL Azure", map[BackendCapability]bool{
			BackendCapabilityArrayFilters: true, BackendCapabilityTransactionalDDL: true, BackendCapabilityOnlineIndex: true,
			BackendCapabilityDataCompression: true, BackendCapabilityTryConvert: true, BackendCapabilityCreateOrAlter: true,
			BackendCapabilityDropIfExists: true, BackendCapabilityTableRebuildRequired: false,
		}),
		Entry("Azure SQL Managed Instance at compatibility 100", "12.0.2000.8", 100, 8, "SQL Azure", map[BackendCapability]bool{
			BackendCapabilityArrayFilters: false, BackendCapabilityTransactionalDDL: true, BackendCapabilityOnlineIndex: true,
			BackendCapabilityDataCompression: true, BackendCapabilityTryConvert: false, BackendCapabilityCreateOrAlter: true,
			BackendCapabilityDropIfExists: true, BackendCapabilityTableRebuildRequired: false,
		}),
		Entry("an engine edition whose features are not verified", "12.0.2000.8", 130, 6, "SQL Azure", map[BackendCapability]bool{
			BackendCapabilityArrayFilters: true, BackendCapabilityTransactionalDDL: true, BackendCapabilityOnlineIndex: false,
			BackendCapabilityDataCompression: false, BackendCapabilityTryConvert: true, BackendCapabilityCreateOrAlter: false,
			BackendCapabilityDropIfExists: false, BackendCapabilityTableRebuildRequired: false,
		}),
	)

	It("names the missing SQL Server requirement when a feature is unsupported", func() {
		capabilities, err := probeSQLServer("13.0.1601.5", 100, 2, "Standard Edition (64-bit)")
		Expect(err).ToNot(HaveOccurred())
		Expect(capabilities.Require(BackendCapabilityTryConvert)).To(MatchError(And(
			HavePrefix(`backend "sqlserver" does not support capability "tryConvert": `),
			HaveSuffix("requires compatibility level 110 or newer"))))
		Expect(capabilities.Require(BackendCapabilityCreateOrAlter)).To(MatchError(ContainSubstring("requires version 13.0.4001 or newer")))
		Expect(capabilities.Require(BackendCapabilityDropIfExists)).To(Succeed())
		Expect(capabilities.Require(BackendCapabilityOnlineIndex)).To(MatchError(And(
			ContainSubstring(`capability "onlineIndex"`), ContainSubstring("Standard Edition (64-bit)"))))
		Expect(capabilities.Require(BackendCapabilityDataCompression)).To(MatchError(ContainSubstring("requires version 13.0.4001 or newer")))
	})

	It("fails when SQL Server reports no engine edition", func() {
		_, err := probe("sqlserver", sqlServerCapabilitiesQuery,
			[]string{"database", "version", "compatibility_level", "engine_edition", "edition"},
			"warehouse", "16.0.4100.1", 160, nil, "Standard Edition (64-bit)")
		Expect(err).To(HaveOccurred())
	})

	It("notes that PostgreSQL online index builds cannot run in a transaction", func() {
		capabilities, err := probe("postgres", postgresCapabilitiesQuery,
			[]string{"database", "version", "version_num"}, "warehouse", "17.4", 170004)
		Expect(err).ToNot(HaveOccurred())
		Expect(capabilities.Features[BackendCapabilityOnlineIndex].Detail).To(And(
			ContainSubstring("CONCURRENTLY"), ContainSubstring("cannot run inside a transaction")))
	})

	DescribeTable("declares every feature for the non SQL Server backends",
		func(backend, statement string, columns []string, values []driver.Value, expected map[BackendCapability]bool) {
			capabilities, err := probe(backend, statement, columns, values...)
			Expect(err).ToNot(HaveOccurred())
			Expect(supported(capabilities)).To(Equal(expected))
		},
		Entry("PostgreSQL 17", "postgres", postgresCapabilitiesQuery, []string{"database", "version", "version_num"},
			[]driver.Value{"warehouse", "17.4", 170004}, map[BackendCapability]bool{
				BackendCapabilityArrayFilters: true, BackendCapabilityTransactionalDDL: true, BackendCapabilityOnlineIndex: true,
				BackendCapabilityDataCompression: false, BackendCapabilityTryConvert: false, BackendCapabilityCreateOrAlter: false,
				BackendCapabilityDropIfExists: true, BackendCapabilityTableRebuildRequired: false,
			}),
		Entry("MySQL 8.0.21", "mysql", mysqlCapabilitiesQuery, []string{"database", "version"},
			[]driver.Value{"warehouse", "8.0.21"}, map[BackendCapability]bool{
				BackendCapabilityArrayFilters: true, BackendCapabilityTransactionalDDL: false, BackendCapabilityOnlineIndex: false,
				BackendCapabilityDataCompression: false, BackendCapabilityTryConvert: false, BackendCapabilityCreateOrAlter: false,
				BackendCapabilityDropIfExists: true, BackendCapabilityTableRebuildRequired: false,
			}),
		Entry("SQLite 3.50", "sqlite", sqliteCapabilitiesQuery, []string{"database", "version", "json_arrays"},
			[]driver.Value{"main", "3.50.4", 1}, map[BackendCapability]bool{
				BackendCapabilityArrayFilters: true, BackendCapabilityTransactionalDDL: true, BackendCapabilityOnlineIndex: false,
				BackendCapabilityDataCompression: false, BackendCapabilityTryConvert: false, BackendCapabilityCreateOrAlter: false,
				BackendCapabilityDropIfExists: true, BackendCapabilityTableRebuildRequired: true,
			}),
		Entry("ClickHouse 25.8", "clickhouse", clickHouseCapabilitiesQuery, []string{"database", "version"},
			[]driver.Value{"default", "25.8.3.66"}, map[BackendCapability]bool{
				BackendCapabilityArrayFilters: true, BackendCapabilityTransactionalDDL: false, BackendCapabilityOnlineIndex: false,
				BackendCapabilityDataCompression: false, BackendCapabilityTryConvert: false, BackendCapabilityCreateOrAlter: false,
				BackendCapabilityDropIfExists: true, BackendCapabilityTableRebuildRequired: false,
			}),
	)

	It("changes the fingerprint when a feature flips or the edition changes", func() {
		standard, err := probeSQLServer("16.0.4100.1", 130, 2, "Standard Edition (64-bit)")
		Expect(err).ToNot(HaveOccurred())
		enterprise, err := probeSQLServer("16.0.4100.1", 130, 3, "Enterprise Edition (64-bit)")
		Expect(err).ToNot(HaveOccurred())
		again, err := probeSQLServer("16.0.4100.1", 130, 2, "Standard Edition (64-bit)")
		Expect(err).ToNot(HaveOccurred())

		standardFingerprint, err := standard.Fingerprint()
		Expect(err).ToNot(HaveOccurred())
		enterpriseFingerprint, err := enterprise.Fingerprint()
		Expect(err).ToNot(HaveOccurred())
		againFingerprint, err := again.Fingerprint()
		Expect(err).ToNot(HaveOccurred())
		Expect(againFingerprint).To(Equal(standardFingerprint))
		Expect(enterpriseFingerprint).ToNot(Equal(standardFingerprint))

		flipped := standard
		flipped.Features = map[BackendCapability]BackendCapabilityStatus{}
		for name, status := range standard.Features {
			flipped.Features[name] = status
		}
		flipped.Features[BackendCapabilityOnlineIndex] = BackendCapabilityStatus{Supported: true}
		flippedFingerprint, err := flipped.Fingerprint()
		Expect(err).ToNot(HaveOccurred())
		Expect(flippedFingerprint).ToNot(Equal(standardFingerprint))
	})

	DescribeTable("probes JSON expansion instead of assuming SQLite build features",
		func(result int, supported bool) {
			capabilities, err := probe("sqlite", sqliteCapabilitiesQuery,
				[]string{"database", "version", "json_arrays"}, "main", "3.50.4", result)
			Expect(err).ToNot(HaveOccurred())
			Expect(capabilities.Features[BackendCapabilityArrayFilters].Supported).To(Equal(supported))
		},
		Entry("JSON functions are present", 1, true),
		Entry("JSON functions return an unexpected result", 0, false),
	)

	It("reports ClickHouse array support as a static server feature", func() {
		capabilities, err := probe("clickhouse", clickHouseCapabilitiesQuery,
			[]string{"database", "version"}, "default", "25.8.3.66")
		Expect(err).ToNot(HaveOccurred())
		Expect(capabilities.Features[BackendCapabilityArrayFilters]).To(Equal(BackendCapabilityStatus{
			Supported: true, Detail: "native Array(String) values",
		}))
	})

	It("fails when required metadata is empty", func() {
		_, err := probe("postgres", postgresCapabilitiesQuery,
			[]string{"database", "version", "version_num"}, "warehouse", "", 170004)
		Expect(err).To(MatchError(ContainSubstring("version is empty")))
	})

	It("fails when a live probe has no selected database", func() {
		_, err := probe("mysql", mysqlCapabilitiesQuery,
			[]string{"database", "version"}, "", "8.0.21")
		Expect(err).To(MatchError(ContainSubstring("database is empty")))
	})

	It("fails when version metadata cannot be interpreted", func() {
		_, err := probe("mysql", mysqlCapabilitiesQuery,
			[]string{"database", "version"}, "warehouse", "not-a-version")
		Expect(err).To(MatchError(ContainSubstring("parse MySQL version")))
	})

	It("rejects capability metadata without a feature status", func() {
		err := (BackendCapabilities{Backend: "postgres", Features: map[BackendCapability]BackendCapabilityStatus{}}).Validate()
		Expect(err).To(MatchError(ContainSubstring("features are missing")))
	})

	It("fingerprints feature metadata deterministically", func() {
		left := BackendCapabilities{
			Backend: "postgres", Version: "17.4", Database: "warehouse",
			Features: map[BackendCapability]BackendCapabilityStatus{
				BackendCapabilityArrayFilters: {Supported: true, MinimumVersion: "9.5"},
			},
		}
		right := left
		right.Features = map[BackendCapability]BackendCapabilityStatus{
			BackendCapabilityArrayFilters: {Supported: true, MinimumVersion: "9.5"},
		}
		leftFingerprint, err := left.Fingerprint()
		Expect(err).ToNot(HaveOccurred())
		rightFingerprint, err := right.Fingerprint()
		Expect(err).ToNot(HaveOccurred())
		Expect(rightFingerprint).To(Equal(leftFingerprint))

		right.Version = "17.5"
		changed, err := right.Fingerprint()
		Expect(err).ToNot(HaveOccurred())
		Expect(changed).ToNot(Equal(leftFingerprint))
	})

	It("fingerprints static provider capabilities without invented server metadata", func() {
		capabilities := BackendCapabilities{
			Backend: "opensearch",
			Features: map[BackendCapability]BackendCapabilityStatus{
				BackendCapabilityArrayFilters: {Supported: true, Detail: "native keyword arrays"},
			},
		}

		fingerprint, err := capabilities.Fingerprint()
		Expect(err).ToNot(HaveOccurred())
		Expect(fingerprint).To(HavePrefix("backend-capabilities-v2:"))
		Expect(capabilities.Require(BackendCapabilityArrayFilters)).To(Succeed())
	})
})
