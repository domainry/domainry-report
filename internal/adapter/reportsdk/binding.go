package reportsdk

import (
	"context"
	"fmt"
	"sync"

	actioncontract "github.com/domainry/domainry-foundation/action"
	"github.com/domainry/domainry-foundation/modulecapability"
	"github.com/domainry/domainry-foundation/modulehttp"
	sdk "github.com/domainry/domainry-report-sdk"
	"github.com/domainry/domainry-report-sdk/modulehost"
	reportpersistence "github.com/domainry/domainry-report-sdk/persistence"
	reportapplication "github.com/domainry/domainry-report/internal/application/report"
)

type Binding struct {
	service    reportapplication.Service
	mu         sync.RWMutex
	queries    sdk.Queries
	snapshots  sdk.SnapshotCommands
	exports    sdk.Exports
	adapters   []modulehttp.Adapter
	capability modulecapability.Binding
}

func NewBinding(service reportapplication.Service, capability modulecapability.Binding) (*Binding, error) {
	if capability == nil {
		return nil, fmt.Errorf("Report capability binding is required")
	}
	return &Binding{service: service, capability: capability}, nil
}

func (b *Binding) CapabilitySummary(ctx context.Context) (modulecapability.ModuleSummary, error) {
	return b.capability.CapabilitySummary(ctx)
}
func (b *Binding) CapabilityCategory(ctx context.Context, key string) (modulecapability.CategoryDocument, error) {
	return b.capability.CapabilityCategory(ctx, key)
}
func (b *Binding) ValidateCapabilityCandidate(ctx context.Context, request modulecapability.ValidationRequest) (modulecapability.ValidationResult, error) {
	return b.capability.ValidateCapabilityCandidate(ctx, request)
}

func (*Binding) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{ProtocolVersion: sdk.ProtocolVersionV3, Mode: sdk.DeploymentModeModule, Capabilities: []string{
		sdk.CapabilityDefinitionsSync, sdk.CapabilityQueriesExecute, sdk.CapabilitySnapshotsManage, sdk.CapabilityExportsManage, sdk.CapabilityHTTPAdapter,
	}}
}

func (*Binding) Close(context.Context) error { return nil }

func (b *Binding) Definitions() reportpersistence.DefinitionRepository {
	return b.service.Definitions()
}

func (b *Binding) Snapshots() reportpersistence.SnapshotRepository { return b.service.Snapshots() }

// DefinitionRepository preserves repository.Binding compatibility.
func (b *Binding) DefinitionRepository() reportpersistence.DefinitionRepository {
	return b.service.Definitions()
}

func (b *Binding) BindApplicationHost(host modulehost.ApplicationHost) error {
	if host == nil || host.ReportSubjects() == nil || host.ReportDatasets() == nil || host.ReportObjectSQL() == nil || host.ReportSourceVersions() == nil || host.ReportExecutionAudit() == nil || host.ReportExportAuthorization() == nil || host.ReportSnapshotTerminals() == nil || host.ReportExports() == nil || len(host.ReportCursorSigningKey()) == 0 {
		return fmt.Errorf("Report application host is incomplete")
	}
	definitions := storedDefinitionProvider{repository: b.service.Definitions()}
	queries := reportapplication.NewQueryService(host, definitions, b.service.Snapshots())
	snapshots := reportapplication.NewSnapshotService(queries, b.service.Snapshots(), host.ReportSnapshotTerminals(), host.ReportClock())
	exports := reportapplication.NewExportService(queries, definitions, host.ReportExportAuthorization(), host.ReportExports())
	adapter, err := newReportHTTPAdapter(b)
	if err != nil {
		return fmt.Errorf("build Report HTTP adapter: %w", err)
	}
	b.mu.Lock()
	b.queries = queries
	b.snapshots = snapshots
	b.exports = exports
	b.adapters = []modulehttp.Adapter{adapter}
	b.mu.Unlock()
	return nil
}

func (*Binding) AuthorizationActions() ([]actioncontract.ActionDefinition, error) {
	return reportapplication.AuthorizationActions()
}

func (b *Binding) Exports() sdk.Exports {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.exports
}

func (b *Binding) SnapshotCommands() sdk.SnapshotCommands {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.snapshots
}

func (b *Binding) Queries() sdk.Queries {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.queries
}

var _ sdk.Binding = (*Binding)(nil)
var _ sdk.ApplicationHostBinder = (*Binding)(nil)
var _ sdk.ApplicationBinding = (*Binding)(nil)
var _ reportpersistence.Binding = (*Binding)(nil)
var _ actioncontract.Provider = (*Binding)(nil)
