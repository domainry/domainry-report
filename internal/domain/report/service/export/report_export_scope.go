package export

import (
	"context"
	"strings"

	"github.com/domainry/domainry-foundation/apperror"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
	reportobjectsql "github.com/domainry/domainry-report/internal/domain/report/service/objectsql"
)

// Authorization is the narrow host policy boundary consumed by Report export
// rules. Implementations answer current Runtime record/field policy without
// moving Report scope validation back into the host.
type Authorization interface {
	AuthorizeReportExportSource(context.Context, string) error
	AuthorizeReportExportField(context.Context, string, string) (bool, error)
	AuthorizeReportObjectSQLExport(context.Context, reportmodel.ReportSchema) error
}

// NormalizeScope binds only declared object_sql_v1 parameters and result
// columns. A request can narrow a published definition but cannot introduce
// SQL, source objects, fields, filters, or joins.
func NormalizeScope(ctx context.Context, report reportmodel.ReportSchema, objectKey string, control reportmodel.ReportExportControlSchema, request reportmodel.ReportExportScopeRequest, authorization Authorization) (reportmodel.ReportExportScopeRequest, reportmodel.ReportSchema, map[string]bool, error) {
	scope := request
	scope.Purpose = strings.TrimSpace(scope.Purpose)
	if scope.Purpose == "" || len(scope.Purpose) > 512 || report.ObjectSQLV1 == nil {
		return scope, report, nil, exportScopeError("backend.report.export_scope_invalid")
	}
	for _, sourceObject := range reportmodel.ReportObjectSQLObjectKeys(report.ObjectSQLV1) {
		if !reportExportControlOwnsSource(control, sourceObject) {
			return scope, report, nil, exportScopeError("backend.report.export_control_invalid")
		}
	}
	if authorization == nil {
		return scope, report, nil, &apperror.AppError{Kind: apperror.KindInternal, Code: "backend.report.export_authorizer_unavailable"}
	}
	if err := authorization.AuthorizeReportExportSource(ctx, strings.TrimSpace(objectKey)); err != nil {
		return scope, report, nil, err
	}

	parameters, err := reportobjectsql.NormalizeParameters(report.ObjectSQLV1.Parameters, scope.Parameters)
	if err != nil {
		return scope, report, nil, err
	}
	scope.Parameters = parameters

	allowedColumns := make(map[string]bool, len(report.ObjectSQLV1.ResultSchema))
	for _, column := range report.ObjectSQLV1.ResultSchema {
		allowedColumns[column.Key] = true
		if len(request.FieldProjection) == 0 {
			scope.FieldProjection = append(scope.FieldProjection, column.Key)
		}
	}
	if len(request.FieldProjection) > 0 {
		scope.FieldProjection = normalizeScopeProjection(request.FieldProjection)
	}
	if len(scope.FieldProjection) == 0 || len(scope.FieldProjection) > 128 {
		return scope, report, nil, exportScopeError("backend.report.export_projection_not_allowed")
	}
	for _, column := range scope.FieldProjection {
		if !allowedColumns[column] {
			return scope, report, nil, exportScopeError("backend.report.export_projection_not_allowed")
		}
	}

	scope.Freshness.Mode = strings.TrimSpace(scope.Freshness.Mode)
	if scope.Freshness.Mode == "" {
		scope.Freshness.Mode = "realtime"
	}
	if scope.Freshness.Mode != "realtime" || scope.Freshness.SnapshotID != "" || scope.Freshness.MaximumLagSeconds != 0 {
		return scope, report, nil, exportScopeError("backend.report.export_freshness_invalid")
	}
	return scope, report, map[string]bool{}, nil
}

func ValidateFieldAccess(ctx context.Context, report reportmodel.ReportSchema, _ reportmodel.ReportExportControlSchema, authorization Authorization) error {
	if authorization == nil {
		return &apperror.AppError{Kind: apperror.KindInternal, Code: "backend.report.export_authorizer_unavailable"}
	}
	return authorization.AuthorizeReportObjectSQLExport(ctx, report)
}

func reportExportControlOwnsSource(control reportmodel.ReportExportControlSchema, objectKey string) bool {
	objectKey = strings.TrimSpace(objectKey)
	for _, source := range control.SourceObjects {
		if strings.TrimSpace(source) == objectKey {
			return true
		}
	}
	return false
}
