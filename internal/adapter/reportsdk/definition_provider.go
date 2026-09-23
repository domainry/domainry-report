package reportsdk

import (
	"context"
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
		report := definition.Report
		if strings.TrimSpace(report.Key) == "" {
			return nil, fmt.Errorf("Report definition key is required")
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
		if strings.TrimSpace(definition.Report.Key) != reportKey {
			continue
		}
		for _, control := range definition.ExportControls {
			if strings.TrimSpace(control.ReportKey) != reportKey {
				continue
			}
			for _, source := range control.SourceObjects {
				if strings.TrimSpace(source) == objectKey {
					return control, true, nil
				}
			}
		}
	}
	return reportmodel.ReportExportControlSchema{}, false, nil
}
