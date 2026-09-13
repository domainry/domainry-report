package report

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/domainry/domainry-report-sdk"
	model "github.com/domainry/domainry-report-sdk/model"
)

type queryReadScopeFixture struct {
	*evidenceExecutor
	scope string
}

func (h *queryReadScopeFixture) ReadReportResultScope(context.Context, model.ReportSchema, model.ReportSubject) (string, error) {
	return h.scope, nil
}

type analysisReadScopeFixture struct {
	*analysisExecutor
	scope string
}

func (h *analysisReadScopeFixture) ReadReportResultScope(context.Context, model.ReportSchema, model.ReportSubject) (string, error) {
	return h.scope, nil
}

func TestReportIndependentReadUsesSourceScopeAndPreservesExecutionAuthorization(t *testing.T) {
	s, executor, versions, a := evidenceFixture()
	host := &queryReadScopeFixture{executor, strings.Repeat("a", 64)}
	s.objectSQL = host
	query := model.ReportObjectSQLRequest{ReportKey: "sales"}
	result, err := s.Query(t.Context(), query, a)
	if err != nil || result.Source.ReadProof == "" {
		t.Fatal(result, err)
	}
	catalogRequest := model.ReportCatalogRequest{Page: model.ReportPageRequest{PageSize: 1}}
	catalog, err := s.Catalog(t.Context(), catalogRequest, a)
	if err != nil {
		t.Fatal(err)
	}
	reader := applicationTestSubjectWithPermissions(sdk.ActionReportResultsRead, "event.read")
	reader.AccessScopeHash = "changed-global-authorization-revision"
	s.subjects = applicationTestSubjects{subject: reader}
	in := model.ReportQueryResultAuthorization{Query: query, Result: result}
	check := func() error { return s.AuthorizeQueryResultRead(t.Context(), in, a) }
	if err := check(); err != nil {
		t.Fatal("execution-only revision invalidated readable data", err)
	}
	if err := s.AuthorizeCatalogRead(t.Context(), model.ReportCatalogReadAuthorization{Request: catalogRequest, Result: catalog}, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Query(t.Context(), query, a); err == nil {
		t.Fatal("reading granted execution")
	}
	if err := s.AuthorizeQueryResult(t.Context(), in, a); err == nil {
		t.Fatal("reading granted raw result replay")
	}
	for _, change := range []string{"result", "request", "scope", "data", "field", "read", "user", "proof", "metadata"} {
		t.Run(change, func(t *testing.T) {
			original := in
			current := reader
			switch change {
			case "result":
				in.Result.Source.Complete = !in.Result.Source.Complete
			case "request":
				in.Query.Page.PageSize = 1
			case "scope":
				host.scope = strings.Repeat("b", 64)
			case "data":
				versions.version = "v2"
			case "field":
				executor.deny["sales"] = true
			case "read":
				current = applicationTestSubjectWithPermissions("event.read")
			case "user":
				current.Principal.UserID = "other"
			case "proof":
				in.Result.Source.ReadProof = ""
			case "metadata":
				r := applicationTestReport(false)
				r.Name = "Changed"
				s.definitions = applicationTestDefinitions{reports: []model.ReportSchema{r}}
			}
			s.subjects = applicationTestSubjects{subject: current}
			if err := check(); err == nil {
				t.Fatal("changed or unauthorized source accepted")
			}
			in = original
			host.scope = strings.Repeat("a", 64)
			versions.version = "v1"
			executor.deny["sales"] = false
			s.subjects = applicationTestSubjects{subject: reader}
			s.definitions = applicationTestDefinitions{reports: []model.ReportSchema{applicationTestReport(false)}}
		})
	}
	if len(executor.requests) != 1 {
		t.Fatal("result read executed query", len(executor.requests))
	}
	legacy, _, _, _ := evidenceFixture()
	legacy.subjects = applicationTestSubjects{subject: reader}
	if err := legacy.AuthorizeQueryResultRead(t.Context(), in, a); err == nil {
		t.Fatal("missing source scope allowed reading")
	}
}

func TestAnalysisIndependentReadRechecksDataAndOriginalSpecification(t *testing.T) {
	s, e, v, a, request := analysisFixture()
	host := &analysisReadScopeFixture{e, strings.Repeat("c", 64)}
	s.objectSQL = host
	result, err := s.RunAnalysis(t.Context(), request, a)
	if err != nil || result.Source.ReadProof == "" {
		t.Fatal(result, err)
	}
	catalogRequest := model.AnalysisCatalogRequest{DatasetKey: request.DatasetKey}
	catalog, err := s.AnalysisCatalog(t.Context(), catalogRequest, a)
	if err != nil {
		t.Fatal(err)
	}
	reader := applicationTestSubjectWithPermissions(sdk.ActionReportResultsRead, "event.read", "order.read")
	reader.AccessScopeHash = "new-identity-revision"
	s.subjects = applicationTestSubjects{subject: reader}
	in := model.AnalysisResultAuthorization{Request: request, Result: result}
	if err := s.AuthorizeAnalysisResultRead(t.Context(), in, a); err != nil {
		t.Fatal(err)
	}
	if err := s.AuthorizeAnalysisCatalogRead(t.Context(), model.AnalysisCatalogReadAuthorization{Request: catalogRequest, Result: catalog}, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunAnalysis(t.Context(), request, a); err == nil {
		t.Fatal("reading granted analysis execution")
	}
	if err := s.AuthorizeAnalysisResult(t.Context(), in, a); err == nil {
		t.Fatal("reading granted raw replay")
	}
	for _, change := range []string{"body", "scope", "data", "field", "spec", "read", "catalog"} {
		t.Run(change, func(t *testing.T) {
			raw, _ := json.Marshal(in)
			var changed model.AnalysisResultAuthorization
			_ = json.Unmarshal(raw, &changed)
			switch change {
			case "body":
				changed.Result.Source.Complete = false
			case "scope":
				host.scope = strings.Repeat("d", 64)
			case "data":
				v.version = "new"
			case "field":
				e.denied = true
			case "spec":
				changed.Request.Filters = []model.AnalysisFilter{{Field: "category", Operator: "eq", Values: []any{"other"}}}
			case "read":
				s.subjects = applicationTestSubjects{subject: applicationTestSubjectWithPermissions("event.read")}
			case "catalog":
				e.datasets[1].Name = "Changed"
			}
			if err := s.AuthorizeAnalysisResultRead(t.Context(), changed, a); err == nil {
				t.Fatal("invalid analysis read accepted")
			}
			host.scope = strings.Repeat("c", 64)
			v.version = "v1"
			e.denied = false
			e.datasets[1].Name = "Events"
			s.subjects = applicationTestSubjects{subject: reader}
		})
	}
	if len(e.requests) != 1 {
		t.Fatal("reading reran analysis")
	}
}
