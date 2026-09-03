package export

import (
	"context"
	"strings"
	"time"

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

func NormalizeScope(ctx context.Context, report reportmodel.ReportSchema, objectKey string, control reportmodel.ReportExportControlSchema, request reportmodel.ReportExportScopeRequest, authorization Authorization) (reportmodel.ReportExportScopeRequest, reportmodel.ReportSchema, map[string]bool, error) {
	scope := request
	originalTimeZone := defaultReportTimeZone(report)
	scope.QueryKey = strings.TrimSpace(scope.QueryKey)
	scope.AnalysisKey = strings.TrimSpace(scope.AnalysisKey)
	scope.TagMatch = strings.ToLower(strings.TrimSpace(scope.TagMatch))
	scope.Purpose = strings.TrimSpace(scope.Purpose)
	if scope.Purpose == "" || len(scope.Purpose) > 512 || len(scope.QueryKey) > 128 || len(scope.AnalysisKey) > 128 || report.ExportScope == nil && scope.TagMatch != "" && scope.TagMatch != "any" && scope.TagMatch != "all" {
		return scope, report, nil, exportScopeError("backend.report.export_scope_invalid")
	}
	if report.ObjectSQLV1 != nil {
		return normalizeObjectSQLExportScope(ctx, report, objectKey, control, scope, authorization)
	}
	if report.ExportScope == nil {
		var predicateErr error
		report, scope.QueryKey, scope.Tags, predicateErr = ApplyDeclaredPredicates(report, scope.QueryKey, scope.Tags, control.AllowedQueryKeys, control.AllowedTags, true)
		if predicateErr != nil {
			return scope, report, nil, predicateErr
		}
	}
	if report.ExportScope != nil {
		var scopeErr error
		scope, report, scopeErr = applyReportOwnedExportScope(scope, report)
		if scopeErr != nil {
			return scope, report, nil, scopeErr
		}
	}
	if report.Dataset.RuntimeTags != nil {
		aliases := reportmodel.ReportDatasetAliasObjects(report.Dataset)
		for _, alias := range report.Dataset.RuntimeTags.ScopeJoinAliases {
			if !reportExportControlOwnsSource(control, aliases[alias]) {
				return scope, report, nil, exportScopeError("backend.report.export_control_invalid")
			}
		}
	}

	for _, sourceObject := range reportmodel.ReportDatasetObjectKeys(report.Dataset) {
		if !reportExportControlOwnsSource(control, strings.TrimSpace(sourceObject)) {
			return scope, report, nil, exportScopeError("backend.report.export_control_invalid")
		}
	}
	if authorization == nil {
		return scope, report, nil, &apperror.AppError{Kind: apperror.KindInternal, Code: "backend.report.export_authorizer_unavailable"}
	}
	// The path-selected object is the exact export Permission boundary. Joined
	// sources are validated as control-owned, but never switch authorization to
	// another source object's export Permission.
	if err := authorization.AuthorizeReportExportSource(ctx, strings.TrimSpace(objectKey)); err != nil {
		return scope, report, nil, err
	}

	dimensions := map[string]reportmodel.ReportDatasetDimension{}
	for _, dimension := range report.Dataset.Dimensions {
		dimensions[dimension.Key] = dimension
	}
	measures := map[string]reportmodel.ReportDatasetMeasure{}
	for _, measure := range report.Dataset.Measures {
		measures[measure.Key] = measure
	}
	if scope.AnalysisKey != "" {
		found := false
		for _, analysis := range report.Dataset.Analyses {
			found = found || analysis.Key == scope.AnalysisKey
		}
		if !found {
			return scope, report, nil, exportScopeError("backend.report.export_analysis_not_allowed")
		}
	}
	if len(scope.Filters) > 32 {
		return scope, report, nil, exportScopeError("backend.report.export_scope_invalid")
	}
	for index := range scope.Filters {
		filter := &scope.Filters[index]
		filter.DimensionKey, filter.Operator = strings.TrimSpace(filter.DimensionKey), strings.TrimSpace(filter.Operator)
		dimension, ok := dimensions[filter.DimensionKey]
		if !ok || !validReportExportFilter(filter.Operator, len(filter.Values)) {
			return scope, report, nil, exportScopeError("backend.report.export_filter_not_allowed")
		}
		for valueIndex := range filter.Values {
			filter.Values[valueIndex] = strings.TrimSpace(filter.Values[valueIndex])
			if len(filter.Values[valueIndex]) > 1024 {
				return scope, report, nil, exportScopeError("backend.report.export_scope_invalid")
			}
		}
		datasetFilter := reportmodel.ReportDatasetFilter{Field: dimension.Field, Operator: filter.Operator}
		if len(filter.Values) == 1 {
			datasetFilter.Value = filter.Values[0]
		} else if len(filter.Values) > 0 {
			datasetFilter.Values = stringValuesAsAny(filter.Values)
		}
		report.Dataset.Filters = append(report.Dataset.Filters, datasetFilter)
	}
	scope.TimeZone = strings.TrimSpace(scope.TimeZone)
	if scope.TimeZone == "" {
		scope.TimeZone = strings.TrimSpace(report.Dataset.TimeZone)
		if scope.TimeZone == "" {
			scope.TimeZone = "UTC"
		}
	}
	location, locationErr := time.LoadLocation(scope.TimeZone)
	if locationErr != nil {
		return scope, report, nil, exportScopeError("backend.report.export_timezone_invalid")
	}
	report.Dataset.TimeZone = scope.TimeZone
	if scope.DateRange != nil {
		scope.DateRange.DimensionKey, scope.DateRange.From, scope.DateRange.To = strings.TrimSpace(scope.DateRange.DimensionKey), strings.TrimSpace(scope.DateRange.From), strings.TrimSpace(scope.DateRange.To)
		dimension, ok := dimensions[scope.DateRange.DimensionKey]
		from, fromErr := reportExportDateBoundary(scope.DateRange.From, location, false)
		to, toErr := reportExportDateBoundary(scope.DateRange.To, location, true)
		if !ok || strings.TrimSpace(dimension.TimeGrain) == "" || fromErr != nil || toErr != nil || from.After(to) {
			return scope, report, nil, exportScopeError("backend.report.export_date_range_invalid")
		}
		report.Dataset.Filters = append(report.Dataset.Filters, reportmodel.ReportDatasetFilter{Field: dimension.Field, Operator: "between", Values: []any{from.UTC().Format(time.RFC3339Nano), to.UTC().Format(time.RFC3339Nano)}})
	}
	if len(scope.FieldProjection) == 0 {
		for _, dimension := range report.Dataset.Dimensions {
			scope.FieldProjection = append(scope.FieldProjection, dimension.Key)
		}
		for _, measure := range report.Dataset.Measures {
			scope.FieldProjection = append(scope.FieldProjection, measure.Key)
		}
	} else {
		scope.FieldProjection = normalizeScopeProjection(scope.FieldProjection)
	}
	if len(scope.FieldProjection) == 0 || len(scope.FieldProjection) > 128 {
		return scope, report, nil, exportScopeError("backend.report.export_projection_not_allowed")
	}
	selectedDimensions := map[string]bool{}
	selectedMeasures := map[string]bool{}
	for _, key := range scope.FieldProjection {
		if _, ok := dimensions[key]; ok {
			selectedDimensions[key] = true
		} else {
			if _, ok := measures[key]; !ok {
				return scope, report, nil, exportScopeError("backend.report.export_projection_not_allowed")
			}
			selectedMeasures[key] = true
		}
	}
	// Projection is an execution boundary, not only a CSV column selector.
	// Leaving unprojected dimensions in the dataset would split groups on
	// hidden values and then emit duplicate visible rows with an incorrect
	// row_count. Filters remain in the scoped dataset, so a dimension may still
	// narrow source rows without becoming a grouping/output dimension.
	projectedDimensions := make([]reportmodel.ReportDatasetDimension, 0, len(selectedDimensions))
	for _, dimension := range report.Dataset.Dimensions {
		if selectedDimensions[dimension.Key] {
			projectedDimensions = append(projectedDimensions, dimension)
		}
	}
	projectedMeasures := make([]reportmodel.ReportDatasetMeasure, 0, len(selectedMeasures))
	for _, measure := range report.Dataset.Measures {
		if selectedMeasures[measure.Key] {
			projectedMeasures = append(projectedMeasures, measure)
		}
	}
	report.Dataset.Dimensions = projectedDimensions
	report.Dataset.Measures = projectedMeasures

	metricRefs := []reportmodel.ReportMetricDefinitionRef{}
	for _, measure := range report.Dataset.Measures {
		if selectedMeasures[measure.Key] {
			version, _ := CanonicalJSONSHA256(measure)
			metricRefs = append(metricRefs, reportmodel.ReportMetricDefinitionRef{Key: measure.Key, Version: "sha256:" + version})
		}
	}
	if len(request.MetricDefinitions) > 0 && !reportMetricDefinitionsEqual(request.MetricDefinitions, metricRefs) {
		return scope, report, nil, exportScopeError("backend.report.export_metric_definition_mismatch")
	}
	scope.MetricDefinitions = metricRefs

	scope.Freshness.Mode = strings.TrimSpace(scope.Freshness.Mode)
	if scope.Freshness.Mode == "" {
		scope.Freshness.Mode = "realtime"
	}
	if scope.Freshness.Mode != "realtime" && scope.Freshness.Mode != "snapshot" || scope.Freshness.MaximumLagSeconds < 0 || scope.Freshness.MaximumLagSeconds > 86400 {
		return scope, report, nil, exportScopeError("backend.report.export_freshness_invalid")
	}
	if scope.Freshness.Mode == "snapshot" && (scope.QueryKey != "" || len(scope.Tags) > 0 || len(scope.Filters) > 0 || scope.DateRange != nil || scope.TimeZone != originalTimeZone) {
		return scope, report, nil, exportScopeError("backend.report.export_snapshot_scope_unsupported")
	}

	aliases := reportmodel.ReportDatasetAliasObjects(report.Dataset)
	maskedDimensions := map[string]bool{}
	for _, dimension := range report.Dataset.Dimensions {
		object := aliases[dimension.Field.SourceAlias]
		masked, err := authorization.AuthorizeReportExportField(ctx, object, dimension.Field.FieldKey)
		if err != nil {
			return scope, report, nil, &apperror.AppError{Kind: apperror.KindForbidden, Code: "backend.report.export_field_denied", Err: err}
		}
		if masked {
			if !control.MaskingRequired {
				return scope, report, nil, &apperror.AppError{Kind: apperror.KindForbidden, Code: "backend.report.export_field_denied"}
			}
			maskedDimensions[dimension.Key] = true
		}
	}
	for _, measure := range report.Dataset.Measures {
		for _, field := range reportMeasureFields(measure) {
			object := aliases[field.SourceAlias]
			masked, err := authorization.AuthorizeReportExportField(ctx, object, field.FieldKey)
			if err != nil || masked {
				return scope, report, nil, &apperror.AppError{Kind: apperror.KindForbidden, Code: "backend.report.export_sensitive_measure_denied", Err: err}
			}
		}
	}
	return scope, report, maskedDimensions, nil
}

func normalizeObjectSQLExportScope(ctx context.Context, report reportmodel.ReportSchema, objectKey string, control reportmodel.ReportExportControlSchema, scope reportmodel.ReportExportScopeRequest, authorization Authorization) (reportmodel.ReportExportScopeRequest, reportmodel.ReportSchema, map[string]bool, error) {
	if report.ExportScope != nil || scope.QueryKey != "" || scope.AnalysisKey != "" || len(scope.Filters) > 0 || scope.DateRange != nil || strings.TrimSpace(scope.TimeZone) != "" || len(scope.Tags) > 0 || scope.TagMatch != "" || len(scope.MetricDefinitions) > 0 {
		return scope, report, nil, exportScopeError("backend.report.object_sql_export_scope_invalid")
	}
	for _, sourceObject := range reportmodel.ReportObjectSQLObjectKeys(report.ObjectSQLV1) {
		sourceObject = strings.TrimSpace(sourceObject)
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
	if len(scope.FieldProjection) == 0 {
		for _, column := range report.ObjectSQLV1.ResultSchema {
			scope.FieldProjection = append(scope.FieldProjection, column.Key)
			allowedColumns[column.Key] = true
		}
	} else {
		for _, column := range report.ObjectSQLV1.ResultSchema {
			allowedColumns[column.Key] = true
		}
		scope.FieldProjection = normalizeScopeProjection(scope.FieldProjection)
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
	_ = objectKey
	return scope, report, map[string]bool{}, nil
}

func ValidateFieldAccess(ctx context.Context, report reportmodel.ReportSchema, control reportmodel.ReportExportControlSchema, authorization Authorization) (map[string]bool, error) {
	if authorization == nil {
		return nil, &apperror.AppError{Kind: apperror.KindInternal, Code: "backend.report.export_authorizer_unavailable"}
	}
	if report.ObjectSQLV1 != nil {
		if err := authorization.AuthorizeReportObjectSQLExport(ctx, report); err != nil {
			return nil, err
		}
		return map[string]bool{}, nil
	}
	aliases := reportmodel.ReportDatasetAliasObjects(report.Dataset)
	maskedDimensions := map[string]bool{}
	check := func(field reportmodel.ReportDatasetField, maskedAllowed bool) (bool, error) {
		objectKey := aliases[field.SourceAlias]
		if objectKey == "" || strings.TrimSpace(field.FieldKey) == "" {
			return false, &apperror.AppError{Kind: apperror.KindBadRequest, Code: "backend.report.export_field_not_found"}
		}
		masked, err := authorization.AuthorizeReportExportField(ctx, objectKey, field.FieldKey)
		if err != nil {
			return false, err
		}
		if masked && (!maskedAllowed || !control.MaskingRequired) {
			return false, &apperror.AppError{Kind: apperror.KindForbidden, Code: "backend.report.export_sensitive_measure_denied"}
		}
		return masked, nil
	}
	for _, dimension := range report.Dataset.Dimensions {
		masked, err := check(dimension.Field, true)
		if err != nil {
			return nil, err
		}
		maskedDimensions[dimension.Key] = masked
	}
	for _, filter := range report.Dataset.Filters {
		if _, err := check(filter.Field, false); err != nil {
			return nil, err
		}
	}
	if report.Dataset.RuntimeQuery != nil {
		for _, predicate := range report.Dataset.RuntimeQuery.Predicates {
			if _, err := check(predicate.Field, false); err != nil {
				return nil, err
			}
		}
	}
	if report.Dataset.RuntimeTags != nil {
		if _, err := check(report.Dataset.RuntimeTags.TargetField, false); err != nil {
			return nil, err
		}
		if _, err := check(report.Dataset.RuntimeTags.TagField, false); err != nil {
			return nil, err
		}
	}
	for _, measure := range report.Dataset.Measures {
		for _, field := range reportMeasureFields(measure) {
			if _, err := check(field, false); err != nil {
				return nil, err
			}
		}
	}
	if report.Dataset.Privacy != nil {
		if _, err := check(report.Dataset.Privacy.EntityField, false); err != nil {
			return nil, err
		}
	}
	for _, analysis := range report.Dataset.Analyses {
		for _, field := range []reportmodel.ReportDatasetField{analysis.EntityField, analysis.TimeField} {
			if _, err := check(field, false); err != nil {
				return nil, err
			}
		}
		if analysis.EventField != nil {
			if _, err := check(*analysis.EventField, false); err != nil {
				return nil, err
			}
		}
	}
	for _, join := range report.Dataset.Joins {
		for _, equality := range join.Equalities() {
			if _, err := check(reportmodel.ReportDatasetField{SourceAlias: join.LeftAlias, FieldKey: equality.LeftField}, false); err != nil {
				return nil, err
			}
			if _, err := check(reportmodel.ReportDatasetField{SourceAlias: join.Alias, FieldKey: equality.RightField}, false); err != nil {
				return nil, err
			}
		}
	}
	return maskedDimensions, nil
}
