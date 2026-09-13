package reportsdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/domainry/domainry-foundation/modulehttp"
	sdk "github.com/domainry/domainry-report-sdk"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
	reportapplication "github.com/domainry/domainry-report/internal/application/report"
)

type reportHTTPAdapter struct {
	binding    *Binding
	handler    http.Handler
	routes     []modulehttp.Route
	operations map[string]map[string]any
}

func newReportHTTPAdapter(binding *Binding) (*reportHTTPAdapter, error) {
	routes, operations, err := reportHTTPContract()
	if err != nil {
		return nil, err
	}
	adapter := &reportHTTPAdapter{binding: binding, routes: routes, operations: operations}
	mux := http.NewServeMux()
	handlers := adapter.handlers()
	for _, route := range routes {
		key := strings.TrimSpace(route.Action.Key)
		handler, found := handlers[key]
		if !found {
			return nil, fmt.Errorf("Report Action %q has no HTTP handler", key)
		}
		mux.HandleFunc(route.Pattern(), handler)
		delete(handlers, key)
	}
	if len(handlers) != 0 {
		keys := make([]string, 0, len(handlers))
		for key := range handlers {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("Report handlers have no Action manifest entries: %v", keys)
	}
	adapter.handler = mux
	return adapter, nil
}

func (*reportHTTPAdapter) ContractVersion() string { return modulehttp.ContractVersion }
func (*reportHTTPAdapter) Owner() string           { return "report" }
func (*reportHTTPAdapter) Name() string            { return "business" }
func (s *reportHTTPAdapter) Handler() http.Handler { return s.handler }

func (s *reportHTTPAdapter) Routes() []modulehttp.Route {
	return append([]modulehttp.Route(nil), s.routes...)
}

func (s *reportHTTPAdapter) OpenAPIOperations() map[string]map[string]any {
	return s.operations
}

func reportHTTPContract() ([]modulehttp.Route, map[string]map[string]any, error) {
	definitions, err := reportapplication.AuthorizationActions()
	if err != nil {
		return nil, nil, err
	}
	byAction := reportOpenAPIOperationsByAction()
	routes := make([]modulehttp.Route, 0, len(definitions))
	operations := make(map[string]map[string]any, len(definitions))
	for _, definition := range definitions {
		if definition.HTTP == nil {
			continue
		}
		route, err := modulehttp.RouteFromAction(definition)
		if err != nil {
			return nil, nil, fmt.Errorf("project Report Action %q: %w", definition.Key, err)
		}
		operation, found := byAction[definition.Key]
		if !found {
			return nil, nil, fmt.Errorf("Report Action %q has no OpenAPI operation", definition.Key)
		}
		if err := applyReportOperationPrerequisites(operation, definition); err != nil {
			return nil, nil, err
		}
		routes = append(routes, route)
		operations[route.Pattern()] = operation
		delete(byAction, definition.Key)
	}
	if len(byAction) != 0 {
		return nil, nil, fmt.Errorf("Report OpenAPI operations have no Action manifest entries")
	}
	return routes, operations, nil
}

func reportOpenAPIOperationsByAction() map[string]map[string]any {
	security := []any{map[string]any{"BearerAuth": []any{}}}
	reportKey := map[string]any{"name": "reportKey", "in": "path", "required": true, "schema": map[string]any{"type": "string"}}
	pathParameter := func(name string) map[string]any {
		return map[string]any{"name": name, "in": "path", "required": true, "schema": map[string]any{"type": "string"}}
	}
	queryParameter := func(name string, schema map[string]any) map[string]any {
		return map[string]any{"name": name, "in": "query", "required": false, "schema": schema}
	}
	return map[string]map[string]any{
		sdk.ActionReportSummaryGet: {
			"operationId": "getReportSummary", "tags": []string{"Reports"}, "summary": "Execute an authorized report summary",
			"security": security, "parameters": []any{
				reportKey,
				queryParameter("mode", map[string]any{"type": "string", "enum": []string{"realtime", "snapshot"}}),
				queryParameter("query_key", map[string]any{"type": "string"}),
				queryParameter("tags", map[string]any{"type": "array", "items": map[string]any{"type": "string"}}),
				queryParameter("page_size", map[string]any{"type": "integer", "minimum": 1, "maximum": reportmodel.ReportPageMaximumSize}),
				queryParameter("cursor", map[string]any{"type": "string"}),
			}, "responses": standardOpenAPIResponses("200", "Report summary", reportSummaryOpenAPISchema()),
		},
		sdk.ActionReportQueryExecute: {
			"operationId": "queryReportObjectSQL", "tags": []string{"Reports"}, "summary": "Execute an authored Object SQL report with typed parameters",
			"security": security, "parameters": []any{reportKey}, "requestBody": jsonRequestBody(map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"parameters": reportObjectSQLParametersOpenAPISchema(), "page_size": map[string]any{"type": "integer", "minimum": 1, "maximum": reportmodel.ReportPageMaximumSize}, "cursor": map[string]any{"type": "string"}}}), "responses": standardOpenAPIResponses("200", "Report summary", reportSummaryOpenAPISchema()),
		},
		sdk.ActionReportSnapshotsRefresh: {
			"operationId": "refreshReportSnapshot", "tags": []string{"Reports"}, "summary": "Refresh a materialized report snapshot",
			"security": security, "parameters": []any{reportKey}, "responses": standardOpenAPIResponses("200", "Report snapshot", reportSnapshotOpenAPISchema()),
		},
		sdk.ActionReportExportsPrepare: {
			"operationId": "prepareReportExport", "tags": []string{"Reports"}, "summary": "Prepare a governed report export",
			"security": security, "parameters": []any{reportKey, pathParameter("objectKey")},
			"requestBody": jsonRequestBody(map[string]any{"type": "object", "additionalProperties": false, "required": []string{"audit_id", "scope"}, "properties": map[string]any{"audit_id": map[string]any{"type": "string"}, "retry_of_job_id": map[string]any{"type": "string", "description": "Failed predecessor job ID for one new, freshly authorized attempt; the original job remains unchanged."}, "scope": reportExportScopeOpenAPISchema()}}),
			"responses":   standardOpenAPIResponses("202", "Accepted report export job", reportExportJobOpenAPISchema()),
		},
	}
}

func (s *reportHTTPAdapter) handlers() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		sdk.ActionReportSummaryGet:       s.summary,
		sdk.ActionReportQueryExecute:     s.queryObjectSQL,
		sdk.ActionReportSnapshotsRefresh: s.refreshSnapshot,
		sdk.ActionReportExportsPrepare:   s.prepareExport,
	}
}

func standardOpenAPIResponses(status, description string, schema map[string]any) map[string]any {
	return map[string]any{
		status: map[string]any{"description": description, "content": map[string]any{"application/json": map[string]any{"schema": schema}}},
		"400":  reportOpenAPIErrorResponse("Invalid request"), "401": reportOpenAPIErrorResponse("Authentication required"),
		"403": reportOpenAPIErrorResponse("Forbidden"), "404": reportOpenAPIErrorResponse("Report not found"), "409": reportOpenAPIErrorResponse("Report state conflict"),
		"default": reportOpenAPIErrorResponse(),
	}
}

func reportOpenAPIErrorResponse(descriptions ...string) map[string]any {
	description := "Error"
	if len(descriptions) != 0 && strings.TrimSpace(descriptions[0]) != "" {
		description = strings.TrimSpace(descriptions[0])
	}
	return map[string]any{"description": description, "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/Error"}}}}
}

func jsonRequestBody(schema map[string]any) map[string]any {
	return map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schema}}}
}

func (b *Binding) HTTPAdapters() []modulehttp.Adapter {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]modulehttp.Adapter(nil), b.adapters...)
}

func (s *reportHTTPAdapter) summary(w http.ResponseWriter, r *http.Request) {
	page, err := reportPageRequest(r)
	if err != nil {
		writeReportError(w, err)
		return
	}
	result, err := s.binding.Queries().Summary(r.Context(), reportmodel.ReportSummaryRequest{
		ReportKey: strings.TrimSpace(r.PathValue("reportKey")), Mode: strings.TrimSpace(r.URL.Query().Get("mode")),
		Page: page,
	}, reportAuthority(r))
	if err != nil {
		writeReportError(w, err)
		return
	}
	writeReportJSON(w, http.StatusOK, result)
}

func (s *reportHTTPAdapter) queryObjectSQL(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Parameters map[string]any `json:"parameters"`
		PageSize   int            `json:"page_size,omitempty"`
		Cursor     string         `json:"cursor,omitempty"`
	}
	if err := decodeReportJSON(w, r, &request); err != nil {
		writeReportError(w, &sdk.Error{StatusCode: 400, Code: "backend.bad_request", Cause: err})
		return
	}
	if request.Parameters == nil {
		request.Parameters = map[string]any{}
	}
	result, err := s.binding.Queries().QueryObjectSQL(r.Context(), reportmodel.ReportObjectSQLRequest{ReportKey: strings.TrimSpace(r.PathValue("reportKey")), Parameters: request.Parameters, Page: reportmodel.ReportPageRequest{PageSize: request.PageSize, Cursor: strings.TrimSpace(request.Cursor)}}, reportAuthority(r))
	if err != nil {
		writeReportError(w, err)
		return
	}
	writeReportJSON(w, http.StatusOK, result)
}

func (s *reportHTTPAdapter) refreshSnapshot(w http.ResponseWriter, r *http.Request) {
	result, err := s.binding.SnapshotCommands().Refresh(r.Context(), reportmodel.ReportSnapshotRefreshRequest{ReportKey: strings.TrimSpace(r.PathValue("reportKey")), IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key"))}, reportAuthority(r))
	if err != nil {
		writeReportError(w, err)
		return
	}
	writeReportJSON(w, http.StatusOK, result)
}

func (s *reportHTTPAdapter) prepareExport(w http.ResponseWriter, r *http.Request) {
	var request struct {
		AuditID      string                               `json:"audit_id"`
		Scope        reportmodel.ReportExportScopeRequest `json:"scope"`
		RetryOfJobID string                               `json:"retry_of_job_id,omitempty"`
	}
	if err := decodeReportJSON(w, r, &request); err != nil {
		writeReportError(w, &sdk.Error{StatusCode: 400, Code: "backend.bad_request", Cause: err})
		return
	}
	job, err := s.binding.Exports().Prepare(r.Context(), reportmodel.ReportExportPrepareRequest{
		ReportKey: strings.TrimSpace(r.PathValue("reportKey")), ObjectKey: strings.TrimSpace(r.PathValue("objectKey")),
		AuditID: strings.TrimSpace(request.AuditID), IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key")), Scope: request.Scope,
		RetryOfJobID: strings.TrimSpace(request.RetryOfJobID),
	}, reportAuthority(r))
	if err != nil {
		writeReportError(w, err)
		return
	}
	w.Header().Set("Location", "/data-exchange/jobs/"+job.ID+"?provider=reports&operation=export")
	writeReportJSON(w, http.StatusAccepted, job)
}

func reportAuthority(r *http.Request) reportmodel.ReportAuthority {
	token := ""
	parts := strings.Fields(strings.TrimSpace(r.Header.Get("Authorization")))
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		token = parts[1]
	}
	return reportmodel.ReportAuthority{
		AccessToken: token, RequestID: strings.TrimSpace(r.Header.Get("X-Request-ID")),
		BusinessProfileKey: strings.TrimSpace(r.Header.Get("X-Business-Profile-Key")), BusinessProfileID: strings.TrimSpace(r.Header.Get("X-Business-Profile-ID")),
	}
}

func reportPageRequest(r *http.Request) (reportmodel.ReportPageRequest, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("page_size"))
	if raw == "" {
		return reportmodel.ReportPageRequest{Cursor: strings.TrimSpace(r.URL.Query().Get("cursor"))}, nil
	}
	pageSize, err := strconv.Atoi(raw)
	if err != nil {
		return reportmodel.ReportPageRequest{}, &sdk.Error{StatusCode: 400, Code: "backend.report.page_size_invalid", Cause: err}
	}
	return reportmodel.ReportPageRequest{PageSize: pageSize, Cursor: strings.TrimSpace(r.URL.Query().Get("cursor"))}, nil
}

func decodeReportJSON(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body contains multiple JSON values")
		}
		return err
	}
	return nil
}

func writeReportJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeReportError(w http.ResponseWriter, err error) {
	status, code, message, params := http.StatusInternalServerError, "backend.internal_error", "", map[string]string(nil)
	var stable *sdk.Error
	if errors.As(err, &stable) {
		if stable.StatusCode >= 400 && stable.StatusCode <= 599 {
			status = stable.StatusCode
		}
		if strings.TrimSpace(stable.Code) != "" {
			code = stable.Code
		}
		message, params = stable.Message, stable.Params
	}
	payload := map[string]any{"code": code}
	if strings.TrimSpace(message) != "" {
		payload["message"] = message
	}
	if len(params) != 0 {
		payload["params"] = params
	}
	writeReportJSON(w, status, payload)
}

var _ modulehttp.Adapter = (*reportHTTPAdapter)(nil)
var _ modulehttp.OpenAPIProvider = (*reportHTTPAdapter)(nil)
var _ modulehttp.Provider = (*Binding)(nil)
