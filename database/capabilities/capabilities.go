package capabilities

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Queryer is what a probe reads through; *sql.DB, *sql.Conn and *sql.Tx satisfy it.
type Queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type BackendCapability string

const (
	BackendCapabilityArrayFilters BackendCapability = "arrayFilters"
	// BackendCapabilityTransactionalDDL: DDL can run inside a transaction and roll back.
	BackendCapabilityTransactionalDDL BackendCapability = "transactionalDDL"
	// BackendCapabilityOnlineIndex: an index can be built without blocking writers.
	BackendCapabilityOnlineIndex BackendCapability = "onlineIndex"
	// BackendCapabilityDataCompression: row/page data compression can be applied to tables and indexes.
	BackendCapabilityDataCompression BackendCapability = "dataCompression"
	// BackendCapabilityTryConvert: TRY_CONVERT is available to SQL in this database.
	BackendCapabilityTryConvert BackendCapability = "tryConvert"
	// BackendCapabilityCreateOrAlter: CREATE OR ALTER is available for routines and views.
	BackendCapabilityCreateOrAlter BackendCapability = "createOrAlter"
	// BackendCapabilityDropIfExists: DROP ... IF EXISTS is available.
	BackendCapabilityDropIfExists BackendCapability = "dropIfExists"
	// BackendCapabilityTableRebuildRequired: altering a column requires rebuilding the table.
	BackendCapabilityTableRebuildRequired BackendCapability = "tableRebuildRequired"
)

type BackendCapabilityStatus struct {
	Supported                 bool   `json:"supported"`
	Detail                    string `json:"detail,omitempty"`
	MinimumVersion            string `json:"minimumVersion,omitempty"`
	MinimumCompatibilityLevel *int   `json:"minimumCompatibilityLevel,omitempty"`
}

type BackendCapabilities struct {
	Backend            string                                        `json:"backend"`
	Version            string                                        `json:"version"`
	Database           string                                        `json:"database"`
	CompatibilityLevel *int                                          `json:"compatibilityLevel,omitempty"`
	EngineEdition      *int                                          `json:"engineEdition,omitempty"`
	Edition            string                                        `json:"edition,omitempty"`
	Features           map[BackendCapability]BackendCapabilityStatus `json:"features"`
}

func (c BackendCapabilities) Require(capability BackendCapability) error {
	if err := c.Validate(); err != nil {
		return err
	}
	status, ok := c.Features[capability]
	if !ok {
		return fmt.Errorf("backend %q did not report capability %q", c.Backend, capability)
	}
	if status.Supported {
		return nil
	}
	requirements := make([]string, 0, 2)
	if status.MinimumVersion != "" {
		requirements = append(requirements, "version "+status.MinimumVersion+" or newer")
	}
	if status.MinimumCompatibilityLevel != nil {
		requirements = append(requirements, fmt.Sprintf("compatibility level %d or newer", *status.MinimumCompatibilityLevel))
	}
	reason := status.Detail
	if len(requirements) > 0 {
		if reason != "" {
			reason += "; "
		}
		reason += "requires " + strings.Join(requirements, " and ")
	}
	if reason == "" {
		reason = "the feature is unavailable"
	}
	return fmt.Errorf("backend %q does not support capability %q: %s", c.Backend, capability, reason)
}

func (c BackendCapabilities) Fingerprint() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	type feature struct {
		Name   BackendCapability       `json:"name"`
		Status BackendCapabilityStatus `json:"status"`
	}
	names := make([]string, 0, len(c.Features))
	for name := range c.Features {
		names = append(names, string(name))
	}
	sort.Strings(names)
	features := make([]feature, 0, len(names))
	for _, name := range names {
		capability := BackendCapability(name)
		features = append(features, feature{Name: capability, Status: c.Features[capability]})
	}
	payload, err := json.Marshal(struct {
		Backend            string    `json:"backend"`
		Version            string    `json:"version"`
		Database           string    `json:"database"`
		CompatibilityLevel *int      `json:"compatibilityLevel,omitempty"`
		EngineEdition      *int      `json:"engineEdition,omitempty"`
		Edition            string    `json:"edition,omitempty"`
		Features           []feature `json:"features"`
	}{c.Backend, c.Version, c.Database, c.CompatibilityLevel, c.EngineEdition, c.Edition, features})
	if err != nil {
		return "", fmt.Errorf("encode backend capability fingerprint: %w", err)
	}
	digest := sha256.Sum256(payload)
	return "backend-capabilities-v2:" + hex.EncodeToString(digest[:]), nil
}

func (c BackendCapabilities) Validate() error {
	if strings.TrimSpace(c.Backend) == "" {
		return fmt.Errorf("backend capability metadata backend is empty")
	}
	if len(c.Features) == 0 {
		return fmt.Errorf("backend capability metadata features are missing")
	}
	return nil
}

const (
	postgresCapabilitiesQuery  = `SELECT current_database(), current_setting('server_version'), current_setting('server_version_num')::integer`
	mysqlCapabilitiesQuery     = `SELECT COALESCE(DATABASE(), ''), VERSION()`
	sqlServerCapabilitiesQuery = `SELECT DB_NAME(), CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(128)), compatibility_level,
       CAST(SERVERPROPERTY('EngineEdition') AS int), CAST(SERVERPROPERTY('Edition') AS nvarchar(128))
FROM sys.databases
WHERE name = DB_NAME()`
	sqliteCapabilitiesQuery = `SELECT 'main', sqlite_version(),
       EXISTS (SELECT 1 FROM json_each('["probe"]') WHERE type = 'text' AND value = 'probe')`
	clickHouseCapabilitiesQuery = `SELECT currentDatabase(), version()`
)

func ProbeBackendCapabilities(ctx context.Context, db Queryer, backend string) (BackendCapabilities, error) {
	if db == nil {
		return BackendCapabilities{}, fmt.Errorf("probe backend capabilities: nil sql database")
	}
	backend = normalizeCapabilityBackend(backend)
	var capabilities BackendCapabilities
	var err error
	switch backend {
	case "postgres":
		capabilities, err = probePostgresCapabilities(ctx, db)
	case "mysql":
		capabilities, err = probeMySQLCapabilities(ctx, db)
	case "sqlserver":
		capabilities, err = probeSQLServerCapabilities(ctx, db)
	case "sqlite":
		capabilities, err = probeSQLiteCapabilities(ctx, db)
	case "clickhouse":
		capabilities, err = probeClickHouseCapabilities(ctx, db)
	default:
		return BackendCapabilities{}, fmt.Errorf("probe backend capabilities: unsupported SQL backend %q", backend)
	}
	if err != nil {
		return BackendCapabilities{}, fmt.Errorf("probe %s backend capabilities: %w", backend, err)
	}
	if err := capabilities.Validate(); err != nil {
		return BackendCapabilities{}, err
	}
	if strings.TrimSpace(capabilities.Version) == "" {
		return BackendCapabilities{}, fmt.Errorf("backend capability metadata version is empty")
	}
	if strings.TrimSpace(capabilities.Database) == "" {
		return BackendCapabilities{}, fmt.Errorf("backend capability metadata database is empty")
	}
	return capabilities, nil
}

func probePostgresCapabilities(ctx context.Context, db Queryer) (BackendCapabilities, error) {
	var database, version string
	var versionNumber int
	if err := db.QueryRowContext(ctx, postgresCapabilitiesQuery).Scan(&database, &version, &versionNumber); err != nil {
		return BackendCapabilities{}, err
	}
	status := BackendCapabilityStatus{Detail: "native and JSON arrays", MinimumVersion: "9.5"}
	if versionNumber >= 90500 {
		status.Supported = true
	}
	return capabilities(BackendCapabilities{Backend: "postgres", Version: version, Database: database}, map[BackendCapability]BackendCapabilityStatus{
		BackendCapabilityArrayFilters:         status,
		BackendCapabilityTransactionalDDL:     supportedStatus("DDL is transactional"),
		BackendCapabilityOnlineIndex:          supportedStatus("CREATE INDEX CONCURRENTLY cannot run inside a transaction"),
		BackendCapabilityDataCompression:      unsupportedStatus("no table or index data compression statement"),
		BackendCapabilityTryConvert:           unsupportedStatus("TRY_CONVERT is SQL Server only"),
		BackendCapabilityCreateOrAlter:        unsupportedStatus("CREATE OR ALTER is SQL Server only"),
		BackendCapabilityDropIfExists:         supportedStatus("DROP ... IF EXISTS"),
		BackendCapabilityTableRebuildRequired: unsupportedStatus("ALTER COLUMN is performed in place or by an engine managed rewrite"),
	}), nil
}

func probeMySQLCapabilities(ctx context.Context, db Queryer) (BackendCapabilities, error) {
	var database, version string
	if err := db.QueryRowContext(ctx, mysqlCapabilitiesQuery).Scan(&database, &version); err != nil {
		return BackendCapabilities{}, err
	}
	minimum, minimumVersion, product := [3]int{8, 0, 4}, "8.0.4", "MySQL"
	if strings.Contains(strings.ToLower(version), "mariadb") {
		minimum, minimumVersion, product = [3]int{10, 6, 0}, "10.6.0", "MariaDB"
	}
	status := BackendCapabilityStatus{Detail: "JSON arrays via JSON_TABLE", MinimumVersion: minimumVersion}
	parsed, err := parseBackendVersion(version)
	if err != nil {
		return BackendCapabilities{}, fmt.Errorf("parse %s version %q: %w", product, version, err)
	}
	if versionAtLeast(parsed, minimum) {
		status.Supported = true
	}
	return capabilities(BackendCapabilities{Backend: "mysql", Version: version, Database: database}, map[BackendCapability]BackendCapabilityStatus{
		BackendCapabilityArrayFilters:         status,
		BackendCapabilityTransactionalDDL:     unsupportedStatus("DDL statements implicitly commit"),
		BackendCapabilityOnlineIndex:          unsupportedStatus("index builds are not reported as non-blocking"),
		BackendCapabilityDataCompression:      unsupportedStatus("not exposed as a portable schema feature"),
		BackendCapabilityTryConvert:           unsupportedStatus("TRY_CONVERT is SQL Server only"),
		BackendCapabilityCreateOrAlter:        unsupportedStatus("CREATE OR ALTER is SQL Server only"),
		BackendCapabilityDropIfExists:         supportedStatus("DROP ... IF EXISTS"),
		BackendCapabilityTableRebuildRequired: unsupportedStatus("ALTER COLUMN is performed in place or by an engine managed rewrite"),
	}), nil
}

func probeSQLiteCapabilities(ctx context.Context, db Queryer) (BackendCapabilities, error) {
	var database, version string
	var jsonArrays int
	if err := db.QueryRowContext(ctx, sqliteCapabilitiesQuery).Scan(&database, &version, &jsonArrays); err != nil {
		return BackendCapabilities{}, err
	}
	status := BackendCapabilityStatus{Supported: jsonArrays == 1, Detail: "JSON arrays via json_each"}
	if !status.Supported {
		status.Detail = "SQLite JSON functions are unavailable"
	}
	return capabilities(BackendCapabilities{Backend: "sqlite", Version: version, Database: database}, map[BackendCapability]BackendCapabilityStatus{
		BackendCapabilityArrayFilters:         status,
		BackendCapabilityTransactionalDDL:     supportedStatus("DDL is transactional"),
		BackendCapabilityOnlineIndex:          unsupportedStatus("a single writer holds the database lock during an index build"),
		BackendCapabilityDataCompression:      unsupportedStatus("no table or index data compression"),
		BackendCapabilityTryConvert:           unsupportedStatus("TRY_CONVERT is SQL Server only"),
		BackendCapabilityCreateOrAlter:        unsupportedStatus("CREATE OR ALTER is SQL Server only"),
		BackendCapabilityDropIfExists:         supportedStatus("DROP ... IF EXISTS"),
		BackendCapabilityTableRebuildRequired: supportedStatus("ALTER COLUMN requires creating a new table, copying rows and renaming it"),
	}), nil
}

func probeClickHouseCapabilities(ctx context.Context, db Queryer) (BackendCapabilities, error) {
	var database, version string
	if err := db.QueryRowContext(ctx, clickHouseCapabilitiesQuery).Scan(&database, &version); err != nil {
		return BackendCapabilities{}, err
	}
	return capabilities(BackendCapabilities{Backend: "clickhouse", Version: version, Database: database}, map[BackendCapability]BackendCapabilityStatus{
		BackendCapabilityArrayFilters:         supportedStatus("native Array(String) values"),
		BackendCapabilityTransactionalDDL:     unsupportedStatus("DDL is not transactional"),
		BackendCapabilityOnlineIndex:          unsupportedStatus("no transactional index build"),
		BackendCapabilityDataCompression:      unsupportedStatus("compression is a column codec, not a table or index option"),
		BackendCapabilityTryConvert:           unsupportedStatus("TRY_CONVERT is SQL Server only"),
		BackendCapabilityCreateOrAlter:        unsupportedStatus("CREATE OR ALTER is SQL Server only"),
		BackendCapabilityDropIfExists:         supportedStatus("DROP ... IF EXISTS"),
		BackendCapabilityTableRebuildRequired: unsupportedStatus("ALTER COLUMN is performed by an engine managed mutation"),
	}), nil
}

func capabilities(base BackendCapabilities, features map[BackendCapability]BackendCapabilityStatus) BackendCapabilities {
	base.Features = features
	return base
}

func supportedStatus(detail string) BackendCapabilityStatus {
	return BackendCapabilityStatus{Supported: true, Detail: detail}
}

func unsupportedStatus(detail string) BackendCapabilityStatus {
	return BackendCapabilityStatus{Detail: detail}
}

func normalizeCapabilityBackend(backend string) string {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "postgres", "postgresql", "pgx":
		return "postgres"
	case "mysql":
		return "mysql"
	case "sql_server", "sqlserver", "mssql":
		return "sqlserver"
	case "sqlite", "sqlite3":
		return "sqlite"
	case "clickhouse":
		return "clickhouse"
	default:
		return strings.ToLower(strings.TrimSpace(backend))
	}
}

func parseBackendVersion(version string) ([3]int, error) {
	var result [3]int
	parts := strings.SplitN(strings.TrimSpace(version), ".", len(result))
	if len(parts) != len(result) {
		return result, fmt.Errorf("expected major.minor.patch")
	}
	for index, part := range parts {
		digits := strings.TrimLeftFunc(part, func(char rune) bool { return char >= '0' && char <= '9' })
		digits = part[:len(part)-len(digits)]
		if digits == "" {
			return result, fmt.Errorf("component %d is not numeric", index+1)
		}
		value, err := strconv.Atoi(digits)
		if err != nil {
			return result, fmt.Errorf("component %d: %w", index+1, err)
		}
		result[index] = value
	}
	return result, nil
}

func versionAtLeast(version, minimum [3]int) bool {
	for index := range version {
		if version[index] != minimum[index] {
			return version[index] > minimum[index]
		}
	}
	return true
}

func intPointer(value int) *int {
	return &value
}
