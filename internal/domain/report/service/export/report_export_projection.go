package export

import reportmodel "github.com/domainry/domainry-report-sdk/model"

func Rows(summary reportmodel.ReportSummary, analysisKey string) ([]reportmodel.ReportResultRow, error) {
	if analysisKey == "" {
		return summary.Rows, nil
	}
	for _, analysis := range summary.Analyses {
		if analysis.Key == analysisKey {
			return analysis.Rows, nil
		}
	}
	return nil, exportScopeError("backend.report.export_analysis_not_allowed")
}
