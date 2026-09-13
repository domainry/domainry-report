package report

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	reportsdk "github.com/domainry/domainry-report-sdk"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
	reportobjectsql "github.com/domainry/domainry-report/internal/domain/report/service/objectsql"
)

type queryEvidenceState struct {
	report    reportmodel.ReportSchema
	subject   reportmodel.ReportSubject
	plan      reportmodel.ReportObjectSQLPlan
	version   string
	readScope string
}

// Query returns evidence only after the existing owner has executed the
// published query and the surrounding source/authorization state is stable.
func (s *QueryService) Query(ctx context.Context, request reportmodel.ReportObjectSQLRequest, authority reportmodel.ReportAuthority) (reportmodel.ReportQueryResult, error) {
	before, err := s.queryEvidenceState(ctx, request.ReportKey, authority)
	if err != nil {
		return reportmodel.ReportQueryResult{}, err
	}
	request, err = normalizedEvidenceQuery(before.report, request)
	if err != nil {
		return reportmodel.ReportQueryResult{}, err
	}
	summary, err := s.QueryObjectSQL(ctx, request, authority)
	if err != nil {
		return reportmodel.ReportQueryResult{}, err
	}
	after, err := s.queryEvidenceState(ctx, request.ReportKey, authority)
	if err != nil {
		return reportmodel.ReportQueryResult{}, err
	}
	if before.fingerprint() != after.fingerprint() || before.readScope != after.readScope {
		return reportmodel.ReportQueryResult{}, reportError(409, "backend.report.source_changed", nil)
	}
	out := reportmodel.ReportQueryResult{Summary: summary, Source: reportmodel.ReportQuerySource{
		ReportKey: after.report.Key, DefinitionVersion: canonicalHash(after.report), DataVersion: after.version,
		QueriedAt: s.clock().UTC().Format(time.RFC3339Nano), RowLimit: after.plan.Limit,
		Complete: request.Page.Cursor == "" && !summary.Truncated,
	}}
	out.Source.Proof = s.resultProof(request, out, after)
	if after.readScope != "" {
		out.Source.ReadProof = s.queryResultReadProof(request, out, after)
	}
	return out, nil
}

func (s *QueryService) AuthorizeQueryResult(ctx context.Context, request reportmodel.ReportQueryResultAuthorization, authority reportmodel.ReportAuthority) error {
	state, err := s.queryEvidenceState(ctx, request.Query.ReportKey, authority)
	if err != nil {
		return err
	}
	query, err := normalizedEvidenceQuery(state.report, request.Query)
	if err != nil {
		return err
	}
	proof := request.Result.Source.Proof
	if len(proof) != sha256.Size*2 || !hmac.Equal([]byte(proof), []byte(s.resultProof(query, request.Result, state))) {
		return reportError(409, "backend.report.result_stale_or_invalid", nil)
	}
	return nil
}

func (s *QueryService) queryEvidenceState(ctx context.Context, key string, authority reportmodel.ReportAuthority) (queryEvidenceState, error) {
	return s.queryEvidenceStateForAction(ctx, key, authority, reportsdk.ActionReportQueryExecute)
}

func (s *QueryService) queryEvidenceStateForAction(ctx context.Context, key string, authority reportmodel.ReportAuthority, action string) (queryEvidenceState, error) {
	if s == nil || s.sourceVersions == nil || len(s.cursorKey) == 0 || s.clock == nil {
		return queryEvidenceState{}, reportError(500, "backend.report.evidence_unavailable", nil)
	}
	subject, definition, err := s.resolve(ctx, key, authority, action)
	if err != nil {
		return queryEvidenceState{}, err
	}
	scoped := reportForDataPermissions(definition, reportDataPermissionKeys(definition))
	plan, err := s.compileAuthorizedObjectSQL(ctx, scoped, subject)
	if err != nil {
		return queryEvidenceState{}, err
	}
	version, err := s.sourceVersions.ReadReportSourceVersion(ctx, scoped, subject)
	if err != nil {
		return queryEvidenceState{}, normalizeHostError(err, "backend.report.source_version_failed")
	}
	readScope, err := s.resultReadScope(ctx, scoped, subject)
	if err != nil {
		return queryEvidenceState{}, err
	}
	return queryEvidenceState{report: definition, subject: subject, plan: plan, version: canonicalHash(version), readScope: readScope}, nil
}

func normalizedEvidenceQuery(report reportmodel.ReportSchema, request reportmodel.ReportObjectSQLRequest) (reportmodel.ReportObjectSQLRequest, error) {
	if report.ObjectSQLV1 == nil {
		return reportmodel.ReportObjectSQLRequest{}, reportError(400, "backend.report.object_sql_not_enabled", nil)
	}
	parameters, err := reportobjectsql.NormalizeParameters(report.ObjectSQLV1.Parameters, request.Parameters)
	if err != nil {
		return reportmodel.ReportObjectSQLRequest{}, objectSQLApplicationError(err)
	}
	request.ReportKey, request.Parameters = report.Key, parameters
	request.Page.Cursor = strings.TrimSpace(request.Page.Cursor)
	if request.Page.PageSize == 0 {
		request.Page.PageSize = reportmodel.ReportPageDefaultSize
	}
	if request.Page.PageSize < 1 || request.Page.PageSize > reportmodel.ReportPageMaximumSize || len(request.Page.Cursor) > 16384 {
		return reportmodel.ReportObjectSQLRequest{}, reportError(400, "backend.report.page_size_invalid", nil)
	}
	return request, nil
}

func (s queryEvidenceState) fingerprint() string {
	return canonicalHash(struct {
		Definition reportmodel.ReportSchema
		Scope      string
		Version    string
	}{s.report, reportSnapshotAccessScopeHash(s.subject, reportDataPermissionKeys(s.report)), s.version})
}

func (s *QueryService) resultProof(query reportmodel.ReportObjectSQLRequest, result reportmodel.ReportQueryResult, state queryEvidenceState) string {
	result.Source.Proof = ""
	result.Source.ReadProof = ""
	return s.queryProof("report-query-result-v1", struct {
		Query       reportmodel.ReportObjectSQLRequest
		Result      reportmodel.ReportQueryResult
		Fingerprint string
	}{query, result, state.fingerprint()})
}

func (s *QueryService) queryProof(domain string, value any) string {
	content, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write([]byte(domain + "\x00"))
	_, _ = mac.Write(content)
	return hex.EncodeToString(mac.Sum(nil))
}

var _ reportsdk.GovernedQueries = (*QueryService)(nil)
