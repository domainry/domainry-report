// Package module is the public embedded-module facade.
package module

import (
	"github.com/domainry/domainry-foundation/schemaownership"
	"github.com/domainry/domainry-report-sdk/modulehost"
	moduleassembly "github.com/domainry/domainry-report/internal/assembly/module"
	reportmigration "github.com/domainry/domainry-report/internal/infrastructure/persistence/database/migration"
)

type Factory = moduleassembly.Factory

func NewFactory() *Factory { return moduleassembly.NewFactory() }

func SchemaOwnership() []schemaownership.Table { return moduleassembly.SchemaOwnership() }

func OwnedTables() []string { return moduleassembly.OwnedTables() }

// SchemaMigrations exposes Report's canonical source-owned snapshot schema for
// cross-module composition verification without exposing its Store.
func SchemaMigrations(driver, schema string) ([]modulehost.SchemaMigration, error) {
	return reportmigration.Migrations(driver, schema)
}
