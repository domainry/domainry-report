package reportsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	reportmodel "github.com/domainry/domainry-report-sdk/model"
	reportpersistence "github.com/domainry/domainry-report-sdk/persistence"
)

// storedDefinitionProvider projects Report-owned persisted definitions into
// the application model. Runtime may publish a manifest snapshot through the
// persistence SDK, but it is not an alternate read authority.
type storedDefinitionProvider struct {
	repository reportpersistence.DefinitionRepository
}

func (p storedDefinitionProvider) ReportDefinitions(ctx context.Context) ([]reportmodel.ReportSchema, error) {
	if p.repository == nil {
		return nil, fmt.Errorf("Report definition repository is unavailable")
	}
	snapshot, err := p.repository.DefinitionSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	reports := make([]reportmodel.ReportSchema, 0, len(snapshot.Definitions))
	for _, definition := range snapshot.Definitions {
		if strings.TrimSpace(definition.ResourceType) != "report" {
			continue
		}
		var report reportmodel.ReportSchema
		if err := json.Unmarshal(definition.Payload, &report); err != nil {
			return nil, fmt.Errorf("decode Report definition %q: %w", definition.Key, err)
		}
		if strings.TrimSpace(report.Key) == "" || strings.TrimSpace(report.Key) != strings.TrimSpace(definition.Key) {
			return nil, fmt.Errorf("Report definition %q has inconsistent payload identity", definition.Key)
		}
		reports = append(reports, report)
	}
	return reports, nil
}

func (p storedDefinitionProvider) ReportExportControl(ctx context.Context, reportKey, objectKey string) (reportmodel.ReportExportControlSchema, bool, error) {
	if p.repository == nil {
		return reportmodel.ReportExportControlSchema{}, false, fmt.Errorf("Report definition repository is unavailable")
	}
	snapshot, err := p.repository.DefinitionSnapshot(ctx)
	if err != nil {
		return reportmodel.ReportExportControlSchema{}, false, err
	}
	reportKey, objectKey = strings.TrimSpace(reportKey), strings.TrimSpace(objectKey)
	for _, definition := range snapshot.Definitions {
		if strings.TrimSpace(definition.ResourceType) != "report_export_control" {
			continue
		}
		var control reportmodel.ReportExportControlSchema
		if err := json.Unmarshal(definition.Payload, &control); err != nil {
			return reportmodel.ReportExportControlSchema{}, false, fmt.Errorf("decode Report export control %q: %w", definition.Key, err)
		}
		if strings.TrimSpace(control.Key) == "" || strings.TrimSpace(control.Key) != strings.TrimSpace(definition.Key) {
			return reportmodel.ReportExportControlSchema{}, false, fmt.Errorf("Report export control %q has inconsistent payload identity", definition.Key)
		}
		if strings.TrimSpace(control.ReportKey) != reportKey {
			continue
		}
		for _, source := range control.SourceObjects {
			if strings.TrimSpace(source) == objectKey {
				return control, true, nil
			}
		}
	}
	return reportmodel.ReportExportControlSchema{}, false, nil
}
