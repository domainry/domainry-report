package migration

import (
	"github.com/domainry/domainry-report-sdk/modulehost"
	reportschema "github.com/domainry/domainry-report/internal/infrastructure/persistence/database/schema"
)

const SchemaVersion uint = 1

// Migrations returns source-owned migration content for registration in the
// host's single migration ledger. Report never creates a private ledger.
func Migrations(driver, schemaName string) ([]modulehost.SchemaMigration, error) {
	statements, err := reportschema.Statements(driver, schemaName)
	if err != nil {
		return nil, err
	}
	return []modulehost.SchemaMigration{{Version: SchemaVersion, Name: "report_foundation", Statements: statements}}, nil
}
