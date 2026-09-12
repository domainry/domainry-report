package report

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	reportsdk "github.com/domainry/domainry-report-sdk"
	model "github.com/domainry/domainry-report-sdk/model"
	"github.com/domainry/domainry-report-sdk/modulehost"
)

type analysisTableHost struct {
	applicationTestObjectSQL
	dataset             model.AnalysisDataset
	version             modulehost.AnalysisTableVersion
	rows                []model.AnalysisTableRow
	fields              []string
	streamed, checks    int
	denied              bool
	change              func(*analysisTableHost)
	streamError         error
	ignoreConsumerError bool
	contentMismatch     bool
}

func (h *analysisTableHost) ReportAnalysisSources(context.Context, model.ReportSubject) ([]model.AnalysisDataset, error) {
	return []model.AnalysisDataset{h.dataset}, nil
}
func (h *analysisTableHost) ReadReportAnalysisTableVersion(ctx context.Context, key string, fields []string, subject model.ReportSubject) (modulehost.AnalysisTableVersion, error) {
	h.checks++
	h.fields = append([]string{}, fields...)
	if h.denied || key != h.dataset.Key || subject.Principal.UserID != "user-1" || subject.Principal.WorkspaceID != "workspace-1" {
		return modulehost.AnalysisTableVersion{}, reportError(403, "backend.permission.denied", nil)
	}
	v := h.version
	digest := sha256.New()
	encoder := json.NewEncoder(digest)
	_ = encoder.Encode(fields)
	for _, row := range h.rows {
		values := make([]*string, 0, len(fields))
		for _, field := range fields {
			values = append(values, row[field])
		}
		_ = encoder.Encode(values)
	}
	v.ContentSHA256 = hex.EncodeToString(digest.Sum(nil))
	if h.contentMismatch {
		v.ContentSHA256 = strings.Repeat("0", 64)
	}
	return v, ctx.Err()
}
func (h *analysisTableHost) StreamReportAnalysisTable(ctx context.Context, v modulehost.AnalysisTableVersion, fields []string, subject model.ReportSubject, consume func(model.AnalysisTableRow) error) (modulehost.AnalysisTableVersion, error) {
	h.streamed++
	if _, err := h.ReadReportAnalysisTableVersion(ctx, v.DatasetKey, fields, subject); err != nil {
		return modulehost.AnalysisTableVersion{}, err
	}
	for _, raw := range h.rows {
		row := model.AnalysisTableRow{}
		for _, key := range fields {
			if value, ok := raw[key]; ok {
				row[key] = value
			}
		}
		if err := consume(row); err != nil && !h.ignoreConsumerError {
			return modulehost.AnalysisTableVersion{}, err
		}
	}
	if h.change != nil {
		h.change(h)
	}
	v, err := h.ReadReportAnalysisTableVersion(ctx, v.DatasetKey, fields, subject)
	if err != nil {
		return v, err
	}
	return v, h.streamError
}
func (h *analysisTableHost) ExecuteReportObjectSQL(context.Context, model.ReportObjectSQLExecutionRequest) (model.ReportObjectSQLExecutionResult, error) {
	panic("table executed Object SQL")
}
func tableOwnerFixture() (*QueryService, *analysisTableHost, model.ReportAuthority, model.AnalysisRequest) {
	s, _, _, a := evidenceFixture()
	s.subjects = applicationTestSubjects{subject: applicationTestSubjectWithPermissions(reportsdk.ActionReportQueryExecute)}
	h := &analysisTableHost{dataset: model.AnalysisDataset{Key: "file_expenses", Name: "费用表", Kind: "table_file", Version: "file-schema-1", Columns: []model.AnalysisColumn{{Key: "dept", Type: "text"}, {Key: "amount", Type: "decimal", Unit: "CNY", Scale: 2}}}, version: modulehost.AnalysisTableVersion{DatasetKey: "file_expenses", DefinitionVersion: "file-schema-1", DataVersion: "original-plus-parser-and-table-sha", Rows: 1205, Complete: true}}
	dept, amount := "研发", "0.01"
	for i := 0; i < 1205; i++ {
		h.rows = append(h.rows, model.AnalysisTableRow{"dept": &dept, "amount": &amount})
	}
	s.tables = h
	s.objectSQL = &h.applicationTestObjectSQL
	return s, h, a, model.AnalysisRequest{DatasetKey: h.dataset.Key, GroupBy: []string{"dept"}, Measures: []model.AnalysisMeasure{{Key: "total", Field: "amount", Function: "sum"}}}
}

func TestAnalysisTableOwnerCompleteStreamCurrentScopeAndReopen(t *testing.T) {
	s, h, a, r := tableOwnerFixture()
	catalog, err := s.AnalysisCatalog(t.Context(), model.AnalysisCatalogRequest{}, a)
	if err != nil || len(catalog.Datasets) != 1 {
		t.Fatal(catalog, err)
	}
	out, err := s.RunAnalysis(t.Context(), r, a)
	if err != nil || len(out.Rows) != 1 || !out.Source.Complete || out.Source.InputCounts["dataset"] != "1205" || *out.Rows[0].Values["total"] != "12.05" {
		t.Fatal(out, err)
	}
	if h.streamed != 1 || h.checks < 3 || !reflect.DeepEqual(h.fields, []string{"amount", "dept"}) {
		t.Fatal(h.streamed, h.checks, h.fields)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var saved model.AnalysisResult
	if err = json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	reopened, host, _, _ := tableOwnerFixture()
	if err = reopened.AuthorizeAnalysisResult(t.Context(), model.AnalysisResultAuthorization{Request: r, Result: saved}, a); err != nil || host.streamed != 0 {
		t.Fatal(err, host.streamed)
	}
	host.denied = true
	if err = reopened.AuthorizeAnalysisResult(t.Context(), model.AnalysisResultAuthorization{Request: r, Result: saved}, a); err == nil {
		t.Fatal("revoked source exposed old result")
	}
	host.denied = false
	host.version.DataVersion = "changed-original"
	if err = reopened.AuthorizeAnalysisResult(t.Context(), model.AnalysisResultAuthorization{Request: r, Result: saved}, a); err == nil {
		t.Fatal("changed source exposed old result")
	}
}

func TestAnalysisTableOwnerFailsClosedOnIncompleteOrChangingSource(t *testing.T) {
	for _, kind := range []string{"catalog_only", "incomplete", "negative", "too_many", "empty_version", "wrong_definition", "wrong_dataset", "short", "long", "bad_cell", "ignored_consumer_error", "content_mismatch", "during_revoke", "during_version", "during_partial", "stream_error", "cancel", "subject", "workspace", "report_permission"} {
		t.Run(kind, func(t *testing.T) {
			s, h, a, r := tableOwnerFixture()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch kind {
			case "catalog_only":
				s.tables = nil
				s.objectSQL = &analysisExecutor{datasets: []model.AnalysisDataset{h.dataset}}
			case "incomplete":
				h.version.Complete = false
			case "negative":
				h.version.Rows = -1
			case "too_many":
				h.version.Rows = 100001
			case "empty_version":
				h.version.DataVersion = ""
			case "wrong_definition":
				h.version.DefinitionVersion = "other"
			case "wrong_dataset":
				h.version.DatasetKey = "other"
			case "short":
				h.rows = h.rows[:1]
			case "long":
				h.version.Rows = 1
			case "bad_cell", "ignored_consumer_error":
				delete(h.rows[0], "amount")
				h.ignoreConsumerError = kind == "ignored_consumer_error"
			case "content_mismatch":
				h.contentMismatch = true
			case "during_revoke":
				h.change = func(h *analysisTableHost) { h.denied = true }
			case "during_version":
				h.change = func(h *analysisTableHost) { h.version.DataVersion = "changed" }
			case "during_partial":
				h.change = func(h *analysisTableHost) { h.version.Complete = false }
			case "stream_error":
				h.streamError = errors.New("private storage detail")
			case "cancel":
				h.change = func(*analysisTableHost) { cancel() }
			case "subject", "workspace", "report_permission":
				subject := applicationTestSubjectWithPermissions(reportsdk.ActionReportQueryExecute)
				if kind == "subject" {
					subject.Principal.UserID = "another"
				}
				if kind == "workspace" {
					subject.Principal.WorkspaceID = "another"
				}
				if kind == "report_permission" {
					subject = applicationTestSubjectWithPermissions()
				}
				s.subjects = applicationTestSubjects{subject: subject}
			}
			out, err := s.RunAnalysis(ctx, r, a)
			if err == nil || len(out.Rows) != 0 || out.Source.Complete || out.Source.Proof != "" {
				t.Fatal("partial/unauthorized success", out, err)
			}
			if strings.Contains(err.Error(), "private storage detail") {
				t.Fatal("source error disclosed", err)
			}
		})
	}
}

func TestAnalysisObjectProofFingerprintRemainsCompatible(t *testing.T) {
	s, _, _, a, r := analysisFixture()
	state, err := s.analysisState(t.Context(), r, a)
	if err != nil {
		t.Fatal(err)
	}
	plans := []model.ReportObjectSQLPlan{}
	for _, q := range state.queries {
		plans = append(plans, q.Plan)
	}
	before := canonicalHash(struct {
		Dataset  model.AnalysisDataset
		Spec     model.AnalysisRequest
		Plans    []model.ReportObjectSQLPlan
		Versions []model.ReportSnapshotSourceVersion
		Scope    string
	}{state.plan.Dataset, state.plan.Spec, plans, state.versions, reportSnapshotAccessScopeHash(state.subject, []string{state.plan.Dataset.Key + ".read"})})
	if state.fingerprint() != before {
		t.Fatal("existing signed business results invalidated")
	}
}

var _ modulehost.AnalysisTables = (*analysisTableHost)(nil)
