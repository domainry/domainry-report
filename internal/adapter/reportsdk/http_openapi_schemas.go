package reportsdk

import reportmodel "github.com/domainry/domainry-report-sdk/model"

func reportObjectSQLParametersOpenAPISchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": map[string]any{"type": []string{"string", "number", "integer", "boolean", "null"}}}
}

func reportSummaryOpenAPISchema() map[string]any {
	stringMap := map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}
	row := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"dimensions": stringMap, "measures": stringMap}}
	column := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"key", "type", "kind"}, "properties": map[string]any{
		"key": map[string]any{"type": "string"}, "type": map[string]any{"type": "string"}, "kind": map[string]any{"type": "string", "enum": []string{"dimension", "measure"}},
		"precision": map[string]any{"type": "integer"}, "scale": map[string]any{"type": "integer"},
	}}
	return map[string]any{"type": "object", "additionalProperties": false,
		"required": []string{"key", "rows", "row_count", "source_row_count", "execution_mode", "page_size", "truncated", "total", "total_semantics"},
		"properties": map[string]any{
			"key": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "rows": map[string]any{"type": "array", "items": row},
			"row_count": map[string]any{"type": "integer"}, "source_row_count": map[string]any{"type": "integer"}, "execution_mode": map[string]any{"type": "string"},
			"result_schema": map[string]any{"type": "array", "items": column}, "page_size": map[string]any{"type": "integer", "minimum": 1, "maximum": reportmodel.ReportPageMaximumSize},
			"next_cursor": map[string]any{"type": "string"}, "truncated": map[string]any{"type": "boolean"}, "total": map[string]any{"type": "integer", "minimum": 0},
			"total_semantics": map[string]any{"type": "string", "enum": []string{reportmodel.ReportTotalExact, reportmodel.ReportTotalAtLeast}},
		},
	}
}

func reportSnapshotOpenAPISchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"id", "workspace_id", "report_key", "idempotency_key", "status", "started_at"}, "properties": map[string]any{
		"id": map[string]any{"type": "string"}, "workspace_id": map[string]any{"type": "string"}, "report_key": map[string]any{"type": "string"},
		"idempotency_key": map[string]any{"type": "string"}, "status": map[string]any{"type": "string"}, "summary": reportSummaryOpenAPISchema(),
		"watermark": map[string]any{"type": "string"}, "source_versions": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		"started_at": map[string]any{"type": "string", "format": "date-time"}, "refreshed_at": map[string]any{"type": "string", "format": "date-time"}, "error_code": map[string]any{"type": "string"},
	}}
}

func reportExportScopeOpenAPISchema() map[string]any {
	filter := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"dimension_key", "operator"}, "properties": map[string]any{
		"dimension_key": map[string]any{"type": "string"}, "operator": map[string]any{"type": "string", "enum": []string{"eq", "ne", "gt", "gte", "lt", "lte", "in", "not_in", "between", "is_null", "not_null"}},
		"values": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	}}
	dateRange := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"dimension_key", "from", "to"}, "properties": map[string]any{"dimension_key": map[string]any{"type": "string"}, "from": map[string]any{"type": "string"}, "to": map[string]any{"type": "string"}}}
	metric := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"key", "version"}, "properties": map[string]any{"key": map[string]any{"type": "string"}, "version": map[string]any{"type": "string"}}}
	freshness := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"mode"}, "properties": map[string]any{"mode": map[string]any{"type": "string", "enum": []string{"realtime", "snapshot"}}, "snapshot_id": map[string]any{"type": "string"}, "maximum_lag_seconds": map[string]any{"type": "integer", "format": "int64", "minimum": 0, "maximum": 86400}}}
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"purpose", "freshness"}, "properties": map[string]any{
		"parameters": reportObjectSQLParametersOpenAPISchema(), "query_key": map[string]any{"type": "string"}, "analysis_key": map[string]any{"type": "string"},
		"filters": map[string]any{"type": "array", "items": filter}, "date_range": dateRange, "timezone": map[string]any{"type": "string"},
		"tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "tag_match": map[string]any{"type": "string"},
		"field_projection": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "purpose": map[string]any{"type": "string"},
		"metric_definitions": map[string]any{"type": "array", "items": metric}, "freshness": freshness,
	}}
}

func reportExportJobOpenAPISchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false,
		"required": []string{"id", "audit_id", "report_key", "object_key", "status", "pages_completed", "rows_exported", "total", "scope", "created_at", "updated_at"},
		"properties": map[string]any{
			"id": map[string]any{"type": "string"}, "audit_id": map[string]any{"type": "string"},
			"report_key": map[string]any{"type": "string"}, "object_key": map[string]any{"type": "string"}, "status": map[string]any{"type": "string", "enum": []string{"accepted", "running", "completed", "failed", "cancelled"}},
			"pages_completed": map[string]any{"type": "integer"}, "rows_exported": map[string]any{"type": "integer"}, "total": map[string]any{"type": "integer"}, "scope": reportExportScopeOpenAPISchema(),
			"artifact_id": map[string]any{"type": "string"}, "content_sha256": map[string]any{"type": "string"}, "expires_at": map[string]any{"type": "string", "format": "date-time"},
			"error_code": map[string]any{"type": "string"}, "created_at": map[string]any{"type": "string", "format": "date-time"}, "updated_at": map[string]any{"type": "string", "format": "date-time"},
		},
	}
}
