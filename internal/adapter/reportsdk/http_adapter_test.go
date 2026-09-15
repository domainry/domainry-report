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
	sdk "github.com/domainry/domainry-report-sdk"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
	reportcontract "github.com/domainry/domainry-report/contract"
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
func TestReportHTTPAdapterOwnsExactRoutesGovernanceAndOpenAPI(t *testing.T) {
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
	operations := adapter.OpenAPIOperations()
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
	prepareParameters := reportOpenAPIParameters(prepare)
	for _, expected := range []struct {
		name        string
		example     string
		errorCode   string
		enum        []string
		minimumSize int
	}{
		{name: reportcontract.IdempotencyKeyHeader, example: sdk.ActionReportExportsPrepare + ":<stable-logical-operation-id>", errorCode: reportcontract.IdempotencyKeyRequiredErrorCode, minimumSize: 1},
		{name: reportcontract.OperationReasonHeader, example: "Approved governed export for the stated business purpose", errorCode: reportcontract.OperationReasonRequiredErrorCode, minimumSize: 1},
		{name: reportcontract.OperationConfirmationHeader, example: reportcontract.OperationConfirmationConfirmed, errorCode: reportcontract.OperationConfirmationRequiredErrorCode, enum: []string{reportcontract.OperationConfirmationConfirmed}},
	} {
		parameter := reportOpenAPIParameter(prepareParameters, expected.name)
		if parameter == nil || parameter["in"] != "header" || parameter["required"] != true || parameter["example"] != expected.example {
			t.Fatalf("export prepare OpenAPI header %s=%#v", expected.name, parameter)
		}
		headerSchema := parameter["schema"].(map[string]any)
		if expected.minimumSize != 0 && headerSchema["minLength"] != expected.minimumSize {
			t.Fatalf("export prepare OpenAPI header %s schema=%#v", expected.name, headerSchema)
		}
		if expected.enum != nil && !reflect.DeepEqual(headerSchema["enum"], expected.enum) {
			t.Fatalf("export prepare OpenAPI header %s enum=%#v", expected.name, headerSchema["enum"])
		}
	}
	responses := prepare["responses"].(map[string]any)
	if responses["202"] == nil || responses["200"] == nil {
		t.Fatalf("export prepare responses=%#v", responses)
	}
	governanceErrors := responses["400"].(map[string]any)["x-domainry-error-codes"]
	wantGovernanceErrors := []string{
		reportcontract.IdempotencyKeyRequiredErrorCode,
		reportcontract.OperationConfirmationRequiredErrorCode,
		reportcontract.OperationReasonEncodingErrorCode,
		reportcontract.OperationReasonRequiredErrorCode,
	}
	if !reflect.DeepEqual(governanceErrors, wantGovernanceErrors) {
		t.Fatalf("export prepare governance errors=%#v want=%#v", governanceErrors, wantGovernanceErrors)
	}
	prerequisites := prepare["x-domainry-operation-prerequisites"].(map[string]any)
	if prerequisites["contract_version"] != reportOperationPrerequisitesContractVersion || prerequisites["action_key"] != sdk.ActionReportExportsPrepare || prerequisites["risk_level"] != string(actioncontract.RiskHigh) || !reflect.DeepEqual(prerequisites["error_codes"], wantGovernanceErrors) {
		t.Fatalf("export prepare prerequisites=%#v", prerequisites)
	}
	values := prerequisites["prerequisites"].([]any)
	if len(values) != 3 {
		t.Fatalf("export prepare prerequisites entries=%#v", values)
	}
	for _, expected := range []struct {
		header, actionField, predicate, actionValue, errorCode string
	}{
		{header: reportcontract.IdempotencyKeyHeader, actionField: "idempotency_decision", predicate: "equals", actionValue: "caller_key_required", errorCode: reportcontract.IdempotencyKeyRequiredErrorCode},
		{header: reportcontract.OperationReasonHeader, actionField: "approval_policies", predicate: "contains", actionValue: string(actioncontract.ApprovalReason), errorCode: reportcontract.OperationReasonRequiredErrorCode},
		{header: reportcontract.OperationConfirmationHeader, actionField: "approval_policies", predicate: "contains", actionValue: string(actioncontract.ApprovalConfirmation), errorCode: reportcontract.OperationConfirmationRequiredErrorCode},
	} {
		var found map[string]any
		for _, raw := range values {
			candidate := raw.(map[string]any)
			if candidate["header"].(map[string]any)["name"] == expected.header {
				found = candidate
				break
			}
		}
		if found == nil {
			t.Fatalf("export prepare prerequisite %s is absent: %#v", expected.header, values)
		}
		condition := found["condition"].(map[string]any)
		if condition["action_field"] != expected.actionField || condition[expected.predicate] != expected.actionValue {
			t.Fatalf("export prepare prerequisite %s condition=%#v", expected.header, condition)
		}
		if !stringSliceContains(found["error_codes"].([]string), expected.errorCode) {
			t.Fatalf("export prepare prerequisite %s errors=%#v", expected.header, found["error_codes"])
		}
	}
	schema := prepare["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if !reflect.DeepEqual(schema["required"], []string{"audit_id", "scope"}) {
		t.Fatalf("export prepare required=%#v", schema["required"])
	}
}

func TestReportOpenAPIPrerequisitesStayAlignedWithActionMetadata(t *testing.T) {
	routes, operations, err := reportHTTPContract()
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		action := route.Action
		parameters := reportOpenAPIParameters(operations[route.Pattern()])
		checks := []struct {
			name string
			want bool
		}{
			{name: reportcontract.IdempotencyKeyHeader, want: action.IdempotencyDecision == "caller_key_required"},
			{name: reportcontract.OperationReasonHeader, want: reportActionHasApprovalPolicy(action, actioncontract.ApprovalReason)},
			{name: reportcontract.OperationConfirmationHeader, want: reportActionHasApprovalPolicy(action, actioncontract.ApprovalConfirmation)},
		}
		for _, check := range checks {
			parameter := reportOpenAPIParameter(parameters, check.name)
			if (parameter != nil) != check.want {
				t.Fatalf("Action %s metadata/header drift for %s: want=%t parameter=%#v", action.Key, check.name, check.want, parameter)
			}
			if parameter != nil && (parameter["in"] != "header" || parameter["required"] != true) {
				t.Fatalf("Action %s prerequisite %s=%#v", action.Key, check.name, parameter)
			}
		}
	}
}

func TestReportOpenAPIRejectsConfirmationWithoutReasonMetadata(t *testing.T) {
	action := actioncontract.ActionDefinition{
		Key: "report.fixture.confirm", RiskLevel: actioncontract.RiskHigh,
		ApprovalPolicies: []actioncontract.ApprovalPolicy{actioncontract.ApprovalConfirmation},
	}
	operation := map[string]any{"operationId": "confirmFixture", "responses": map[string]any{"204": map[string]any{"description": "Confirmed"}}}
	if err := applyReportOperationPrerequisites(operation, action); err == nil {
		t.Fatal("accepted confirmation metadata without the host-required auditable reason")
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
	return reportOpenAPIParameter(parameters, name) != nil
}

func reportOpenAPIParameter(parameters []map[string]any, name string) map[string]any {
	for _, parameter := range parameters {
		if parameter["name"] == name {
			return parameter
		}
	}
	return nil
}

func stringSliceContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

var _ sdk.Queries = (*reportHTTPQueries)(nil)
var _ sdk.SnapshotCommands = reportHTTPSnapshots{}
var _ sdk.Exports = (*reportHTTPExports)(nil)
