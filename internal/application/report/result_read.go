package report

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	sdk "github.com/domainry/domainry-report-sdk"
	model "github.com/domainry/domainry-report-sdk/model"
	"github.com/domainry/domainry-report-sdk/modulehost"
)

func (s *QueryService) resultReadScope(ctx context.Context, report model.ReportSchema, subject model.ReportSubject) (string, error) {
	reader, ok := s.objectSQL.(modulehost.ResultReadScopeReader)
	if !ok {
		return "", nil
	}
	scope, err := reader.ReadReportResultScope(ctx, report, subject)
	if err != nil {
		return "", normalizeHostError(err, "backend.report.read_scope_unavailable")
	}
	decoded, err := hex.DecodeString(scope)
	if err != nil || len(decoded) != sha256.Size {
		return "", reportError(503, "backend.report.read_scope_unavailable", nil)
	}
	return scope, nil
}

// The current source owner attests the actual data projection independently
// of execution grants. Identity remains bound to the original reader until
// an explicit cross-subject sharing contract is implemented.
func resultReadIdentity(subject model.ReportSubject) string {
	return canonicalHash([]any{subject.Principal.WorkspaceID, subject.Principal.UserID, subject.TrustedProcess})
}

func validReadProof(proof, expected string) bool {
	return len(proof) == sha256.Size*2 && hmac.Equal([]byte(proof), []byte(expected))
}

func (s *QueryService) AuthorizeQueryResultRead(ctx context.Context, in model.ReportQueryResultAuthorization, a model.ReportAuthority) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	state, err := s.queryEvidenceStateForAction(ctx, in.Query.ReportKey, a, sdk.ActionReportResultsRead)
	if err != nil {
		return err
	}
	if state.readScope == "" {
		return reportError(503, "backend.report.read_scope_unavailable", nil)
	}
	query, err := normalizedEvidenceQuery(state.report, in.Query)
	if err != nil {
		return err
	}
	if !validReadProof(in.Result.Source.ReadProof, s.queryResultReadProof(query, in.Result, state)) {
		return reportError(409, "backend.report.result_stale_or_invalid", nil)
	}
	return ctx.Err()
}

func (s *QueryService) queryResultReadProof(query model.ReportObjectSQLRequest, result model.ReportQueryResult, state queryEvidenceState) string {
	result.Source.ReadProof = ""
	return s.queryProof("report-query-read-v1", []any{query, result, state.report, state.plan, state.version, state.readScope, resultReadIdentity(state.subject)})
}

func (s *QueryService) AuthorizeAnalysisResultRead(ctx context.Context, in model.AnalysisResultAuthorization, a model.ReportAuthority) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := json.Marshal(in.Result)
	if err != nil || len(raw) > analysisMaximumResultBytes+256 {
		return reportError(400, "backend.report.analysis.result_invalid", err)
	}
	state, err := s.analysisStateForAction(ctx, in.Request, a, sdk.ActionReportResultsRead)
	if err != nil {
		return err
	}
	if state.readScope == "" {
		return reportError(503, "backend.report.read_scope_unavailable", nil)
	}
	if !validReadProof(in.Result.Source.ReadProof, s.analysisResultReadProof(in.Result, state)) {
		return reportError(409, "backend.report.analysis.result_stale_or_invalid", nil)
	}
	return ctx.Err()
}

func (s *QueryService) analysisResultReadProof(result model.AnalysisResult, state analysisState) string {
	result.Source.ReadProof = ""
	plans := make([]model.ReportObjectSQLPlan, 0, len(state.queries))
	for _, query := range state.queries {
		plans = append(plans, query.Plan)
	}
	return s.queryProof("report-analysis-read-v1", []any{result, state.plan.Dataset, state.plan.Spec, plans, state.versions, state.table, state.readScope, resultReadIdentity(state.subject)})
}

var _ sdk.ResultReader = (*QueryService)(nil)
