package reportsdk

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	actioncontract "github.com/domainry/domainry-foundation/action"
	"github.com/domainry/domainry-foundation/modulehttp"
	identitysdk "github.com/domainry/domainry-identity-sdk"
	sdk "github.com/domainry/domainry-report-sdk"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
	reportcontract "github.com/domainry/domainry-report/contract"
)

type reportHTTPQueries struct {
	objectSQLRequest reportmodel.ReportObjectSQLRequest
	authority        reportmodel.ReportAuthority
}

func TestReportAuthorityUsesAuthenticatedRequestIdentityWithoutBearerHeader(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/report/sales/summary", nil)
	request = request.WithContext(identitysdk.WithRequestIdentity(request.Context(), identitysdk.RequestIdentity{
		Principal: identitysdk.Principal{Known: true}, AccessToken: "cookie-session-token",
	}))
	authority := reportAuthority(request)
	if authority.AccessToken != "cookie-session-token" {
		t.Fatalf("access token = %q, want server-side authenticated request token", authority.AccessToken)
	}

	request.Header.Set("Authorization", "Bearer explicit-token")
	authority = reportAuthority(request)
	if authority.AccessToken != "explicit-token" {
		t.Fatalf("access token = %q, want explicit bearer token", authority.AccessToken)
	}
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

type reportHTTPExports struct {
	prepareRequest reportmodel.ReportExportPrepareRequest
	artifact       *reportmodel.ReportExportArtifact
}

func (e *reportHTTPExports) Prepare(_ context.Context, request reportmodel.ReportExportPrepareRequest, _ reportmodel.ReportAuthority) (reportmodel.ReportExportJob, error) {
	e.prepareRequest = request
	return reportmodel.ReportExportJob{ID: "job-1"}, nil
}

func (e *reportHTTPExports) PrepareForDelivery(_ context.Context, request reportmodel.ReportExportPrepareRequest, _ reportmodel.ReportAuthority) (reportmodel.ReportExportPreparation, error) {
	e.prepareRequest = request
	return reportmodel.ReportExportPreparation{Job: reportmodel.ReportExportJob{ID: "job-1"}, Artifact: e.artifact}, nil
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

// This is an embedded Adapter mapping contract. It deliberately does not
// claim a networked Remote binding or SaaS end-to-end execution path.
func TestReportHTTPAdapterOwnsExactRoutesAndGovernance(t *testing.T) {
	adapter, err := newReportHTTPAdapter(&Binding{})
	if err != nil {
		t.Fatal(err)
	}
	wantPatterns := []string{
		"GET /report/{reportKey}/summary",
		"POST /report/{reportKey}/query",
		"POST /report/{reportKey}/snapshots/refresh",
		"POST /report/{reportKey}/exports/{objectKey}/prepare",
	}
	routes := adapter.Routes()
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
	if governance := routes[3].Action; governance.EffectClass != "write" || !reflect.DeepEqual(governance.ApprovalPolicies, []actioncontract.ApprovalPolicy{actioncontract.ApprovalConfirmation, actioncontract.ApprovalReason}) || governance.IdempotencyDecision != "caller_key_required" {
		t.Fatalf("export prepare governance=%#v", governance)
	}
}

func TestReportHTTPAdapterRejectsTrailingJSONAndForwardsCallerProof(t *testing.T) {
	queries, exports := &reportHTTPQueries{}, &reportHTTPExports{}
	binding := &Binding{queries: queries, snapshots: reportHTTPSnapshots{}, exports: exports}
	adapter, err := newReportHTTPAdapter(binding)
	if err != nil {
		t.Fatal(err)
	}
	handler := adapter.Handler()

	for name, body := range map[string]string{
		"trailing value": `{"parameters":{}} {}`,
		"unknown field":  `{"parameters":{},"sql":"SELECT 1"}`,
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/report/sales/query", strings.NewReader(body))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}

	request := httptest.NewRequest(http.MethodPost, "/report/sales/query", strings.NewReader(`{"parameters":{"region":"west"},"page_size":25}`))
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

	request = httptest.NewRequest(http.MethodPost, "/report/sales/exports/order/prepare", strings.NewReader(`{"audit_id":"audit-1","retry_of_job_id":"failed-job-1","scope":{"purpose":"test","freshness":{"mode":"realtime"}}}`))
	if err := reportcontract.ApplyExportPrepareHeaders(request.Header, "export:orders:request-1", "approved test export"); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || response.Header().Get("Location") != "/data-exchange/jobs/job-1?provider=reports&operation=export" {
		t.Fatalf("prepare status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	if exports.prepareRequest.IdempotencyKey != "export:orders:request-1" || exports.prepareRequest.RetryOfJobID != "failed-job-1" {
		t.Fatalf("prepare request=%#v", exports.prepareRequest)
	}

	exports.artifact = &reportmodel.ReportExportArtifact{
		ID: "artifact-1", Filename: "sales.csv", ContentType: "text/csv; charset=utf-8", ContentSHA256: "abc123", Size: 14,
		Content: io.NopCloser(strings.NewReader("id,name\n1,one\n")),
	}
	request = httptest.NewRequest(http.MethodPost, "/report/sales/exports/order/prepare", strings.NewReader(`{"audit_id":"audit-2","scope":{"purpose":"test","freshness":{"mode":"realtime"}}}`))
	if err := reportcontract.ApplyExportPrepareHeaders(request.Header, "export:orders:request-2", "approved bounded export"); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "id,name\n1,one\n" || response.Header().Get("X-Report-Export-Job-ID") != "job-1" || response.Header().Get("Content-Disposition") != `attachment; filename=sales.csv` {
		t.Fatalf("inline prepare status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
}

var _ sdk.Queries = (*reportHTTPQueries)(nil)
var _ sdk.SnapshotCommands = reportHTTPSnapshots{}
var _ sdk.Exports = (*reportHTTPExports)(nil)
