package plan

import (
	reportcontract "github.com/domainry/domainry-report-sdk/contract"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

// BuildReportDatasetPlan remains as an internal compatibility seam while the
// deployment-neutral definition contract is single-sourced in the SDK.
func BuildReportDatasetPlan(report reportmodel.ReportSchema) (reportmodel.ReportDatasetPlan, error) {
	return reportcontract.BuildReportDatasetPlan(report)
}
