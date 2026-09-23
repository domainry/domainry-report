package module

import (
	"context"
	"fmt"

	shareddefinition "github.com/domainry/domainry-foundation/definition"
	"github.com/domainry/domainry-foundation/schemaownership"
	metadatasdk "github.com/domainry/domainry-metadata-sdk"
	reportsdk "github.com/domainry/domainry-report-sdk"
	"github.com/domainry/domainry-report-sdk/modulehost"
	reportadapter "github.com/domainry/domainry-report/internal/adapter/reportsdk"
	reportapplication "github.com/domainry/domainry-report/internal/application/report"
	reportmigration "github.com/domainry/domainry-report/internal/infrastructure/persistence/database/migration"
	reportpersistence "github.com/domainry/domainry-report/internal/infrastructure/persistence/database/report"
)

type Factory struct{}

func NewFactory() *Factory { return &Factory{} }

func SchemaOwnership() []schemaownership.Table { return reportmigration.SchemaOwnership() }

func OwnedTables() []string { return reportmigration.OwnedTables() }

func (*Factory) Open(ctx context.Context, application reportsdk.ApplicationRef, host modulehost.Host) (reportsdk.Binding, error) {
	if err := application.Validate(); err != nil {
		return nil, err
	}
	if host == nil || host.Database() == nil || host.Dialect() == nil || host.Migrations() == nil {
		return nil, fmt.Errorf("Report Module persistence host is incomplete")
	}
	migrations, err := reportmigration.Migrations(host.Migrations().Driver(), host.Migrations().Schema())
	if err != nil {
		return nil, err
	}
	if err := host.Migrations().ApplyOwnedMigrations(ctx, reportmigration.MigrationOwner, migrations); err != nil {
		return nil, fmt.Errorf("apply Report Module migrations: %w", err)
	}
	definitionKernel, err := shareddefinition.Open(ctx, application.RuntimeID, host.Database(), host.Dialect(), host.Migrations())
	if err != nil {
		return nil, fmt.Errorf("open Report Definition persistence: %w", err)
	}
	definitions := metadatasdk.AdaptDefinitionStore(definitionKernel)
	return reportadapter.NewBinding(reportapplication.NewService(
		reportpersistence.NewDefinitionStore(host, definitions),
		reportpersistence.NewReportSnapshotStore(host),
	))
}
