package module

import (
	"context"
	"fmt"
	"strings"

	reportsdk "github.com/domainry/domainry-report-sdk"
	"github.com/domainry/domainry-report-sdk/modulehost"
	reportrepository "github.com/domainry/domainry-report-sdk/repository"
	reportpersistence "github.com/domainry/domainry-report/internal/persistence"
)

type Factory struct{}

func NewFactory() *Factory { return &Factory{} }

func (*Factory) OpenModule(ctx context.Context, application reportsdk.ApplicationRef, host modulehost.Host) (reportsdk.Binding, error) {
	if strings.TrimSpace(application.RuntimeID) == "" {
		return nil, fmt.Errorf("Report runtime identity is required")
	}
	if host == nil || host.Database() == nil || host.Dialect() == nil || host.Migrations() == nil {
		return nil, fmt.Errorf("Report Module persistence host is incomplete")
	}
	migrations, err := reportpersistence.SchemaMigrations(host.Migrations().Driver(), host.Migrations().Schema())
	if err != nil {
		return nil, err
	}
	if err := host.Migrations().ApplyOwnedMigrations(ctx, "report", migrations); err != nil {
		return nil, fmt.Errorf("apply Report Module migrations: %w", err)
	}
	return binding{definitions: reportpersistence.NewDefinitionStore(host.Database(), host.Dialect())}, nil
}

type binding struct {
	definitions reportrepository.DefinitionRepository
}

func (binding) Descriptor() reportsdk.Descriptor {
	return reportsdk.Descriptor{ProtocolVersion: reportsdk.ProtocolVersionV1, Mode: "module"}
}
func (binding) Close(context.Context) error                                   { return nil }
func (b binding) DefinitionRepository() reportrepository.DefinitionRepository { return b.definitions }

var _ reportrepository.Binding = binding{}
