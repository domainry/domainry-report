package module

import (
	"context"
	"fmt"

	reportsdk "github.com/domainry/domainry-report-sdk"
	"github.com/domainry/domainry-report-sdk/modulehost"
	reportcapability "github.com/domainry/domainry-report/capability"
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
	capability, err := reportcapability.Open(reportcapability.Inputs{})
	if err != nil {
		return nil, fmt.Errorf("build Report capability disclosure: %w", err)
	}
	return reportadapter.NewBinding(reportapplication.NewService(
		reportpersistence.NewDefinitionStore(host.Database(), host.Dialect()),
		reportpersistence.NewReportSnapshotStore(host),
	), capability)
}
