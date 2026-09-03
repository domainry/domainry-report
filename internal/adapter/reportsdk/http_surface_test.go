package reportsdk

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	actioncontract "github.com/domainry/domainry-foundation/action"
	"github.com/domainry/domainry-foundation/modulehttp"
	sdk "github.com/domainry/domainry-report-sdk"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

type reportHTTPQueries struct {
	objectSQLRequest reportmodel.ReportObjectSQLRequest
	authority        reportmodel.ReportAuthority
}

func (*reportHTTPQueries) Summary(context.Context, reportmodel.ReportSummaryRequest, reportmodel.ReportAuthority) (reportmodel.ReportSummary, error) {
	return reportmodel.ReportSummary{}, nil
}

func (q *reportHTTPQueries) QueryObjectSQL(_ context.Context, request reportmodel.ReportObjectSQLRequest, authority reportmodel.ReportAuthority) (reportmodel.ReportSummary, error) {
	q.objectSQLRequest, q.authority = request, authority
	return reportmodel.ReportSummary{Key: request.ReportKey, Rows: []reportmodel.ReportResultRow{}, ExecutionMode: "object_sql_v1"}, nil
}

type reportHTTPSnapshots struct{}

func (reportHTTPSnapshots) Refresh(context.Context, reportmodel.ReportSnapshotRefreshRequest, reportmodel.ReportAuthority) (reportmodel.ReportSnapshot, error) {
	return reportmodel.ReportSnapshot{}, nil
}

type reportHTTPExports struct{}

func (*reportHTTPExports) Prepare(context.Context, reportmodel.ReportExportPrepareRequest, reportmodel.ReportAuthority) (reportmodel.ReportExportJob, error) {
	return reportmodel.ReportExportJob{ID: "job-1"}, nil
}

func (*reportHTTPExports) ResolveExecution(context.Context, reportmodel.ReportExportExecutionRequest, reportmodel.ReportAuthority) (reportmodel.ReportExportExecution, error) {
	return reportmodel.ReportExportExecution{}, nil
}

func (*reportHTTPExports) ReadPage(context.Context, reportmodel.ReportExportExecutionRequest, reportmodel.ReportAuthority) (reportmodel.ReportSummary, error) {
	return reportmodel.ReportSummary{}, nil
}

func (*reportHTTPExports) SourceVersion(context.Context, reportmodel.ReportExportExecutionRequest, reportmodel.ReportAuthority) (reportmodel.ReportSnapshotSourceVersion, error) {
	return reportmodel.ReportSnapshotSourceVersion{}, nil
}

func TestReportHTTPSurfaceOwnsExactRoutesGovernanceAndOpenAPI(t *testing.T) {
	surface, err := newReportHTTPSurface(&Binding{})
	if err != nil {
		t.Fatal(err)
	}
	wantPatterns := []string{
		"GET /reports/{reportKey}/summary",
		"POST /reports/{reportKey}/query",
		"POST /reports/{reportKey}/snapshots/refresh",
		"POST /reports/{reportKey}/exports/{objectKey}/prepare",
	}
	routes := surface.Routes()
	if len(routes) != len(wantPatterns) {
		t.Fatalf("routes=%d want=%d", len(routes), len(wantPatterns))
	}
	for index, route := range routes {
		if route.Pattern() != wantPatterns[index] {
			t.Fatalf("route[%d]=%q want=%q", index, route.Pattern(), wantPatterns[index])
		}
		if route.Action.Authorization.Strategy != actioncontract.AuthorizationAuthenticated || route.Action.Permission == nil || route.Action.Permission.Key != route.Action.Key || !reflect.DeepEqual(route.Action.Exposures, []modulehttp.Exposure{modulehttp.ExposurePublic}) {
			t.Fatalf("route[%d] authorization/governance=%#v", index, route)
		}
	}
	if governance := routes[1].Action; governance.EffectClass != "read" || governance.IdempotencyDecision != "not_applicable" {
		t.Fatalf("Object SQL query governance=%#v", governance)
	}
	if governance := routes[3].Action; governance.EffectClass != "write" || !reflect.DeepEqual(governance.ApprovalPolicies, []actioncontract.ApprovalPolicy{actioncontract.ApprovalConfirmation}) || governance.IdempotencyDecision != "caller_key_required" {
		t.Fatalf("export prepare governance=%#v", governance)
	}
	operations := surface.OpenAPIOperations()
	if len(operations) != len(wantPatterns) {
		t.Fatalf("OpenAPI operations=%d want=%d", len(operations), len(wantPatterns))
	}
	for _, pattern := range wantPatterns {
		if operations[pattern] == nil {
			t.Fatalf("missing owner OpenAPI operation %s", pattern)
		}
	}
	summaryParameters := reportOpenAPIParameters(operations[wantPatterns[0]])
	for _, name := range []string{"reportKey", "mode", "query_key", "tags", "page_size", "cursor"} {
		if !reportOpenAPIHasParameter(summaryParameters, name) {
			t.Errorf("summary OpenAPI missing parameter %s: %#v", name, summaryParameters)
		}
	}
	properties := reportSummaryOpenAPISchema()["properties"].(map[string]any)
	if semantics := properties["total_semantics"].(map[string]any)["enum"].([]string); !reflect.DeepEqual(semantics, []string{reportmodel.ReportTotalExact, reportmodel.ReportTotalAtLeast}) {
		t.Fatalf("summary total semantics=%v", semantics)
	}
	prepare := operations[wantPatterns[3]]
	if !reportOpenAPIHasParameter(reportOpenAPIParameters(prepare), "Idempotency-Key") {
		t.Fatalf("export prepare OpenAPI is missing caller idempotency key")
	}
	responses := prepare["responses"].(map[string]any)
	if responses["202"] == nil || responses["200"] != nil {
		t.Fatalf("export prepare responses=%#v", responses)
	}
	schema := prepare["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if !reflect.DeepEqual(schema["required"], []string{"audit_id", "scope"}) {
		t.Fatalf("export prepare required=%#v", schema["required"])
	}
}

func TestReportHTTPSurfaceRejectsTrailingJSONAndForwardsCallerProof(t *testing.T) {
	queries, exports := &reportHTTPQueries{}, &reportHTTPExports{}
	binding := &Binding{queries: queries, snapshots: reportHTTPSnapshots{}, exports: exports}
	surface, err := newReportHTTPSurface(binding)
	if err != nil {
		t.Fatal(err)
	}
	handler := surface.Handler()

	for name, body := range map[string]string{
		"trailing value": `{"parameters":{}} {}`,
		"unknown field":  `{"parameters":{},"sql":"SELECT 1"}`,
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/reports/sales/query", strings.NewReader(body))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}

	request := httptest.NewRequest(http.MethodPost, "/reports/sales/query", strings.NewReader(`{"parameters":{"region":"west"},"page_size":25}`))
	request.Header.Set("Authorization", "Bearer report-token")
	request.Header.Set("X-Request-ID", "request-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("query status=%d body=%s", response.Code, response.Body.String())
	}
	if queries.objectSQLRequest.ReportKey != "sales" || queries.objectSQLRequest.Page.PageSize != 25 || queries.objectSQLRequest.Parameters["region"] != "west" {
		t.Fatalf("query request=%#v", queries.objectSQLRequest)
	}
	if queries.authority.AccessToken != "report-token" || queries.authority.RequestID != "request-1" {
		t.Fatalf("query authority=%#v", queries.authority)
	}

	request = httptest.NewRequest(http.MethodPost, "/reports/sales/exports/order/prepare", strings.NewReader(`{"audit_id":"audit-1","scope":{"purpose":"test","freshness":{"mode":"realtime"}}}`))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || response.Header().Get("Location") != "/data-exchange/jobs/job-1?provider=reports&operation=export" {
		t.Fatalf("prepare status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
}

func reportOpenAPIParameters(operation map[string]any) []map[string]any {
	values, _ := operation["parameters"].([]any)
	parameters := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if parameter, ok := value.(map[string]any); ok {
			parameters = append(parameters, parameter)
		}
	}
	return parameters
}

func reportOpenAPIHasParameter(parameters []map[string]any, name string) bool {
	for _, parameter := range parameters {
		if parameter["name"] == name {
			return true
		}
	}
	return false
}

var _ sdk.Queries = (*reportHTTPQueries)(nil)
var _ sdk.SnapshotCommands = reportHTTPSnapshots{}
var _ sdk.Exports = (*reportHTTPExports)(nil)
