package report

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"time"

	model "github.com/domainry/domainry-report-sdk/model"
	"github.com/domainry/domainry-report-sdk/modulehost"
)

// AuthorizeAnalysisResult never reexecutes historical analysis. It validates
// current source/field permissions and versions, then verifies the full result
// and normalized specification against an owner-issued proof.
func (s *QueryService) AuthorizeAnalysisResult(ctx context.Context, request model.AnalysisResultAuthorization, authority model.ReportAuthority) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := json.Marshal(request.Result)
	if err != nil || len(raw) > analysisMaximumResultBytes+256 {
		return reportError(400, "backend.report.analysis.result_invalid", err)
	}
	state, err := s.analysisState(ctx, request.Request, authority)
	if err != nil {
		return err
	}
	proof := request.Result.Source.Proof
	if len(proof) != sha256.Size*2 || !hmac.Equal([]byte(proof), []byte(s.analysisResultProof(request.Result, state))) {
		return reportError(409, "backend.report.analysis.result_stale_or_invalid", nil)
	}
	return ctx.Err()
}

func (s analysisState) fingerprint() string {
	plans := make([]model.ReportObjectSQLPlan, 0, len(s.queries))
	for _, query := range s.queries {
		plans = append(plans, query.Plan)
	}
	return canonicalHash(struct {
		Dataset  model.AnalysisDataset
		Spec     model.AnalysisRequest
		Plans    []model.ReportObjectSQLPlan
		Versions []model.ReportSnapshotSourceVersion
		Scope    string
		Table    *modulehost.AnalysisTableVersion `json:",omitempty"`
	}{s.plan.Dataset, s.plan.Spec, plans, s.versions, reportSnapshotAccessScopeHash(s.subject, []string{s.plan.Dataset.Key + ".read"}), s.table})
}

func (s *QueryService) analysisResultProof(result model.AnalysisResult, state analysisState) string {
	result.Source.Proof = ""
	result.Source.ReadProof = ""
	return s.queryProof("report-analysis-result-v1", struct {
		Result      model.AnalysisResult
		Fingerprint string
	}{result, state.fingerprint()})
}
