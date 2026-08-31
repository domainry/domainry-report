package reportsdk

import (
	"context"
	"encoding/json"
	"testing"

	reportmodel "github.com/domainry/domainry-report-sdk/model"
	reportpersistence "github.com/domainry/domainry-report-sdk/persistence"
)

type definitionProviderRepository struct {
	snapshot reportpersistence.DefinitionSnapshot
}

func (*definitionProviderRepository) SyncDefinitions(context.Context, reportpersistence.DefinitionSnapshot) error {
	return nil
}

func (r *definitionProviderRepository) DefinitionSnapshot(context.Context) (reportpersistence.DefinitionSnapshot, error) {
	return r.snapshot, nil
}

func TestStoredDefinitionProviderIsReportReadAuthority(t *testing.T) {
	report := reportmodel.ReportSchema{Key: "sales", Name: "Sales"}
	control := reportmodel.ReportExportControlSchema{Key: "sales-export", ReportKey: "sales", SourceObjects: []string{"order"}}
	reportJSON, _ := json.Marshal(report)
	controlJSON, _ := json.Marshal(control)
	provider := storedDefinitionProvider{repository: &definitionProviderRepository{snapshot: reportpersistence.DefinitionSnapshot{Definitions: []reportpersistence.Definition{
		{ResourceType: "report", Key: report.Key, Payload: reportJSON},
		{ResourceType: "report_export_control", Key: control.Key, Payload: controlJSON},
		{ResourceType: "sensitive_field_policy", Key: "ignored", Payload: json.RawMessage(`{"key":"ignored"}`)},
	}}}}
	reports, err := provider.ReportDefinitions(t.Context())
	if err != nil || len(reports) != 1 || reports[0].Key != report.Key {
		t.Fatalf("reports=%#v err=%v", reports, err)
	}
	got, found, err := provider.ReportExportControl(t.Context(), report.Key, "order")
	if err != nil || !found || got.Key != control.Key {
		t.Fatalf("control=%#v found=%v err=%v", got, found, err)
	}
	if _, found, err := provider.ReportExportControl(t.Context(), report.Key, "customer"); err != nil || found {
		t.Fatalf("unexpected control found=%v err=%v", found, err)
	}
}

var _ reportpersistence.DefinitionRepository = (*definitionProviderRepository)(nil)
