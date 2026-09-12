package report

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	reportsdk "github.com/domainry/domainry-report-sdk"
	model "github.com/domainry/domainry-report-sdk/model"
	"github.com/domainry/domainry-report-sdk/modulehost"
	analysis "github.com/domainry/domainry-report/internal/domain/report/service/analysis"
	objectsql "github.com/domainry/domainry-report/internal/domain/report/service/objectsql"
)

const analysisMaximumResultBytes = 2 << 20

type analysisState struct {
	plan     analysis.Plan
	subject  model.ReportSubject
	queries  []model.ReportObjectSQLExecutionRequest
	versions []model.ReportSnapshotSourceVersion
	table    *modulehost.AnalysisTableVersion
}

// RunAnalysis compiles a closed specification, executes every aggregation at
// the data owner, then checks the same current subject, metadata and source
// versions again. Validation alone never returns an analysis result.
func (s *QueryService) RunAnalysis(ctx context.Context, request model.AnalysisRequest, authority model.ReportAuthority) (model.AnalysisResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	before, err := s.analysisState(ctx, request, authority)
	if err != nil {
		return model.AnalysisResult{}, err
	}
	beforeFingerprint := before.fingerprint()
	var evaluated analysis.Evaluation
	if before.table != nil {
		evaluated, err = s.evaluateAnalysisTable(ctx, before)
	} else {
		evaluated, err = s.evaluateAnalysisObjects(ctx, before)
	}
	if err != nil {
		return model.AnalysisResult{}, err
	}
	after, err := s.analysisState(ctx, before.plan.Spec, authority)
	if err != nil {
		return model.AnalysisResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return model.AnalysisResult{}, err
	}
	if beforeFingerprint != after.fingerprint() {
		return model.AnalysisResult{}, reportError(409, "backend.report.analysis.source_changed", nil)
	}
	dataVersion := canonicalHash(after.versions)
	if after.table != nil {
		dataVersion = canonicalHash(after.table)
	}
	visualization, coverage, references := analysis.DescribeResult(after.plan, evaluated)
	result := model.AnalysisResult{Spec: after.plan.Spec, Columns: after.plan.Columns, Rows: evaluated.Rows, Methods: after.plan.Methods, Source: model.AnalysisSource{
		DatasetKey: after.plan.Dataset.Key, DefinitionVersion: canonicalHash(after.plan.Dataset), DataVersion: dataVersion,
		QueriedAt: s.clock().UTC().Format(time.RFC3339Nano), InputCounts: evaluated.InputCounts, Scope: "current_subject_filtered_dataset", Complete: true,
	}, Visualization: visualization, Coverage: coverage, References: references}
	raw, err := json.Marshal(result)
	if err != nil || len(raw) > analysisMaximumResultBytes {
		return model.AnalysisResult{}, reportError(422, "backend.report.analysis.result_limit_exceeded", err)
	}
	result.Source.Proof = s.analysisResultProof(result, after)
	return result, nil
}

func (s *QueryService) evaluateAnalysisObjects(ctx context.Context, before analysisState) (analysis.Evaluation, error) {
	results := make([]model.ReportObjectSQLExecutionResult, 0, len(before.queries))
	for _, query := range before.queries {
		if err := ctx.Err(); err != nil {
			return analysis.Evaluation{}, err
		}
		result, err := s.objectSQL.ExecuteReportObjectSQL(ctx, query)
		if err != nil {
			return analysis.Evaluation{}, normalizeHostError(err, "backend.report.analysis.execution_failed")
		}
		results = append(results, result)
	}
	evaluated, err := analysis.Evaluate(before.plan, results)
	if err != nil {
		return analysis.Evaluation{}, analysisError(err)
	}
	return evaluated, nil
}

func (s *QueryService) analysisState(ctx context.Context, request model.AnalysisRequest, authority model.ReportAuthority) (analysisState, error) {
	if s == nil || len(s.cursorKey) == 0 || s.clock == nil {
		return analysisState{}, reportError(500, "backend.report.analysis.unavailable", nil)
	}
	subject, datasets, err := s.analysisDatasets(ctx, authority)
	if err != nil {
		return analysisState{}, err
	}
	var dataset model.AnalysisDataset
	for _, candidate := range datasets {
		if candidate.Key == request.DatasetKey {
			dataset = candidate
			break
		}
	}
	if dataset.Key == "" {
		return analysisState{}, reportError(404, "backend.report.analysis.dataset_not_found", nil)
	}
	plan, err := analysis.Compile(request, dataset)
	if err != nil {
		return analysisState{}, analysisError(err)
	}
	state := analysisState{plan: plan, subject: subject}
	if dataset.Kind == "table_file" {
		return s.analysisTableState(ctx, state)
	}
	versions, ok := s.objectSQL.(modulehost.AnalysisSourceVersionReader)
	if !ok {
		return analysisState{}, reportError(500, "backend.report.analysis.source_version_unavailable", nil)
	}
	for _, query := range plan.Queries {
		compiled, err := s.compileAuthorizedObjectSQL(ctx, query.Report, subject)
		if err != nil {
			return analysisState{}, err
		}
		parameters, err := objectsql.NormalizeDeclaredParameters(compiled.Parameters, query.Parameters)
		if err != nil {
			return analysisState{}, objectSQLApplicationError(err)
		}
		version, err := versions.ReadReportAnalysisSourceVersion(ctx, query.Report, subject)
		if err != nil {
			return analysisState{}, normalizeHostError(err, "backend.report.analysis.source_version_failed")
		}
		if len(version.SourceVersions) == 0 {
			return analysisState{}, reportError(500, "backend.report.analysis.source_version_unavailable", nil)
		}
		for _, v := range version.SourceVersions {
			if v == "" {
				return analysisState{}, reportError(500, "backend.report.analysis.source_version_unavailable", nil)
			}
		}
		state.versions = append(state.versions, version)
		// No PageSize/continuation is supplied: LIMIT bounds aggregate output,
		// while host SQL must aggregate the entire authorized input relation.
		state.queries = append(state.queries, model.ReportObjectSQLExecutionRequest{Report: query.Report, Plan: compiled, Parameters: parameters, Subject: subject, Timeout: 30 * time.Second})
	}
	return state, nil
}

func analysisError(err error) error {
	var failure *analysis.Error
	if !errors.As(err, &failure) {
		return reportError(500, "backend.report.analysis.failed", err)
	}
	status := 400
	if failure.Code == "result_invalid" {
		status = 502
	}
	if failure.Code == "result_limit_exceeded" || failure.Code == "arithmetic_limit" {
		status = 422
	}
	return reportError(status, "backend.report.analysis."+failure.Code, err)
}

type analysisCatalogSource struct {
	source modulehost.AnalysisSources
	kind   string
}

func (s *QueryService) analysisSources() ([]analysisCatalogSource, error) {
	if s == nil || len(s.cursorKey) == 0 {
		return nil, reportError(500, "backend.report.analysis.unavailable", nil)
	}
	sources := []analysisCatalogSource{}
	if host, ok := s.objectSQL.(modulehost.AnalysisSources); ok {
		sources = append(sources, analysisCatalogSource{source: host, kind: "business_object"})
	}
	if s.tables != nil {
		sources = append(sources, analysisCatalogSource{source: s.tables, kind: "table_file"})
	}
	if len(sources) == 0 {
		return nil, reportError(500, "backend.report.analysis.unavailable", nil)
	}
	return sources, nil
}

var _ reportsdk.Analyses = (*QueryService)(nil)
