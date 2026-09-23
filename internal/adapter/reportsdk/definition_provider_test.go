package reportsdk

import (
	"context"
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
	provider := storedDefinitionProvider{repository: &definitionProviderRepository{snapshot: reportpersistence.DefinitionSnapshot{Definitions: []reportmodel.ReportDefinitionSchema{{
		Report: report, ExportControls: []reportmodel.ReportExportControlSchema{control},
	}}}}}
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
