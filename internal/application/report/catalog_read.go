package report

import (
	"context"
	"sort"
	"strings"
	"time"

	sdk "github.com/domainry/domainry-report-sdk"
	model "github.com/domainry/domainry-report-sdk/model"
)

func (s *QueryService) catalogReadProof(request model.ReportCatalogRequest, result model.ReportCatalog, definitions []model.ReportSchema, subject model.ReportSubject) string {
	result.ReadProof = ""
	return s.queryProof("report-catalog-read-v1", []any{request, result, definitions, resultReadIdentity(subject)})
}

func (s *QueryService) AuthorizeCatalogRead(ctx context.Context, in model.ReportCatalogReadAuthorization, a model.ReportAuthority) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if s == nil || s.definitions == nil || len(s.cursorKey) == 0 || len(in.Result.Reports) > model.ReportCatalogMaximumSize {
		return reportError(503, "backend.report.catalog_unavailable", nil)
	}
	subject, err := s.resolveSubject(ctx, a)
	if err != nil {
		return err
	}
	if !subject.HasPermission(sdk.ActionReportResultsRead) {
		return reportError(403, "backend.permission.denied", nil)
	}
	definitions, err := s.definitions.ReportDefinitions(ctx)
	if err != nil {
		return normalizeHostError(err, "backend.report.definition_read_failed")
	}
	definitions = append([]model.ReportSchema{}, definitions...)
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Key < definitions[j].Key })
	if !validReadProof(in.Result.ReadProof, s.catalogReadProof(in.Request, in.Result, definitions, subject)) {
		return reportError(409, "backend.report.result_stale_or_invalid", nil)
	}
	// The signature proves the original page and cursor. Current metadata is
	// unchanged, but every disclosed item's audience, sources and fields still
	// require live checks; the old cursor is never used to execute a new query.
	for _, entry := range in.Result.Reports {
		current, definition, err := s.resolve(ctx, entry.Key, a, sdk.ActionReportResultsRead)
		if err != nil {
			return err
		}
		if _, err := s.compileAuthorizedObjectSQL(ctx, reportForDataPermissions(definition, reportDataPermissionKeys(definition)), current); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (s *QueryService) analysisCatalogReadProof(request model.AnalysisCatalogRequest, result model.AnalysisCatalog, datasets []model.AnalysisDataset, subject model.ReportSubject) string {
	result.ReadProof = ""
	return s.queryProof("report-analysis-catalog-read-v1", []any{request, result, datasets, resultReadIdentity(subject)})
}

func (s *QueryService) AuthorizeAnalysisCatalogRead(ctx context.Context, in model.AnalysisCatalogReadAuthorization, a model.ReportAuthority) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if s == nil || len(s.cursorKey) == 0 || len(in.Result.Datasets) > 50 {
		return reportError(503, "backend.report.analysis.unavailable", nil)
	}
	subject, datasets, err := s.analysisDatasetsForAction(ctx, a, sdk.ActionReportResultsRead)
	if err != nil {
		return err
	}
	if key := strings.TrimSpace(in.Request.DatasetKey); key != "" {
		filtered := []model.AnalysisDataset{}
		for _, dataset := range datasets {
			if dataset.Key == key {
				filtered = append(filtered, dataset)
			}
		}
		datasets = filtered
	}
	if !validReadProof(in.Result.ReadProof, s.analysisCatalogReadProof(in.Request, in.Result, datasets, subject)) {
		return reportError(409, "backend.report.analysis.result_stale_or_invalid", nil)
	}
	return ctx.Err()
}
