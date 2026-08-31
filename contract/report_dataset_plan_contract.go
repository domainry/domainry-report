// Package contract preserves the public dataset-plan contract while its
// implementation remains source-owned inside Report's domain layer.
package contract

import (
	reportmodel "github.com/domainry/domainry-report-sdk/model"
	reportplan "github.com/domainry/domainry-report/internal/domain/report/service/plan"
)

func BuildReportDatasetPlan(report reportmodel.ReportSchema) (reportmodel.ReportDatasetPlan, error) {
	return reportplan.BuildReportDatasetPlan(report)
}
