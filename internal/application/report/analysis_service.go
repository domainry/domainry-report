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
	results := make([]model.ReportObjectSQLExecutionResult, 0, len(before.queries))
	for _, query := range before.queries {
		if err := ctx.Err(); err != nil {
			return model.AnalysisResult{}, err
		}
		result, err := s.objectSQL.ExecuteReportObjectSQL(ctx, query)
		if err != nil {
			return model.AnalysisResult{}, normalizeHostError(err, "backend.report.analysis.execution_failed")
		}
		results = append(results, result)
	}
	evaluated, err := analysis.Evaluate(before.plan, results)
	if err != nil {
		return model.AnalysisResult{}, analysisError(err)
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
	result := model.AnalysisResult{Spec: after.plan.Spec, Columns: after.plan.Columns, Rows: evaluated.Rows, Methods: after.plan.Methods, Source: model.AnalysisSource{
		DatasetKey: after.plan.Dataset.Key, DefinitionVersion: canonicalHash(after.plan.Dataset), DataVersion: canonicalHash(after.versions),
		QueriedAt: s.clock().UTC().Format(time.RFC3339Nano), InputCounts: evaluated.InputCounts, Scope: "current_subject_filtered_dataset", Complete: true,
	}}
	raw, err := json.Marshal(result)
	if err != nil || len(raw) > analysisMaximumResultBytes {
		return model.AnalysisResult{}, reportError(422, "backend.report.analysis.result_limit_exceeded", err)
	}
	result.Source.Proof = s.analysisResultProof(result, after)
	return result, nil
}

func (s *QueryService) analysisState(ctx context.Context, request model.AnalysisRequest, authority model.ReportAuthority) (analysisState, error) {
	if s == nil || s.objectSQL == nil || len(s.cursorKey) == 0 || s.clock == nil {
		return analysisState{}, reportError(500, "backend.report.analysis.unavailable", nil)
	}
	versions, ok := s.objectSQL.(modulehost.AnalysisSourceVersionReader)
	if !ok {
		return analysisState{}, reportError(500, "backend.report.analysis.source_version_unavailable", nil)
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

func (s *QueryService) analysisSources() (modulehost.AnalysisSources, error) {
	if s == nil || s.objectSQL == nil || len(s.cursorKey) == 0 {
		return nil, reportError(500, "backend.report.analysis.unavailable", nil)
	}
	host, ok := s.objectSQL.(modulehost.AnalysisSources)
	if !ok {
		return nil, reportError(500, "backend.report.analysis.unavailable", nil)
	}
	return host, nil
}

var _ reportsdk.Analyses = (*QueryService)(nil)
