// Package contract preserves the public dataset-plan contract while its
// implementation remains source-owned inside Report's domain layer.
package contract

import (
	reportsdkcontract "github.com/domainry/domainry-report-sdk/contract"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

func BuildReportDatasetPlan(report reportmodel.ReportSchema) (reportmodel.ReportDatasetPlan, error) {
	return reportsdkcontract.BuildReportDatasetPlan(report)
}
