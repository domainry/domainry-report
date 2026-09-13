package report

import (
	"testing"

	sdk "github.com/domainry/domainry-report-sdk"
	model "github.com/domainry/domainry-report-sdk/model"
)

func TestAnalysisTableIndependentReadRequiresCurrentWholeSource(t *testing.T) {
	s, host, a, request := tableOwnerFixture()
	saved, err := s.RunAnalysis(t.Context(), request, a)
	if err != nil || saved.Source.ReadProof == "" {
		t.Fatal(saved, err)
	}
	read := model.AnalysisResultAuthorization{Request: request, Result: saved}
	subject := applicationTestSubjectWithPermissions(sdk.ActionReportResultsRead)
	subject.AccessScopeHash = "new-authorization-revision-without-execution"
	s.subjects = applicationTestSubjects{subject: subject}
	if err := s.AuthorizeAnalysisResultRead(t.Context(), read, a); err != nil {
		t.Fatal("table reading required execution", err)
	}
	if err := s.AuthorizeAnalysisResult(t.Context(), read, a); err == nil {
		t.Fatal("table reading granted old execution replay")
	}
	if _, err := s.RunAnalysis(t.Context(), request, a); err == nil {
		t.Fatal("table reading executed analysis")
	}
	if host.streamed != 1 {
		t.Fatal("independent reading reopened table stream")
	}
	host.denied = true
	if err := s.AuthorizeAnalysisResultRead(t.Context(), read, a); err == nil {
		t.Fatal("revoked file remained readable")
	}
	host.denied = false
	changed := "0.02"
	original := host.rows[0]["amount"]
	host.rows[0]["amount"] = &changed
	if err := s.AuthorizeAnalysisResultRead(t.Context(), read, a); err == nil {
		t.Fatal("changed cell retained old whole-source proof")
	}
	host.rows[0]["amount"] = original
	version := host.version.DataVersion
	host.version.DataVersion = "replaced-original-file"
	if err := s.AuthorizeAnalysisResultRead(t.Context(), read, a); err == nil {
		t.Fatal("replaced source retained old proof")
	}
	host.version.DataVersion = version
	host.dataset.Columns = host.dataset.Columns[:1]
	if err := s.AuthorizeAnalysisResultRead(t.Context(), read, a); err == nil {
		t.Fatal("revoked projected field retained old result")
	}
	if host.streamed != 1 {
		t.Fatal("denied read executed table analysis")
	}
}
