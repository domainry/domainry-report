package reportsdk

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	actioncontract "github.com/domainry/domainry-foundation/action"
	"github.com/domainry/domainry-foundation/modulehttp"
	sdk "github.com/domainry/domainry-report-sdk"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

type reportHTTPSurface struct {
	binding *Binding
	handler http.Handler
}

func newReportHTTPSurface(binding *Binding) *reportHTTPSurface {
	surface := &reportHTTPSurface{binding: binding}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /reports/{reportKey}/summary", surface.summary)
	mux.HandleFunc("POST /reports/{reportKey}/query", surface.queryObjectSQL)
	mux.HandleFunc("POST /reports/{reportKey}/snapshots/refresh", surface.refreshSnapshot)
	mux.HandleFunc("POST /reports/{reportKey}/exports/{objectKey}/prepare", surface.prepareExport)
	surface.handler = mux
	return surface
}

func (*reportHTTPSurface) ContractVersion() string { return modulehttp.ContractVersion }
func (*reportHTTPSurface) Owner() string           { return "report" }
func (*reportHTTPSurface) Name() string            { return "business" }
func (s *reportHTTPSurface) Handler() http.Handler { return s.handler }

func (*reportHTTPSurface) Routes() []modulehttp.Route {
	principal := func(key, pattern, label string, effect actioncontract.EffectClass, risk actioncontract.RiskLevel, idempotency, audit string, approvals ...actioncontract.ApprovalPolicy) modulehttp.Route {
		method, route, _ := strings.Cut(pattern, " ")
		return modulehttp.Route{Action: actioncontract.ActionDefinition{
			Key: key, Owner: "module:report", SourceKind: "module_surface", CapabilityKey: "report.business", CapabilityLabel: "Business reports",
			OperationKey: key[strings.LastIndex(key, ".")+1:], OperationLabel: label, Label: label,
			Exposures: []actioncontract.Exposure{actioncontract.ExposurePublic}, Authorization: actioncontract.Authorization{Strategy: actioncontract.AuthorizationAuthenticatedPrincipal},
			HTTP: &actioncontract.HTTPBinding{Method: method, RouteTemplate: route}, EffectClass: effect, RiskLevel: risk,
			ApprovalPolicies: approvals, IdempotencyDecision: idempotency, AuditClass: audit, LifecycleStatus: actioncontract.LifecycleActive,
		}}
	}
	return []modulehttp.Route{
		principal("report.summary.get", "GET /reports/{reportKey}/summary", "Get report summary", actioncontract.EffectRead, actioncontract.RiskLow, "not_applicable", "owner_read_audit_policy"),
		principal("report.query.execute", "POST /reports/{reportKey}/query", "Execute report query", actioncontract.EffectRead, actioncontract.RiskLow, "not_applicable", "owner_read_audit_policy"),
		principal("report.snapshots.refresh", "POST /reports/{reportKey}/snapshots/refresh", "Refresh report snapshot", actioncontract.EffectWrite, actioncontract.RiskMedium, "caller_key_required", "mutation_audit_required"),
		principal("report.exports.prepare", "POST /reports/{reportKey}/exports/{objectKey}/prepare", "Prepare report export", actioncontract.EffectWrite, actioncontract.RiskHigh, "caller_key_required", "business_export_prepare_audit", actioncontract.ApprovalConfirmation),
	}
}

func (*reportHTTPSurface) OpenAPIOperations() map[string]map[string]any {
	security := []any{map[string]any{"BearerAuth": []any{}}}
	reportKey := map[string]any{"name": "reportKey", "in": "path", "required": true, "schema": map[string]any{"type": "string"}}
	pathParameter := func(name string) map[string]any {
		return map[string]any{"name": name, "in": "path", "required": true, "schema": map[string]any{"type": "string"}}
	}
	queryParameter := func(name string, schema map[string]any) map[string]any {
		return map[string]any{"name": name, "in": "query", "required": false, "schema": schema}
	}
	idempotencyKey := map[string]any{"name": "Idempotency-Key", "in": "header", "required": true, "schema": map[string]any{"type": "string", "minLength": 1}}
	return map[string]map[string]any{
		"GET /reports/{reportKey}/summary": {
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
		"POST /reports/{reportKey}/query": {
			"operationId": "queryReportObjectSQL", "tags": []string{"Reports"}, "summary": "Execute an authored Object SQL report with typed parameters",
			"security": security, "parameters": []any{reportKey}, "requestBody": jsonRequestBody(map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"parameters": reportObjectSQLParametersOpenAPISchema(), "page_size": map[string]any{"type": "integer", "minimum": 1, "maximum": reportmodel.ReportPageMaximumSize}, "cursor": map[string]any{"type": "string"}}}), "responses": standardOpenAPIResponses("200", "Report summary", reportSummaryOpenAPISchema()),
		},
		"POST /reports/{reportKey}/snapshots/refresh": {
			"operationId": "refreshReportSnapshot", "tags": []string{"Reports"}, "summary": "Refresh a materialized report snapshot",
			"security": security, "parameters": []any{reportKey, idempotencyKey}, "responses": standardOpenAPIResponses("200", "Report snapshot", reportSnapshotOpenAPISchema()),
		},
		"POST /reports/{reportKey}/exports/{objectKey}/prepare": {
			"operationId": "prepareReportExport", "tags": []string{"Reports"}, "summary": "Prepare a governed report export",
			"security": security, "parameters": []any{reportKey, pathParameter("objectKey"), idempotencyKey},
			"requestBody": jsonRequestBody(map[string]any{"type": "object", "additionalProperties": false, "required": []string{"audit_id", "scope"}, "properties": map[string]any{"audit_id": map[string]any{"type": "string"}, "scope": reportExportScopeOpenAPISchema()}}),
			"responses":   standardOpenAPIResponses("202", "Accepted report export job", reportExportJobOpenAPISchema()),
		},
	}
}

func standardOpenAPIResponses(status, description string, schema map[string]any) map[string]any {
	return map[string]any{
		status: map[string]any{"description": description, "content": map[string]any{"application/json": map[string]any{"schema": schema}}},
		"400":  map[string]any{"description": "Invalid request"}, "401": map[string]any{"description": "Authentication required"},
		"403": map[string]any{"description": "Forbidden"}, "404": map[string]any{"description": "Report not found"}, "409": map[string]any{"description": "Report state conflict"},
		"default": reportOpenAPIErrorResponse(),
	}
}

func reportOpenAPIErrorResponse() map[string]any {
	return map[string]any{"description": "Error", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/Error"}}}}
}

func jsonRequestBody(schema map[string]any) map[string]any {
	return map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schema}}}
}

func (b *Binding) HTTPSurfaces() []modulehttp.Surface {
	b.mu.RLock()
	ready := b.queries != nil && b.snapshots != nil && b.exports != nil
	b.mu.RUnlock()
	if !ready {
		return nil
	}
	return []modulehttp.Surface{newReportHTTPSurface(b)}
}

func (s *reportHTTPSurface) summary(w http.ResponseWriter, r *http.Request) {
	page, err := reportPageRequest(r)
	if err != nil {
		writeReportError(w, err)
		return
	}
	result, err := s.binding.Queries().Summary(r.Context(), reportmodel.ReportSummaryRequest{
		ReportKey: strings.TrimSpace(r.PathValue("reportKey")), Mode: strings.TrimSpace(r.URL.Query().Get("mode")),
		QueryKey: strings.TrimSpace(r.URL.Query().Get("query_key")), Tags: append([]string(nil), r.URL.Query()["tags"]...), Page: page,
	}, reportAuthority(r))
	if err != nil {
		writeReportError(w, err)
		return
	}
	writeReportJSON(w, http.StatusOK, result)
}

func (s *reportHTTPSurface) queryObjectSQL(w http.ResponseWriter, r *http.Request) {
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

func (s *reportHTTPSurface) refreshSnapshot(w http.ResponseWriter, r *http.Request) {
	result, err := s.binding.SnapshotCommands().Refresh(r.Context(), reportmodel.ReportSnapshotRefreshRequest{ReportKey: strings.TrimSpace(r.PathValue("reportKey")), IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key"))}, reportAuthority(r))
	if err != nil {
		writeReportError(w, err)
		return
	}
	writeReportJSON(w, http.StatusOK, result)
}

func (s *reportHTTPSurface) prepareExport(w http.ResponseWriter, r *http.Request) {
	var request struct {
		AuditID string                               `json:"audit_id"`
		Scope   reportmodel.ReportExportScopeRequest `json:"scope"`
	}
	if err := decodeReportJSON(w, r, &request); err != nil {
		writeReportError(w, &sdk.Error{StatusCode: 400, Code: "backend.bad_request", Cause: err})
		return
	}
	job, err := s.binding.Exports().Prepare(r.Context(), reportmodel.ReportExportPrepareRequest{
		ReportKey: strings.TrimSpace(r.PathValue("reportKey")), ObjectKey: strings.TrimSpace(r.PathValue("objectKey")),
		AuditID: strings.TrimSpace(request.AuditID), IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key")), Scope: request.Scope,
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
		AccessToken: token, Surface: "business_workspace", RequestID: strings.TrimSpace(r.Header.Get("X-Request-ID")),
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

var _ modulehttp.Surface = (*reportHTTPSurface)(nil)
var _ modulehttp.OpenAPIProvider = (*reportHTTPSurface)(nil)
var _ modulehttp.Provider = (*Binding)(nil)
