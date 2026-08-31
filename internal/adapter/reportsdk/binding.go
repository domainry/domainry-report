package reportsdk

import (
	"context"

	sdk "github.com/domainry/domainry-report-sdk"
	reportpersistence "github.com/domainry/domainry-report-sdk/persistence"
	reportapplication "github.com/domainry/domainry-report/internal/application/report"
)

type Binding struct {
	service reportapplication.Service
}

func NewBinding(service reportapplication.Service) Binding {
	return Binding{service: service}
}

func (Binding) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{ProtocolVersion: sdk.ProtocolVersionV2, Mode: sdk.DeploymentModeModule, Capabilities: []string{"definitions.sync", "snapshots.manage"}}
}

func (Binding) Close(context.Context) error { return nil }

func (b Binding) Definitions() reportpersistence.DefinitionRepository { return b.service.Definitions() }

func (b Binding) Snapshots() reportpersistence.SnapshotRepository { return b.service.Snapshots() }

// DefinitionRepository preserves repository.Binding compatibility.
func (b Binding) DefinitionRepository() reportpersistence.DefinitionRepository {
	return b.service.Definitions()
}

var _ sdk.Binding = Binding{}
var _ reportpersistence.Binding = Binding{}
