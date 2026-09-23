package report

import (
	"context"
	"encoding/json"
	"testing"

	metadatasdk "github.com/domainry/domainry-metadata-sdk"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
	reportpersistence "github.com/domainry/domainry-report-sdk/persistence"
)

type definitionStoreProbe struct {
	replaced metadatasdk.ProjectionSnapshot
	values   []metadatasdk.Definition
}

func (p *definitionStoreProbe) List(context.Context, metadatasdk.DefinitionQuery) ([]metadatasdk.Definition, error) {
	return append([]metadatasdk.Definition(nil), p.values...), nil
}
func (p *definitionStoreProbe) Get(context.Context, string, string, string) (metadatasdk.Definition, bool, error) {
	return metadatasdk.Definition{}, false, nil
}
func (p *definitionStoreProbe) Snapshot(context.Context, metadatasdk.DefinitionQuery) (metadatasdk.DefinitionSnapshot, error) {
	return metadatasdk.DefinitionSnapshot{Definitions: append([]metadatasdk.Definition(nil), p.values...)}, nil
}
func (p *definitionStoreProbe) ReplaceSourceSnapshot(_ context.Context, snapshot metadatasdk.ProjectionSnapshot) error {
	p.replaced = snapshot
	p.values = append([]metadatasdk.Definition(nil), snapshot.Definitions...)
	for index := range p.values {
		p.values[index].Owner = snapshot.Owner
		p.values[index].SchemaVersion = snapshot.SchemaVersion
		p.values[index].SourceKind = snapshot.SourceKind
		p.values[index].SourceID = snapshot.SourceID
	}
	return nil
}
func (*definitionStoreProbe) Publish(context.Context, metadatasdk.DefinitionPublishCommand) (metadatasdk.DefinitionPublishResult, error) {
	return metadatasdk.DefinitionPublishResult{}, nil
}
func (*definitionStoreProbe) Disable(context.Context, metadatasdk.DefinitionDisableCommand) error {
	return nil
}
func (*definitionStoreProbe) GetVersion(context.Context, metadatasdk.DefinitionVersionQuery) (metadatasdk.DefinitionVersion, bool, error) {
	return metadatasdk.DefinitionVersion{}, false, nil
}

func TestDefinitionStorePacksRelatedConfigurationIntoOneSharedReportDefinition(t *testing.T) {
	snapshotStore := openSnapshotTestStore(t)
	host := snapshotStore.host.(snapshotTestHost)
	shared := &definitionStoreProbe{}
	store := NewDefinitionStore(host, shared)
	definition := reportmodel.ReportDefinitionSchema{
		Report:                 reportmodel.ReportSchema{Key: "sales", Name: "Sales"},
		OperationStateExamples: []reportmodel.ReportOperationStateExampleSchema{{Key: "paid-order", ObjectKey: "order"}},
		SensitiveFieldPolicies: []reportmodel.ReportSensitiveFieldPolicySchema{{Key: "customer-pii", ObjectKey: "customer"}},
		ExportControls:         []reportmodel.ReportExportControlSchema{{Key: "sales-export", ReportKey: "sales", SensitiveFieldPolicyKeys: []string{"customer-pii"}}},
	}
	snapshot := reportpersistence.DefinitionSnapshot{
		SchemaVersion: "1", SourceKind: "generated", SourceID: "office",
		Definitions: []reportmodel.ReportDefinitionSchema{definition},
	}
	if err := store.SyncDefinitions(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	if shared.replaced.Owner != metadatasdk.DefinitionOwnerReport || len(shared.replaced.Definitions) != 1 || shared.replaced.Definitions[0].ResourceType != "report" || shared.replaced.Definitions[0].ResourceKey != "sales" {
		t.Fatalf("shared snapshot=%#v", shared.replaced)
	}
	var payload reportmodel.ReportDefinitionSchema
	if err := json.Unmarshal(shared.replaced.Definitions[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.OperationStateExamples) != 1 || len(payload.SensitiveFieldPolicies) != 1 || len(payload.ExportControls) != 1 {
		t.Fatalf("packed payload=%#v", payload)
	}
	stored, err := store.DefinitionSnapshot(t.Context())
	if err != nil || len(stored.Definitions) != 1 || stored.Definitions[0].Report.Key != "sales" || len(stored.Definitions[0].ExportControls) != 1 {
		t.Fatalf("stored snapshot=%#v err=%v", stored, err)
	}
}

func TestDefinitionStoreRejectsInvalidTypedRelationships(t *testing.T) {
	snapshotStore := openSnapshotTestStore(t)
	host := snapshotStore.host.(snapshotTestHost)
	store := NewDefinitionStore(host, &definitionStoreProbe{})
	for name, definition := range map[string]reportmodel.ReportDefinitionSchema{
		"missing report": {},
		"wrong report control": {
			Report:         reportmodel.ReportSchema{Key: "sales"},
			ExportControls: []reportmodel.ReportExportControlSchema{{Key: "export", ReportKey: "other"}},
		},
		"unknown sensitive policy": {
			Report:         reportmodel.ReportSchema{Key: "sales"},
			ExportControls: []reportmodel.ReportExportControlSchema{{Key: "export", ReportKey: "sales", SensitiveFieldPolicyKeys: []string{"missing"}}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := store.SyncDefinitions(t.Context(), reportpersistence.DefinitionSnapshot{
				SchemaVersion: "1", SourceKind: "test", SourceID: "test", Definitions: []reportmodel.ReportDefinitionSchema{definition},
			})
			if err == nil {
				t.Fatal("invalid typed Report definition was accepted")
			}
		})
	}
}

var _ metadatasdk.DefinitionStore = (*definitionStoreProbe)(nil)
