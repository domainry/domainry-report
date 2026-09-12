package report

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	reportsdk "github.com/domainry/domainry-report-sdk"
	model "github.com/domainry/domainry-report-sdk/model"
)

type analysisExecutor struct {
	applicationTestObjectSQL
	versions *applicationTestVersions
	datasets []model.AnalysisDataset
	after    func()
	denied   bool
	failure  error
	partial  bool
	zero     bool
}

func (e *analysisExecutor) ReadReportAnalysisSourceVersion(ctx context.Context, r model.ReportSchema, s model.ReportSubject) (model.ReportSnapshotSourceVersion, error) {
	return e.versions.ReadReportSourceVersion(ctx, r, s)
}

func (e *analysisExecutor) ReportAnalysisSources(context.Context, model.ReportSubject) ([]model.AnalysisDataset, error) {
	return e.datasets, nil
}
func (e *analysisExecutor) ResolveReportObjectSQLSources(ctx context.Context, r model.ReportSchema, s model.ReportSubject) (map[string]model.ReportSourceObject, error) {
	out, err := e.applicationTestObjectSQL.ResolveReportObjectSQLSources(ctx, r, s)
	for key, object := range out {
		object.Fields = append(object.Fields, model.ReportSourceField{Key: "amount", Type: "currency", Precision: 19, Scale: 2})
		out[key] = object
	}
	return out, err
}
func (e *analysisExecutor) AuthorizeReportObjectSQLPlan(ctx context.Context, r model.ReportSchema, p model.ReportObjectSQLPlan, s model.ReportSubject) error {
	if e.denied {
		return reportError(403, "test.field_denied", nil)
	}
	return e.applicationTestObjectSQL.AuthorizeReportObjectSQLPlan(ctx, r, p, s)
}
func (e *analysisExecutor) ExecuteReportObjectSQL(ctx context.Context, r model.ReportObjectSQLExecutionRequest) (model.ReportObjectSQLExecutionResult, error) {
	e.requests = append(e.requests, r)
	if e.after != nil {
		defer e.after()
	}
	if e.failure != nil {
		return model.ReportObjectSQLExecutionResult{}, e.failure
	}
	if e.zero {
		return model.ReportObjectSQLExecutionResult{}, nil
	}
	return model.ReportObjectSQLExecutionResult{Rows: []map[string]string{{"g0": "alpha", "gn0": "0", "m0": "12001", "n0": "12001", "source_count": "12001"}}, Total: 1, TotalKnown: true, HasMore: e.partial}, nil
}
func analysisFixture() (*QueryService, *analysisExecutor, *applicationTestVersions, model.ReportAuthority, model.AnalysisRequest) {
	s, _, v, a := evidenceFixture()
	e := &analysisExecutor{datasets: []model.AnalysisDataset{
		{Key: "order", Name: "Orders", Kind: "business_object", Version: "schema1", Columns: []model.AnalysisColumn{{Key: "status", Type: "text"}, {Key: "amount", Type: "currency", Unit: "CNY", Precision: 19, Scale: 2}}},
		{Key: "event", Name: "Events", Kind: "business_object", Version: "schema1", Columns: []model.AnalysisColumn{{Key: "category", Type: "text"}, {Key: "amount", Type: "currency", Unit: "CNY", Precision: 19, Scale: 2}}},
	}}
	s.objectSQL = e
	e.versions = v
	return s, e, v, a, model.AnalysisRequest{DatasetKey: "event", GroupBy: []string{"category"}, Measures: []model.AnalysisMeasure{{Key: "rows", Function: "count"}}}
}

func TestAnalysisOwnerExecutesWholeDatasetAndSealsAcrossOwnerReopen(t *testing.T) {
	s, e, _, a, request := analysisFixture()
	request.Filters = []model.AnalysisFilter{{Field: "amount", Operator: "ge", Values: []any{json.Number("9007199254740993.01")}}}
	out, err := s.RunAnalysis(t.Context(), request, a)
	if err != nil || len(e.requests) != 1 || len(out.Rows) != 1 || out.Source.InputCounts["dataset"] != "12001" || !out.Source.Complete || out.Source.Proof == "" {
		t.Fatal(out, err, e.requests)
	}
	call := e.requests[0]
	if call.PageSize != 0 || call.PageCursor != "" || call.Plan.Limit != 101 || call.Parameters["p0"] != "9007199254740993.01" || len(call.Plan.Sources) != 1 || call.Subject.Principal.UserID != "user-1" {
		t.Fatalf("bad host execution: %+v", call)
	}
	if strings.Contains(call.Report.ObjectSQLV1.SQL, "9007199254740993.01") {
		t.Fatal("filter interpolated")
	}
	if len(e.authorizedReports) < 2 {
		t.Fatal("missing before/after plan authorization")
	}
	restored, reopened, _, _, _ := analysisFixture()
	raw, _ := json.Marshal(out)
	var persisted model.AnalysisResult
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&persisted); err != nil {
		t.Fatal(err)
	}
	if err := restored.AuthorizeAnalysisResult(t.Context(), model.AnalysisResultAuthorization{Request: request, Result: persisted}, a); err != nil {
		t.Fatal(err)
	}
	if len(reopened.requests) != 0 {
		t.Fatal("history executed source again")
	}
	if out.Spec.Mode != "aggregate" || out.Spec.MaxRows != 100 || out.Source.DefinitionVersion == "" || out.Source.DataVersion == "" || out.Source.Scope != "current_subject_filtered_dataset" {
		t.Fatal(out)
	}
}

func TestAnalysisOwnerRejectsAlteredResultRequestAndCurrentAuthorization(t *testing.T) {
	for _, change := range []string{"row", "count", "method", "unit", "complete", "time", "source", "proof", "group", "filter", "dataset", "definition", "data", "user", "workspace", "scope", "permission", "source_permission", "field", "signing_key"} {
		t.Run(change, func(t *testing.T) {
			s, e, v, a, r := analysisFixture()
			out, err := s.RunAnalysis(t.Context(), r, a)
			if err != nil {
				t.Fatal(err)
			}
			subject := applicationTestSubject()
			switch change {
			case "row":
				value := "0"
				out.Rows[0].Values["rows"] = &value
			case "count":
				out.Source.InputCounts["dataset"] = "100"
			case "method":
				out.Methods[0].Method = "sample"
			case "unit":
				out.Columns[1].Unit = "USD"
			case "complete":
				out.Source.Complete = false
			case "time":
				out.Source.QueriedAt = "tomorrow"
			case "source":
				out.Source.DataVersion = "different"
			case "proof":
				out.Source.Proof = "bad"
			case "group":
				r.GroupBy = nil
			case "filter":
				r.Filters = []model.AnalysisFilter{{Field: "category", Operator: "eq", Values: []any{"beta"}}}
			case "dataset":
				r.DatasetKey = "secret"
			case "definition":
				e.datasets[1].Version = "schema2"
			case "data":
				v.version = "v2"
			case "user":
				subject.Principal.UserID = "user-2"
			case "workspace":
				subject.Principal.WorkspaceID = "workspace-2"
			case "scope":
				subject.AccessScopeHash = "scope2"
			case "permission":
				subject = applicationTestSubjectWithPermissions("event.read")
			case "source_permission":
				subject = applicationTestSubjectWithPermissions(reportsdk.ActionReportQueryExecute)
			case "field":
				e.denied = true
			case "signing_key":
				s.cursorKey = []byte("replacement")
			}
			s.subjects = applicationTestSubjects{subject: subject}
			if err := s.AuthorizeAnalysisResult(t.Context(), model.AnalysisResultAuthorization{Request: r, Result: out}, a); err == nil {
				t.Fatal("stale/altered result accepted")
			}
			if len(e.requests) != 1 {
				t.Fatal("reauthorization executed data")
			}
		})
	}
}

func TestAnalysisOwnerNeverReturnsValidationPartialOrChangingDataAsSuccess(t *testing.T) {
	for _, failure := range []string{"execute", "partial", "validation_only", "source_changed", "field_revoked", "subject_changed", "no_version", "no_executor", "no_discovery", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			s, e, v, a, r := analysisFixture()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch failure {
			case "execute":
				e.failure = errors.New("DB unavailable")
			case "partial":
				e.partial = true
			case "validation_only":
				e.zero = true
				r.GroupBy = nil
			case "source_changed":
				e.after = func() { v.version = "v2" }
			case "field_revoked":
				e.after = func() { e.denied = true }
			case "subject_changed":
				e.after = func() {
					p := applicationTestSubject()
					p.AccessScopeHash = "scope2"
					s.subjects = applicationTestSubjects{subject: p}
				}
			case "no_version":
				v.version = ""
			case "no_executor":
				s.objectSQL = nil
			case "no_discovery":
				s.objectSQL = &applicationTestObjectSQL{}
			case "cancelled":
				e.after = cancel
			}
			out, err := s.RunAnalysis(ctx, r, a)
			if err == nil || out.Source.Proof != "" || out.Source.Complete || len(out.Rows) > 0 {
				t.Fatal("failed analysis returned result", out, err)
			}
			if len(e.requests) > 1 {
				t.Fatal("failed analysis automatically retried")
			}
		})
	}
}

func TestAnalysisCatalogUsesCurrentBoundedMetadataAndNoImplicitProcessGrants(t *testing.T) {
	s, e, _, a, _ := analysisFixture()
	first, err := s.AnalysisCatalog(t.Context(), model.AnalysisCatalogRequest{Page: model.ReportPageRequest{PageSize: 1}}, a)
	if err != nil || len(first.Datasets) != 1 || first.Datasets[0].Key != "event" || !first.Truncated || first.NextCursor == "" {
		t.Fatal(first, err)
	}
	r := model.AnalysisCatalogRequest{Page: model.ReportPageRequest{PageSize: 1, Cursor: first.NextCursor}}
	last, err := s.AnalysisCatalog(t.Context(), r, a)
	if err != nil || len(last.Datasets) != 1 || last.Datasets[0].Key != "order" || last.Truncated || len(e.requests) != 0 {
		t.Fatal(last, err)
	}
	// Returned metadata cannot mutate the host's next discovery response.
	first.Datasets[0].Columns[0].Key = "tampered"
	if e.datasets[1].Columns[0].Key != "category" {
		t.Fatal("catalog aliased host metadata")
	}
	for _, change := range []string{"scope", "user", "filter", "tamper"} {
		t.Run(change, func(t *testing.T) {
			local := *s
			request := r
			p := applicationTestSubject()
			switch change {
			case "scope":
				p.AccessScopeHash = "new"
			case "user":
				p.Principal.UserID = "new"
			case "filter":
				request.DatasetKey = "order"
			case "tamper":
				request.Page.Cursor += "A"
			}
			local.subjects = applicationTestSubjects{subject: p}
			_, err := local.AnalysisCatalog(t.Context(), request, a)
			if err == nil {
				t.Fatal("old catalog cursor accepted")
			}
		})
	}
	process := applicationTestSubjectWithPermissions()
	process.TrustedProcess = true
	process.ProcessCapabilities = []string{reportsdk.ActionReportQueryExecute}
	s.subjects = applicationTestSubjects{subject: process}
	denied, err := s.AnalysisCatalog(t.Context(), model.AnalysisCatalogRequest{}, a)
	if err != nil || len(denied.Datasets) != 0 {
		t.Fatal("process gained arbitrary source read", denied, err)
	}
	process.ProcessCapabilities = append(process.ProcessCapabilities, "event.read")
	s.subjects = applicationTestSubjects{subject: process}
	allowed, err := s.AnalysisCatalog(t.Context(), model.AnalysisCatalogRequest{}, a)
	if err != nil || len(allowed.Datasets) != 1 || allowed.Datasets[0].Key != "event" {
		t.Fatal(allowed, err)
	}
}
