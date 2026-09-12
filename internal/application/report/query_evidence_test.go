package report

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	reportsdk "github.com/domainry/domainry-report-sdk"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

type evidenceExecutor struct {
	applicationTestObjectSQL
	deny     map[string]bool
	err      error
	after    func()
	complete bool
	empty    bool
}

func (e *evidenceExecutor) AuthorizeReportObjectSQLPlan(ctx context.Context, r reportmodel.ReportSchema, p reportmodel.ReportObjectSQLPlan, s reportmodel.ReportSubject) error {
	if e.deny[r.Key] {
		return reportError(403, "test.field_denied", nil)
	}
	return e.applicationTestObjectSQL.AuthorizeReportObjectSQLPlan(ctx, r, p, s)
}

func (e *evidenceExecutor) ExecuteReportObjectSQL(ctx context.Context, r reportmodel.ReportObjectSQLExecutionRequest) (reportmodel.ReportObjectSQLExecutionResult, error) {
	if e.after != nil {
		defer e.after()
	}
	if e.err != nil {
		return reportmodel.ReportObjectSQLExecutionResult{}, e.err
	}
	if e.empty {
		e.requests = append(e.requests, r)
		return reportmodel.ReportObjectSQLExecutionResult{Rows: []map[string]string{}, TotalKnown: true}, nil
	}
	out, err := e.applicationTestObjectSQL.ExecuteReportObjectSQL(ctx, r)
	// Keep large integers as strings all the way through the evidence seal.
	out.Rows[0]["count"] = "9007199254740993"
	if e.complete {
		out.HasMore, out.NextCursor, out.Total = false, "", len(out.Rows)
	}
	return out, err
}

func evidenceFixture() (*QueryService, *evidenceExecutor, *applicationTestVersions, reportmodel.ReportAuthority) {
	executor := &evidenceExecutor{deny: map[string]bool{}}
	versions := &applicationTestVersions{version: "v1"}
	service := &QueryService{subjects: applicationTestSubjects{subject: applicationTestSubject()},
		definitions: applicationTestDefinitions{reports: []reportmodel.ReportSchema{applicationTestReport(false)}},
		objectSQL:   executor, sourceVersions: versions, cursorKey: []byte("evidence-test-key"),
		clock: func() time.Time { return time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC) },
	}
	return service, executor, versions, reportmodel.ReportAuthority{AccessToken: "test-token"}
}

func TestGovernedQueryCatalogFiltersAndPagesWithoutExecuting(t *testing.T) {
	s, e, _, a := evidenceFixture()
	reports := []reportmodel.ReportSchema{}
	for _, key := range []string{"z-last", "c-visible", "b-field-denied", "a-first", "d-audience", "e-source"} {
		r := applicationTestReport(false)
		r.Key = key
		if key == "d-audience" {
			r.AudienceRoles = []string{"different-role"}
		}
		if key == "e-source" {
			r.RequiredPermissions = []string{"secret.read"}
		}
		reports = append(reports, r)
	}
	e.deny["b-field-denied"] = true
	s.definitions = applicationTestDefinitions{reports: reports}
	first, err := s.Catalog(t.Context(), reportmodel.ReportCatalogRequest{Page: reportmodel.ReportPageRequest{PageSize: 1}}, a)
	if err != nil || len(first.Reports) != 1 || first.Reports[0].Key != "a-first" || !first.Truncated || first.NextCursor == "" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	nextRequest := reportmodel.ReportCatalogRequest{Page: reportmodel.ReportPageRequest{PageSize: 2, Cursor: first.NextCursor}}
	next, err := s.Catalog(t.Context(), nextRequest, a)
	if err != nil || len(next.Reports) != 2 || next.Reports[0].Key != "c-visible" || next.Reports[1].Key != "z-last" || next.Truncated {
		t.Fatalf("next=%+v err=%v", next, err)
	}
	if next.Reports[0].RowLimit != 100 || len(next.Reports[0].ResultSchema) != 2 || next.Reports[0].DefinitionVersion == "" {
		t.Fatalf("missing contract: %+v", next)
	}
	raw, _ := json.Marshal(next)
	for _, forbidden := range []string{"SELECT", "required_permissions", "source_objects", "access_scope", "secret.read"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("catalog leaked %s", forbidden)
		}
	}
	if len(e.requests) != 0 {
		t.Fatal("catalog executed a report")
	}
	for _, change := range []string{"user", "workspace", "scope", "definition", "filter", "tamper"} {
		t.Run(change, func(t *testing.T) {
			local := *s
			request := nextRequest
			subject := applicationTestSubject()
			switch change {
			case "user":
				subject.Principal.UserID = "another-user"
			case "workspace":
				subject.Principal.WorkspaceID = "another-workspace"
			case "scope":
				subject.AccessScopeHash = "new-scope"
			case "definition":
				changed := append([]reportmodel.ReportSchema(nil), reports...)
				changed[0].Name = "Changed"
				local.definitions = applicationTestDefinitions{reports: changed}
			case "filter":
				request.ReportKey = "z-last"
			case "tamper":
				request.Page.Cursor += "A"
			}
			local.subjects = applicationTestSubjects{subject: subject}
			_, err := local.Catalog(t.Context(), request, a)
			assertReportSDKErrorCode(t, err, "backend.report.cursor_invalid")
		})
	}
	s.subjects = applicationTestSubjects{subject: applicationTestSubjectWithPermissions("event.read")}
	_, err = s.Catalog(t.Context(), reportmodel.ReportCatalogRequest{}, a)
	assertReportSDKErrorCode(t, err, "backend.permission.denied")
}

func TestGovernedQuerySealsActualPagesAndReauthorizesWithoutExecution(t *testing.T) {
	s, e, _, a := evidenceFixture()
	query := reportmodel.ReportObjectSQLRequest{ReportKey: "sales", Page: reportmodel.ReportPageRequest{PageSize: 2}}
	first, err := s.Query(t.Context(), query, a)
	if err != nil || first.Source.Proof == "" || first.Source.Complete || first.Summary.Rows[0].Measures["count"] != "9007199254740993" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if len(e.requests) != 1 {
		t.Fatalf("expected actual query, got %d", len(e.requests))
	}
	var persisted reportmodel.ReportQueryResult
	raw, _ := json.Marshal(first)
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Summary.ExecutionCursor != "" {
		t.Fatal("private executor cursor crossed SDK boundary")
	}
	restored, _, _, _ := evidenceFixture()
	if err := restored.AuthorizeQueryResult(t.Context(), reportmodel.ReportQueryResultAuthorization{Query: query, Result: persisted}, a); err != nil {
		t.Fatal(err)
	}
	if len(restored.objectSQL.(*evidenceExecutor).requests) != 0 {
		t.Fatal("historical authorization executed query")
	}
	query.Page.Cursor = first.Summary.NextCursor
	last, err := s.Query(t.Context(), query, a)
	if err != nil || last.Summary.Truncated || last.Source.Complete || last.Summary.RowCount != 1 {
		t.Fatalf("continuation falsely complete: %+v err=%v", last, err)
	}
}

func TestGovernedQueryCompleteAndEmptyAreActualExecution(t *testing.T) {
	for _, empty := range []bool{false, true} {
		s, e, _, a := evidenceFixture()
		e.complete, e.empty = true, empty
		out, err := s.Query(t.Context(), reportmodel.ReportObjectSQLRequest{ReportKey: "sales"}, a)
		if err != nil || !out.Source.Complete || out.Source.RowLimit != 100 || len(e.requests) != 1 {
			t.Fatalf("out=%+v err=%v executions=%d", out, err, len(e.requests))
		}
		if empty && (len(out.Summary.Rows) != 0 || out.Summary.Total != 0) {
			t.Fatal("empty result misrepresented")
		}
	}
}

func TestGovernedQueryDeclaredParametersKeepPrecisionAndDefaults(t *testing.T) {
	s, e, _, a := evidenceFixture()
	r := applicationTestReport(false)
	r.ObjectSQLV1.SQL = "SELECT e.category AS category, COUNT(e.id) AS count FROM event e GROUP BY e.category HAVING COUNT(e.id) >= :minimum ORDER BY e.category LIMIT 100"
	r.ObjectSQLV1.Parameters = []reportmodel.ReportObjectSQLParameter{{Key: "minimum", Type: "integer", Default: int64(9007199254740993)}}
	s.definitions = applicationTestDefinitions{reports: []reportmodel.ReportSchema{r}}
	catalog, err := s.Catalog(t.Context(), reportmodel.ReportCatalogRequest{ReportKey: "sales"}, a)
	if err != nil || len(catalog.Reports) != 1 || catalog.Reports[0].Parameters[0].Default != json.Number("9007199254740993") {
		t.Fatal(catalog, err)
	}
	query := reportmodel.ReportObjectSQLRequest{ReportKey: "sales", Page: reportmodel.ReportPageRequest{PageSize: 2}}
	out, err := s.Query(t.Context(), query, a)
	if err != nil || len(e.requests) != 1 || e.requests[0].Parameters["minimum"] != int64(9007199254740993) {
		t.Fatal(out, err, e.requests)
	}
	// Explicit, omitted-default, and JSON-number inputs mean the same query.
	query.Parameters = map[string]any{"minimum": json.Number("9007199254740993")}
	if err := s.AuthorizeQueryResult(t.Context(), reportmodel.ReportQueryResultAuthorization{Query: query, Result: out}, a); err != nil {
		t.Fatal(err)
	}
	query.Parameters["minimum"] = "9007199254740994"
	if err := s.AuthorizeQueryResult(t.Context(), reportmodel.ReportQueryResultAuthorization{Query: query, Result: out}, a); err == nil {
		t.Fatal("changed parameter accepted")
	}
	query.Parameters["minimum"] = "wrong type"
	if _, err := s.Query(t.Context(), query, a); err == nil {
		t.Fatal("invalid typed parameter executed")
	}
	if len(e.requests) != 1 {
		t.Fatal("invalid parameter reached executor")
	}
}

func TestGovernedQueryRejectsChangedRequestResultScopeAndSource(t *testing.T) {
	for _, change := range []string{"row", "name", "column", "total", "cursor", "complete", "source", "timestamp", "proof", "page", "parameter", "report", "user", "workspace", "scope", "definition", "data", "field", "permission", "key"} {
		t.Run(change, func(t *testing.T) {
			s, e, v, a := evidenceFixture()
			query := reportmodel.ReportObjectSQLRequest{ReportKey: "sales", Page: reportmodel.ReportPageRequest{PageSize: 2}}
			out, err := s.Query(t.Context(), query, a)
			if err != nil {
				t.Fatal(err)
			}
			subject := applicationTestSubject()
			switch change {
			case "row":
				out.Summary.Rows[0].Measures["count"] = "0"
			case "name":
				out.Summary.Name = "Other report"
			case "column":
				out.Summary.ResultSchema[0].Type = "integer"
			case "total":
				out.Summary.Total++
			case "cursor":
				out.Summary.NextCursor = "other"
			case "complete":
				out.Source.Complete = true
			case "source":
				out.Source.DefinitionVersion = "other"
			case "timestamp":
				out.Source.QueriedAt = "2030-01-01T00:00:00Z"
			case "proof":
				out.Source.Proof = strings.Repeat("0", 64)
			case "page":
				query.Page.PageSize = 3
			case "parameter":
				query.Parameters = map[string]any{"sql": "SELECT secret"}
			case "report":
				query.ReportKey = "other"
			case "user":
				subject.Principal.UserID = "other"
			case "workspace":
				subject.Principal.WorkspaceID = "other"
			case "scope":
				subject.AccessScopeHash = "other"
			case "definition":
				r := applicationTestReport(false)
				r.Name = "New definition"
				s.definitions = applicationTestDefinitions{reports: []reportmodel.ReportSchema{r}}
			case "data":
				v.version = "v2"
			case "field":
				e.deny["sales"] = true
			case "permission":
				subject = applicationTestSubjectWithPermissions("event.read")
			case "key":
				s.cursorKey = []byte("other-runtime")
			}
			s.subjects = applicationTestSubjects{subject: subject}
			if err := s.AuthorizeQueryResult(t.Context(), reportmodel.ReportQueryResultAuthorization{Query: query, Result: out}, a); err == nil {
				t.Fatal("changed evidence accepted")
			}
			if len(e.requests) != 1 {
				t.Fatal("authorization reran query")
			}
		})
	}
}

func TestGovernedQueryExecutionFailureAndConcurrentChangeProduceNoEvidence(t *testing.T) {
	for _, change := range []string{"execution", "data", "scope", "field"} {
		t.Run(change, func(t *testing.T) {
			s, e, v, a := evidenceFixture()
			switch change {
			case "execution":
				e.err = errors.New("database is unavailable")
			case "data":
				e.after = func() { v.version += "x" }
			case "scope":
				e.after = func() {
					subject := applicationTestSubject()
					subject.AccessScopeHash = "changed"
					s.subjects = applicationTestSubjects{subject: subject}
				}
			case "field":
				e.after = func() { e.deny["sales"] = true }
			}
			out, err := s.Query(t.Context(), reportmodel.ReportObjectSQLRequest{ReportKey: "sales", Page: reportmodel.ReportPageRequest{PageSize: 2}}, a)
			if err == nil || out.Source.Proof != "" || len(out.Summary.Rows) != 0 {
				t.Fatalf("validation treated as execution: %+v err=%v", out, err)
			}
		})
	}
}

var _ reportsdk.GovernedQueries = (*QueryService)(nil)
