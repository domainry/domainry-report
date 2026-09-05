package report

import (
	"context"
	"errors"
	"strings"

	"github.com/domainry/domainry-foundation/apperror"
	reportsdk "github.com/domainry/domainry-report-sdk"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
	"github.com/domainry/domainry-report-sdk/modulehost"
	reportexport "github.com/domainry/domainry-report/internal/domain/report/service/export"
	reportobjectsql "github.com/domainry/domainry-report/internal/domain/report/service/objectsql"
)

type ExportService struct {
	queries     *QueryService
	definitions ExportDefinitionProvider
	authorizer  modulehost.ExportAuthorization
	gateway     modulehost.ExportGateway
}

type ExportDefinitionProvider interface {
	ReportExportControl(context.Context, string, string) (reportmodel.ReportExportControlSchema, bool, error)
}

func NewExportService(queries *QueryService, definitions ExportDefinitionProvider, authorizer modulehost.ExportAuthorization, gateway modulehost.ExportGateway) *ExportService {
	return &ExportService{queries: queries, definitions: definitions, authorizer: authorizer, gateway: gateway}
}

func (s *ExportService) Prepare(ctx context.Context, request reportmodel.ReportExportPrepareRequest, authority reportmodel.ReportAuthority) (reportmodel.ReportExportJob, error) {
	if s == nil || s.queries == nil || s.gateway == nil {
		return reportmodel.ReportExportJob{}, reportError(500, "backend.report.export_unavailable", nil)
	}
	if strings.TrimSpace(request.IdempotencyKey) == "" {
		return reportmodel.ReportExportJob{}, reportError(400, "backend.idempotency.key_required", nil)
	}
	subject, resolved, _, err := s.resolveExecution(ctx, reportmodel.ReportExportExecutionRequest{
		ReportKey: request.ReportKey, ObjectKey: request.ObjectKey, Scope: request.Scope,
	}, authority)
	if err != nil {
		return reportmodel.ReportExportJob{}, err
	}
	request.Scope = resolved.Scope
	job, err := s.gateway.PrepareReportExport(ctx, request, resolved.Definition.Report, resolved.Definition.Control, subject)
	if err != nil {
		return reportmodel.ReportExportJob{}, normalizeHostError(err, "backend.report.export_prepare_failed")
	}
	return job, nil
}

// ResolveExecution applies Report-owned export policy to the current owner
// definitions while borrowing only Record authorization facts from the host.
func (s *ExportService) ResolveExecution(ctx context.Context, request reportmodel.ReportExportExecutionRequest, authority reportmodel.ReportAuthority) (reportmodel.ReportExportExecution, error) {
	_, resolved, _, err := s.resolveExecution(ctx, request, authority)
	return resolved, err
}

// ReadPage is the only asynchronous export execution path. It re-resolves and
// reauthorizes the current definition, then uses the same Report query engine,
// source-version fencing and opaque pagination as interactive queries.
func (s *ExportService) ReadPage(ctx context.Context, request reportmodel.ReportExportExecutionRequest, authority reportmodel.ReportAuthority) (reportmodel.ReportSummary, error) {
	subject, resolved, scoped, err := s.resolveExecution(ctx, request, authority)
	if err != nil {
		return reportmodel.ReportSummary{}, err
	}
	fingerprint := reportFingerprint(scoped, map[string]any{
		"mode": "export", "object_key": strings.TrimSpace(request.ObjectKey), "scope": resolved.Scope,
	}, subject)
	return s.queries.executeStableObjectSQLPage(ctx, scoped, resolved.Scope.Parameters, request.Page, fingerprint, subject)
}

// SourceVersion resolves the same authorized, scoped definition as ReadPage
// and asks the host only for its current source watermark.
func (s *ExportService) SourceVersion(ctx context.Context, request reportmodel.ReportExportExecutionRequest, authority reportmodel.ReportAuthority) (reportmodel.ReportSnapshotSourceVersion, error) {
	subject, _, scoped, err := s.resolveExecution(ctx, request, authority)
	if err != nil {
		return reportmodel.ReportSnapshotSourceVersion{}, err
	}
	if s.queries == nil || s.queries.sourceVersions == nil {
		return reportmodel.ReportSnapshotSourceVersion{}, reportError(500, "backend.report.source_version_unavailable", nil)
	}
	version, err := s.queries.sourceVersions.ReadReportSourceVersion(ctx, scoped, subject)
	if err != nil {
		return reportmodel.ReportSnapshotSourceVersion{}, normalizeHostError(err, "backend.report.source_version_failed")
	}
	return version, nil
}

func (s *ExportService) resolveExecution(ctx context.Context, request reportmodel.ReportExportExecutionRequest, authority reportmodel.ReportAuthority) (reportmodel.ReportSubject, reportmodel.ReportExportExecution, reportmodel.ReportSchema, error) {
	if s == nil || s.queries == nil || s.authorizer == nil {
		return reportmodel.ReportSubject{}, reportmodel.ReportExportExecution{}, reportmodel.ReportSchema{}, reportError(500, "backend.report.export_unavailable", nil)
	}
	subject, definition, err := s.resolveCurrent(ctx, request.ReportKey, request.ObjectKey, authority)
	if err != nil {
		return reportmodel.ReportSubject{}, reportmodel.ReportExportExecution{}, reportmodel.ReportSchema{}, err
	}
	exportPermission := reportExportDataPermission(request.ObjectKey)
	authorization := exportAuthorization{service: s, subject: subject, dataPermissions: []string{exportPermission}}
	scoped := reportForDataPermissions(definition.Report, authorization.dataPermissions)
	plan, err := s.queries.compileAuthorizedObjectSQL(ctx, scoped, subject)
	if err != nil {
		return reportmodel.ReportSubject{}, reportmodel.ReportExportExecution{}, reportmodel.ReportSchema{}, err
	}
	normalized, scoped, _, err := reportexport.NormalizeScope(ctx, scoped, plan, request.ObjectKey, definition.Control, request.Scope, authorization)
	if err != nil {
		return reportmodel.ReportSubject{}, reportmodel.ReportExportExecution{}, reportmodel.ReportSchema{}, exportApplicationError(err)
	}
	scoped = reportForDataPermissions(scoped, authorization.dataPermissions)
	if err := reportexport.ValidateFieldAccess(ctx, plan, authorization); err != nil {
		return reportmodel.ReportSubject{}, reportmodel.ReportExportExecution{}, reportmodel.ReportSchema{}, exportApplicationError(err)
	}
	resolved := reportmodel.ReportExportExecution{Definition: definition, Scope: normalized}
	return subject, resolved, scoped, nil
}

type exportAuthorization struct {
	service         *ExportService
	subject         reportmodel.ReportSubject
	dataPermissions []string
}

func (a exportAuthorization) AuthorizeReportExportSource(ctx context.Context, objectKey string) error {
	err := a.service.authorizer.AuthorizeReportExportSource(ctx, objectKey, a.subject)
	if err != nil {
		return normalizeHostError(err, "backend.report.export_source_authorization_failed")
	}
	return nil
}

func (a exportAuthorization) AuthorizeReportExportField(ctx context.Context, objectKey, fieldKey string) (bool, error) {
	masked, err := a.service.authorizer.AuthorizeReportExportField(ctx, objectKey, fieldKey, a.subject)
	if err != nil {
		return false, normalizeHostError(err, "backend.report.export_field_authorization_failed")
	}
	return masked, nil
}

func exportApplicationError(err error) error {
	var stable *reportsdk.Error
	if errors.As(err, &stable) {
		return err
	}
	var app *apperror.AppError
	if errors.As(err, &app) {
		status := 500
		switch app.Kind {
		case apperror.KindBadRequest:
			status = 400
		case apperror.KindForbidden:
			status = 403
		case apperror.KindNotFound:
			status = 404
		case apperror.KindConflict:
			status = 409
		case apperror.KindRateLimited:
			status = 429
		case apperror.KindUnavailable:
			status = 503
		}
		return &reportsdk.Error{StatusCode: status, Code: app.Code, Params: app.Params, Cause: err}
	}
	if code := apperror.CodeOf(err); code != "" {
		return reportError(400, code, err)
	}
	return reportError(400, "backend.report.export_scope_invalid", err)
}

func (s *ExportService) resolveCurrent(ctx context.Context, reportKey, objectKey string, authority reportmodel.ReportAuthority) (reportmodel.ReportSubject, reportmodel.ReportExportDefinition, error) {
	subject, report, err := s.queries.resolve(ctx, reportKey, authority, reportsdk.ActionReportExportsPrepare)
	if err != nil {
		return reportmodel.ReportSubject{}, reportmodel.ReportExportDefinition{}, err
	}
	if !reportIncludesExportObject(report, objectKey) {
		return reportmodel.ReportSubject{}, reportmodel.ReportExportDefinition{}, reportError(400, "backend.report.object_not_in_report", nil)
	}
	if s.definitions == nil {
		return reportmodel.ReportSubject{}, reportmodel.ReportExportDefinition{}, reportError(500, "backend.report.export_unavailable", nil)
	}
	control, ok, err := s.definitions.ReportExportControl(ctx, report.Key, objectKey)
	if err != nil {
		return reportmodel.ReportSubject{}, reportmodel.ReportExportDefinition{}, normalizeHostError(err, "backend.report.definition_read_failed")
	}
	if !ok {
		return reportmodel.ReportSubject{}, reportmodel.ReportExportDefinition{}, reportError(400, "backend.report.export_control_not_found", nil)
	}
	return subject, reportmodel.ReportExportDefinition{Report: report, Control: control}, nil
}

func reportExportDataPermission(objectKey string) string {
	return strings.TrimSpace(objectKey) + ".export"
}

func reportIncludesExportObject(report reportmodel.ReportSchema, objectKey string) bool {
	objectKey = strings.TrimSpace(objectKey)
	if report.ObjectSQLV1 == nil {
		return false
	}
	candidates, err := reportobjectsql.DiscoverReportObjectSQLSources(report.ObjectSQLV1.SQL)
	if err != nil {
		return false
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == objectKey && objectKey != "" {
			return true
		}
	}
	return false
}

var _ reportsdk.Exports = (*ExportService)(nil)
