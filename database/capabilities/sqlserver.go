package capabilities

import (
	"context"
	"fmt"
)

// SQL Server EngineEdition values from SERVERPROPERTY('EngineEdition').
const (
	engineEditionStandard        = 2 // Standard, Web, Business Intelligence
	engineEditionEnterprise      = 3 // Enterprise, Developer, Evaluation
	engineEditionExpress         = 4
	engineEditionAzureSQLDB      = 5
	engineEditionManagedInstance = 8
)

var (
	// sqlServer2016 is the first release with DROP ... IF EXISTS.
	sqlServer2016 = [3]int{13, 0, 0}
	// sqlServer2016SP1 introduced CREATE OR ALTER and moved data compression into every edition.
	sqlServer2016SP1 = [3]int{13, 0, 4001}
)

type sqlServerProfile struct {
	version            [3]int
	compatibilityLevel int
	engineEdition      int
	edition            string
}

func probeSQLServerCapabilities(ctx context.Context, db Queryer) (BackendCapabilities, error) {
	var database, version, edition string
	var compatibilityLevel, engineEdition int
	err := db.QueryRowContext(ctx, sqlServerCapabilitiesQuery).Scan(&database, &version, &compatibilityLevel, &engineEdition, &edition)
	if err != nil {
		return BackendCapabilities{}, err
	}
	parsed, err := parseBackendVersion(version)
	if err != nil {
		return BackendCapabilities{}, fmt.Errorf("parse SQL Server version %q: %w", version, err)
	}
	profile := sqlServerProfile{version: parsed, compatibilityLevel: compatibilityLevel, engineEdition: engineEdition, edition: edition}
	return capabilities(BackendCapabilities{
		Backend: "sqlserver", Version: version, Database: database, Edition: edition,
		CompatibilityLevel: intPointer(compatibilityLevel), EngineEdition: intPointer(engineEdition),
	}, profile.features()), nil
}

func (p sqlServerProfile) features() map[BackendCapability]BackendCapabilityStatus {
	minimumCompatibility := intPointer(130)
	return map[BackendCapability]BackendCapabilityStatus{
		BackendCapabilityArrayFilters: {
			Supported: p.compatibilityLevel >= *minimumCompatibility, Detail: "JSON arrays via OPENJSON", MinimumCompatibilityLevel: minimumCompatibility,
		},
		BackendCapabilityTransactionalDDL: supportedStatus("DDL is transactional"),
		BackendCapabilityOnlineIndex:      p.onlineIndex(),
		BackendCapabilityDataCompression:  p.dataCompression(),
		BackendCapabilityTryConvert: {
			Supported: p.compatibilityLevel >= 110, Detail: "TRY_CONVERT is a database compatibility level feature", MinimumCompatibilityLevel: intPointer(110),
		},
		BackendCapabilityCreateOrAlter: p.versionGated(
			"CREATE OR ALTER for views, procedures, functions and triggers (not tables)", sqlServer2016SP1, "13.0.4001"),
		BackendCapabilityDropIfExists:         p.versionGated("DROP ... IF EXISTS", sqlServer2016, "13.0.0"),
		BackendCapabilityTableRebuildRequired: unsupportedStatus("ALTER COLUMN is performed in place"),
	}
}

func (p sqlServerProfile) isAzure() bool {
	return p.engineEdition == engineEditionAzureSQLDB || p.engineEdition == engineEditionManagedInstance
}

func (p sqlServerProfile) isInstalledEngine() bool {
	switch p.engineEdition {
	case engineEditionStandard, engineEditionEnterprise, engineEditionExpress:
		return true
	}
	return false
}

func (p sqlServerProfile) unverifiedEdition(feature string) BackendCapabilityStatus {
	return unsupportedStatus(fmt.Sprintf("%s is not verified for engine edition %d (%s)", feature, p.engineEdition, p.edition))
}

// versionGated reports a feature that every verified engine edition has from the minimum release on.
func (p sqlServerProfile) versionGated(feature string, minimum [3]int, minimumText string) BackendCapabilityStatus {
	switch {
	case p.isAzure():
		return supportedStatus(feature)
	case !p.isInstalledEngine():
		return p.unverifiedEdition(feature)
	}
	return BackendCapabilityStatus{Supported: versionAtLeast(p.version, minimum), Detail: feature, MinimumVersion: minimumText}
}

func (p sqlServerProfile) onlineIndex() BackendCapabilityStatus {
	const feature = "ONLINE = ON index builds"
	if p.isAzure() || p.engineEdition == engineEditionEnterprise {
		return supportedStatus(feature)
	}
	if !p.isInstalledEngine() {
		return p.unverifiedEdition(feature)
	}
	return unsupportedStatus(fmt.Sprintf("%s require Enterprise, Developer or Evaluation edition, not %q", feature, p.edition))
}

func (p sqlServerProfile) dataCompression() BackendCapabilityStatus {
	const feature = "row and page data compression"
	if p.isAzure() || p.engineEdition == engineEditionEnterprise {
		return supportedStatus(feature)
	}
	if !p.isInstalledEngine() {
		return p.unverifiedEdition(feature)
	}
	return BackendCapabilityStatus{
		Supported: versionAtLeast(p.version, sqlServer2016SP1), MinimumVersion: "13.0.4001",
		Detail: feature + " is Enterprise only before SQL Server 2016 SP1",
	}
}
