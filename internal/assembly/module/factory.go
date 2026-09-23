package module

import (
	"context"
	"fmt"

	metadatasdk "github.com/domainry/domainry-metadata-sdk"
	metadatamodulehost "github.com/domainry/domainry-metadata-sdk/modulehost"
	metadatamodule "github.com/domainry/domainry-metadata/module"
	reportsdk "github.com/domainry/domainry-report-sdk"
	"github.com/domainry/domainry-report-sdk/modulehost"
	reportadapter "github.com/domainry/domainry-report/internal/adapter/reportsdk"
	reportapplication "github.com/domainry/domainry-report/internal/application/report"
	reportmigration "github.com/domainry/domainry-report/internal/infrastructure/persistence/database/migration"
	reportpersistence "github.com/domainry/domainry-report/internal/infrastructure/persistence/database/report"
)

type Factory struct{}

func NewFactory() *Factory { return &Factory{} }

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
	if err := host.Migrations().ApplyOwnedMigrations(ctx, "report", migrations); err != nil {
		return nil, fmt.Errorf("apply Report Module migrations: %w", err)
	}
	definitions, err := metadatamodule.OpenDefinitionStore(ctx, metadatasdk.ApplicationRef{InstallationID: application.RuntimeID}, reportMetadataHost{host: host})
	if err != nil {
		return nil, fmt.Errorf("open Report Definition persistence: %w", err)
	}
	return reportadapter.NewBinding(reportapplication.NewService(
		reportpersistence.NewDefinitionStore(host, definitions),
		reportpersistence.NewReportSnapshotStore(host),
	))
}

type reportMetadataHost struct{ host modulehost.Host }

func (h reportMetadataHost) Database() metadatamodulehost.Database { return h.host.Database() }
func (h reportMetadataHost) Dialect() metadatamodulehost.Dialect   { return h.host.Dialect() }
func (h reportMetadataHost) Migrations() metadatamodulehost.MigrationRegistrar {
	return reportMetadataMigrations{registrar: h.host.Migrations()}
}

type reportMetadataMigrations struct{ registrar modulehost.MigrationRegistrar }

func (m reportMetadataMigrations) Driver() string { return m.registrar.Driver() }
func (m reportMetadataMigrations) Schema() string { return m.registrar.Schema() }
func (m reportMetadataMigrations) ApplyOwnedMigrations(ctx context.Context, owner string, migrations []metadatamodulehost.SchemaMigration) error {
	return m.registrar.ApplyOwnedMigrations(ctx, owner, migrations)
}
