package reportsdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/domainry/domainry-foundation/modulehttp"
	identitysdk "github.com/domainry/domainry-identity-sdk"
	sdk "github.com/domainry/domainry-report-sdk"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
	reportapplication "github.com/domainry/domainry-report/internal/application/report"
)

type reportHTTPAdapter struct {
	binding *Binding
	handler http.Handler
	routes  []modulehttp.Route
}

func newReportHTTPAdapter(binding *Binding) (*reportHTTPAdapter, error) {
	routes, err := reportHTTPContract()
	if err != nil {
		return nil, err
	}
	adapter := &reportHTTPAdapter{binding: binding, routes: routes}
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

func reportHTTPContract() ([]modulehttp.Route, error) {
	definitions, err := reportapplication.AuthorizationActions()
	if err != nil {
		return nil, err
	}
	routes := make([]modulehttp.Route, 0, len(definitions))
	for _, definition := range definitions {
		if definition.HTTP == nil {
			continue
		}
		route, err := modulehttp.RouteFromAction(definition)
		if err != nil {
			return nil, fmt.Errorf("project Report Action %q: %w", definition.Key, err)
		}
		routes = append(routes, route)
	}
	return routes, nil
}

// CapabilityRoutes returns the source-owned typed route catalog without
// opening the executable adapter.
func CapabilityRoutes() ([]modulehttp.Route, error) {
	return reportHTTPContract()
}

func (s *reportHTTPAdapter) handlers() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		sdk.ActionReportSummaryGet:       s.summary,
		sdk.ActionReportQueryExecute:     s.queryObjectSQL,
		sdk.ActionReportSnapshotsRefresh: s.refreshSnapshot,
		sdk.ActionReportExportsPrepare:   s.prepareExport,
	}
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
	prepareRequest := reportmodel.ReportExportPrepareRequest{
		ReportKey: strings.TrimSpace(r.PathValue("reportKey")), ObjectKey: strings.TrimSpace(r.PathValue("objectKey")),
		AuditID: strings.TrimSpace(request.AuditID), IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key")), Scope: request.Scope,
		RetryOfJobID: strings.TrimSpace(request.RetryOfJobID),
	}
	exports := s.binding.Exports()
	result := reportmodel.ReportExportPreparation{}
	var err error
	if delivery, ok := exports.(sdk.ExportDelivery); ok {
		result, err = delivery.PrepareForDelivery(r.Context(), prepareRequest, reportAuthority(r))
	} else {
		result.Job, err = exports.Prepare(r.Context(), prepareRequest, reportAuthority(r))
	}
	if err != nil {
		writeReportError(w, err)
		return
	}
	w.Header().Set("Location", "/data-exchange/jobs/"+result.Job.ID+"?provider=reports&operation=export")
	if result.Artifact == nil {
		writeReportJSON(w, http.StatusAccepted, result.Job)
		return
	}
	writePreparedReportExport(w, result.Job, result.Artifact)
}

func writePreparedReportExport(w http.ResponseWriter, job reportmodel.ReportExportJob, artifact *reportmodel.ReportExportArtifact) {
	if artifact == nil || artifact.Content == nil {
		writeReportError(w, &sdk.Error{StatusCode: http.StatusInternalServerError, Code: "backend.report.export_artifact_unavailable"})
		return
	}
	defer artifact.Content.Close()
	contentType := strings.TrimSpace(artifact.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	filename := strings.TrimSpace(artifact.Filename)
	if filename == "" {
		filename = "report-export.csv"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("X-Report-Export-Job-ID", job.ID)
	if artifact.ContentSHA256 != "" {
		w.Header().Set("ETag", `"sha256:`+artifact.ContentSHA256+`"`)
	}
	if artifact.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(artifact.Size, 10))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, artifact.Content)
}

func reportAuthority(r *http.Request) reportmodel.ReportAuthority {
	token := ""
	parts := strings.Fields(strings.TrimSpace(r.Header.Get("Authorization")))
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		token = parts[1]
	}
	// Runtime authenticates browser requests through an HttpOnly Cookie before
	// dispatching to module-owned routes. The resolved credential stays in the
	// server-side request context and is intentionally unavailable to browser JS.
	if token == "" {
		if identity, ok := identitysdk.RequestIdentityFromContext(r.Context()); ok {
			token = strings.TrimSpace(identity.AccessToken)
		}
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
var _ modulehttp.Provider = (*Binding)(nil)
