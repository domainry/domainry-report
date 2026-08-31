package report

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	reportsdk "github.com/domainry/domainry-report-sdk"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
	"github.com/domainry/domainry-report-sdk/modulehost"
	reportpersistence "github.com/domainry/domainry-report-sdk/persistence"
	reportengine "github.com/domainry/domainry-report/internal/domain/report/service/engine"
	reportobjectsql "github.com/domainry/domainry-report/internal/domain/report/service/objectsql"
	reportplan "github.com/domainry/domainry-report/internal/domain/report/service/plan"
)

type QueryService struct {
	subjects       modulehost.SubjectResolver
	definitions    DefinitionProvider
	datasets       modulehost.DatasetReader
	objectSQL      modulehost.ObjectSQLExecutor
	sourceVersions modulehost.SourceVersionReader
	audit          modulehost.ExecutionAudit
	snapshots      reportpersistence.SnapshotRepository
	cursorKey      []byte
	clock          func() time.Time
}

// DefinitionProvider is Report's own published-definition projection. It is
// deliberately not a modulehost port: a host synchronizes definitions through
// persistence.Binding, then Report reads and authorizes its own source state.
type DefinitionProvider interface {
	ReportDefinitions(context.Context) ([]reportmodel.ReportSchema, error)
}

func NewQueryService(host modulehost.ApplicationHost, definitions DefinitionProvider, snapshots reportpersistence.SnapshotRepository) *QueryService {
	if host == nil {
		return &QueryService{definitions: definitions, snapshots: snapshots, clock: time.Now}
	}
	clock := host.ReportClock()
	if clock == nil {
		clock = time.Now
	}
	return &QueryService{
		subjects: host.ReportSubjects(), definitions: definitions, datasets: host.ReportDatasets(),
		objectSQL: host.ReportObjectSQL(), sourceVersions: host.ReportSourceVersions(), audit: host.ReportExecutionAudit(),
		snapshots: snapshots, cursorKey: append([]byte(nil), host.ReportCursorSigningKey()...), clock: clock,
	}
}

func (s *QueryService) Summary(ctx context.Context, request reportmodel.ReportSummaryRequest, authority reportmodel.ReportAuthority) (reportmodel.ReportSummary, error) {
	subject, report, err := s.resolve(ctx, request.ReportKey, authority)
	if err != nil {
		return reportmodel.ReportSummary{}, err
	}
	mode := strings.TrimSpace(request.Mode)
	if mode == "" {
		mode = "realtime"
	}
	if mode == "snapshot" {
		if strings.TrimSpace(request.QueryKey) != "" || len(request.Tags) != 0 {
			return reportmodel.ReportSummary{}, reportError(400, "backend.report.snapshot_scope_unsupported", nil)
		}
		summary, err := s.readSnapshot(ctx, report, subject)
		if err != nil {
			return reportmodel.ReportSummary{}, err
		}
		return s.paginateImmutable(summary, request.Page, reportFingerprint(report, map[string]any{"mode": mode}, subject), summary.Snapshot.SnapshotID)
	}
	if mode != "realtime" {
		return reportmodel.ReportSummary{}, reportError(400, "backend.report.execution_mode_invalid", nil)
	}
	scoped := report
	if strings.TrimSpace(request.QueryKey) != "" || len(request.Tags) != 0 {
		if report.ObjectSQLV1 != nil {
			return reportmodel.ReportSummary{}, reportError(400, "backend.report.object_sql_scope_unsupported", nil)
		}
		scoped, err = reportengine.ApplyDeclaredPredicates(report, request.QueryKey, request.Tags)
		if err != nil {
			return reportmodel.ReportSummary{}, predicateApplicationError(err)
		}
	}
	fingerprint := reportFingerprint(report, map[string]any{"mode": mode, "query_key": strings.TrimSpace(request.QueryKey), "tags": request.Tags}, subject)
	summary, err := s.executeStablePage(ctx, report, request.Page, fingerprint, subject, func() (reportmodel.ReportSummary, error) {
		return s.execute(ctx, scoped, nil, subject)
	})
	if err != nil {
		return reportmodel.ReportSummary{}, err
	}
	if err := s.auditCrossWorkspace(ctx, report, summary, subject); err != nil {
		return reportmodel.ReportSummary{}, err
	}
	return summary, nil
}

func (s *QueryService) QueryObjectSQL(ctx context.Context, request reportmodel.ReportObjectSQLRequest, authority reportmodel.ReportAuthority) (reportmodel.ReportSummary, error) {
	subject, report, err := s.resolve(ctx, request.ReportKey, authority)
	if err != nil {
		return reportmodel.ReportSummary{}, err
	}
	if report.ObjectSQLV1 == nil {
		return reportmodel.ReportSummary{}, reportError(400, "backend.report.object_sql_not_enabled", nil)
	}
	normalized, err := reportobjectsql.NormalizeParameters(report.ObjectSQLV1.Parameters, request.Parameters)
	if err != nil {
		return reportmodel.ReportSummary{}, objectSQLApplicationError(err)
	}
	fingerprint := reportFingerprint(report, map[string]any{"parameters": normalized}, subject)
	summary, err := s.executeStableObjectSQLPage(ctx, report, normalized, request.Page, fingerprint, subject)
	if err != nil {
		return reportmodel.ReportSummary{}, err
	}
	if err := s.auditCrossWorkspace(ctx, report, summary, subject); err != nil {
		return reportmodel.ReportSummary{}, err
	}
	return summary, nil
}

func (s *QueryService) resolve(ctx context.Context, reportKey string, authority reportmodel.ReportAuthority) (reportmodel.ReportSubject, reportmodel.ReportSchema, error) {
	if s == nil || s.subjects == nil || s.definitions == nil {
		return reportmodel.ReportSubject{}, reportmodel.ReportSchema{}, reportError(500, "backend.report.execution_unavailable", nil)
	}
	subject, err := s.resolveSubject(ctx, authority)
	if err != nil {
		return reportmodel.ReportSubject{}, reportmodel.ReportSchema{}, err
	}
	reports, err := s.definitions.ReportDefinitions(ctx)
	if err != nil {
		return reportmodel.ReportSubject{}, reportmodel.ReportSchema{}, normalizeHostError(err, "backend.report.definition_read_failed")
	}
	reportKey = strings.TrimSpace(reportKey)
	for _, report := range reports {
		if strings.TrimSpace(report.Key) != reportKey {
			continue
		}
		if !reportVisibleToSubject(report, subject) {
			break
		}
		return subject, report, nil
	}
	return reportmodel.ReportSubject{}, reportmodel.ReportSchema{}, reportError(404, "backend.report.not_found", nil)
}

func reportVisibleToSubject(report reportmodel.ReportSchema, subject reportmodel.ReportSubject) bool {
	if len(report.AudienceRoles) > 0 {
		role := strings.TrimSpace(subject.Principal.RoleKey)
		allowed := false
		for _, candidate := range report.AudienceRoles {
			if role == strings.TrimSpace(candidate) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	if len(report.RequiredPermissions) > 0 && !subject.HasAllPermissions(report.RequiredPermissions) {
		return false
	}
	if reportmodel.ReportCrossWorkspaceAggregate(report) {
		return strings.TrimSpace(subject.Principal.RoleKey) == "superadmin" && len(report.RequiredPermissions) > 0
	}
	return true
}

func (s *QueryService) resolveSubject(ctx context.Context, authority reportmodel.ReportAuthority) (reportmodel.ReportSubject, error) {
	if s == nil || s.subjects == nil {
		return reportmodel.ReportSubject{}, reportError(500, "backend.report.execution_unavailable", nil)
	}
	if err := authority.Validate(); err != nil {
		return reportmodel.ReportSubject{}, reportError(401, "auth.token_required", err)
	}
	subject, err := s.subjects.ResolveReportSubject(ctx, authority)
	if err != nil {
		return reportmodel.ReportSubject{}, normalizeHostError(err, "backend.report.subject_resolution_failed")
	}
	if err := subject.Validate(); err != nil {
		return reportmodel.ReportSubject{}, reportError(403, "backend.workspace_scope_required", err)
	}
	return subject, nil
}

func (s *QueryService) execute(ctx context.Context, report reportmodel.ReportSchema, parameters map[string]any, subject reportmodel.ReportSubject) (reportmodel.ReportSummary, error) {
	if report.ObjectSQLV1 != nil {
		return s.executeObjectSQL(ctx, report, parameters, subject)
	}
	if s.datasets == nil {
		return reportmodel.ReportSummary{}, reportError(500, "backend.report.execution_unavailable", nil)
	}
	plan, err := reportplan.BuildReportDatasetPlan(report)
	if err != nil {
		return reportmodel.ReportSummary{}, datasetPlanApplicationError(err)
	}
	read, err := s.datasets.ReadReportDataset(ctx, reportmodel.ReportDatasetReadRequest{Report: report, Plan: plan, Subject: subject})
	if err != nil {
		return reportmodel.ReportSummary{}, normalizeHostError(err, "backend.report.query_failed")
	}
	rows := reportDatasetSourceRows(read, report.Dataset)
	for _, join := range report.Dataset.Joins {
		if err := reportengine.ValidateJoinedCardinality(rows, join); err != nil {
			return reportmodel.ReportSummary{}, reportError(409, "backend.report.join_cardinality_violated", err)
		}
	}
	filtered := rows[:0]
	for _, row := range rows {
		if reportengine.RowMatchesFilters(row, report.Dataset.Filters) {
			filtered = append(filtered, row)
		}
	}
	rows = reportengine.ApplyRuntimeQuery(filtered, report.Dataset.RuntimeQuery)
	rows = reportengine.ApplyRuntimeTags(rows, report.Dataset.RuntimeTags)
	analyses, err := reportengine.ExecuteAnalyses(rows, report.Dataset)
	if err != nil {
		return reportmodel.ReportSummary{}, reportError(400, "backend.report.analysis_invalid", err)
	}
	objects := make(map[string]reportengine.Object, len(read.Objects))
	for alias, source := range read.Objects {
		objects[alias] = reportEngineObject(source)
	}
	dataset := report.Dataset
	dataset.Measures = append([]reportmodel.ReportDatasetMeasure(nil), report.Dataset.Measures...)
	rootAlias := strings.TrimSpace(dataset.Source.Alias)
	for index := range dataset.Measures {
		if dataset.Measures[index].Operation == "count" && strings.TrimSpace(dataset.Measures[index].SourceAlias) == "" {
			dataset.Measures[index].SourceAlias = rootAlias
		}
	}
	aggregated, err := reportengine.Aggregate(rows, dataset, objects)
	if err != nil {
		var calculation *reportengine.CalculationError
		if errors.As(err, &calculation) && calculation.Stage == "comparison" {
			return reportmodel.ReportSummary{}, reportError(400, "backend.report.comparison_invalid", err)
		}
		return reportmodel.ReportSummary{}, reportError(400, "backend.report.execution_invalid", err)
	}
	return reportmodel.ReportSummary{Key: report.Key, Name: report.Name, Rows: aggregated.Rows, RowCount: len(aggregated.Rows), SourceRowCount: aggregated.VisibleSourceRows, Analyses: analyses, ExecutionMode: "realtime"}, nil
}

func reportDatasetSourceRows(read reportmodel.ReportDatasetReadResult, dataset reportmodel.ReportDatasetSchema) []reportengine.Row {
	if read.Records != nil {
		rootAlias := strings.TrimSpace(dataset.Source.Alias)
		root := read.Records[rootAlias]
		rows := make([]reportengine.Row, 0, len(root))
		for index := range root {
			record := reportEngineRecord(root[index])
			rows = append(rows, reportengine.Row{rootAlias: &record})
		}
		for _, join := range dataset.Joins {
			raw := read.Records[strings.TrimSpace(join.Alias)]
			records := make([]reportengine.Record, len(raw))
			for index := range raw {
				records[index] = reportEngineRecord(raw[index])
			}
			rows = reportengine.JoinRows(rows, records, join)
		}
		return rows
	}
	rows := make([]reportengine.Row, 0, len(read.Rows))
	for _, sourceRow := range read.Rows {
		row := make(reportengine.Row, len(sourceRow))
		for alias, source := range sourceRow {
			if source == nil {
				row[alias] = nil
				continue
			}
			record := reportEngineRecord(*source)
			row[alias] = &record
		}
		rows = append(rows, row)
	}
	return rows
}

func reportEngineRecord(source reportmodel.ReportSourceRecord) reportengine.Record {
	return reportengine.Record{ID: source.ID, Data: source.Data, CreatedAt: source.CreatedAt, UpdatedAt: source.UpdatedAt}
}

func (s *QueryService) executeObjectSQL(ctx context.Context, report reportmodel.ReportSchema, parameters map[string]any, subject reportmodel.ReportSubject) (reportmodel.ReportSummary, error) {
	summary, _, err := s.executeObjectSQLPage(ctx, report, parameters, subject, "", 0, 0)
	return summary, err
}

func (s *QueryService) executeObjectSQLPage(ctx context.Context, report reportmodel.ReportSchema, parameters map[string]any, subject reportmodel.ReportSubject, pageCursor string, pagePosition, pageSize int) (reportmodel.ReportSummary, reportmodel.ReportObjectSQLExecutionResult, error) {
	if s.objectSQL == nil || report.ObjectSQLV1 == nil {
		return reportmodel.ReportSummary{}, reportmodel.ReportObjectSQLExecutionResult{}, reportError(500, "backend.report.object_sql_execution_unavailable", nil)
	}
	plan, err := s.compileAuthorizedObjectSQL(ctx, report, subject)
	if err != nil {
		return reportmodel.ReportSummary{}, reportmodel.ReportObjectSQLExecutionResult{}, err
	}
	normalized, err := reportobjectsql.NormalizeDeclaredParameters(plan.Parameters, parameters)
	if err != nil {
		return reportmodel.ReportSummary{}, reportmodel.ReportObjectSQLExecutionResult{}, objectSQLApplicationError(err)
	}
	timeout := time.Duration(report.ObjectSQLV1.TimeoutMilliseconds) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	result, err := s.objectSQL.ExecuteReportObjectSQL(ctx, reportmodel.ReportObjectSQLExecutionRequest{Report: report, Plan: plan, Parameters: normalized, Subject: subject, Timeout: timeout, PageCursor: pageCursor, PagePosition: pagePosition, PageSize: pageSize})
	if err != nil {
		return reportmodel.ReportSummary{}, reportmodel.ReportObjectSQLExecutionResult{}, normalizeHostError(err, "backend.report.object_sql_query_failed")
	}
	rows := make([]reportmodel.ReportResultRow, 0, len(result.Rows))
	for _, values := range result.Rows {
		row := reportmodel.ReportResultRow{Dimensions: map[string]string{}, Measures: map[string]string{}}
		for _, column := range plan.ResultSchema {
			value, exists := values[column.Key]
			if !exists {
				continue
			}
			if column.Kind == "measure" {
				row.Measures[column.Key] = value
			} else {
				row.Dimensions[column.Key] = value
			}
		}
		if len(row.Dimensions) == 0 {
			row.Dimensions = nil
		}
		if len(row.Measures) == 0 {
			row.Measures = nil
		}
		rows = append(rows, row)
	}
	return reportmodel.ReportSummary{Key: report.Key, Name: report.Name, Rows: rows, RowCount: len(rows), SourceRowCount: -1, ExecutionMode: "object_sql_v1", ResultSchema: append([]reportmodel.ReportResultColumnSchema(nil), plan.ResultSchema...)}, result, nil
}

func (s *QueryService) compileAuthorizedObjectSQL(ctx context.Context, report reportmodel.ReportSchema, subject reportmodel.ReportSubject) (reportmodel.ReportObjectSQLPlan, error) {
	if s == nil || s.objectSQL == nil || report.ObjectSQLV1 == nil {
		return reportmodel.ReportObjectSQLPlan{}, reportError(500, "backend.report.object_sql_execution_unavailable", nil)
	}
	sources, err := s.objectSQL.ResolveReportObjectSQLSources(ctx, report, subject)
	if err != nil {
		return reportmodel.ReportObjectSQLPlan{}, normalizeHostError(err, "backend.report.object_sql_source_resolution_failed")
	}
	objects := make(map[string]reportengine.Object, len(sources))
	for key, source := range sources {
		objects[key] = reportEngineObject(source)
	}
	plan, err := reportobjectsql.CompileReportObjectSQL(*report.ObjectSQLV1, objects)
	if err != nil {
		return reportmodel.ReportObjectSQLPlan{}, objectSQLApplicationError(err)
	}
	if reportmodel.ReportCrossWorkspaceAggregate(report) && !reportobjectsql.SafeCrossWorkspaceAggregatePlan(plan) {
		return reportmodel.ReportObjectSQLPlan{}, reportError(403, "backend.report.cross_workspace_raw_projection_forbidden", nil)
	}
	if err := s.objectSQL.AuthorizeReportObjectSQLPlan(ctx, report, plan, subject); err != nil {
		return reportmodel.ReportObjectSQLPlan{}, normalizeHostError(err, "backend.report.object_sql_field_authorization_failed")
	}
	return plan, nil
}

func reportEngineObject(source reportmodel.ReportSourceObject) reportengine.Object {
	fields := make([]reportengine.Field, 0, len(source.Fields))
	for _, field := range source.Fields {
		fields = append(fields, reportengine.Field{Key: field.Key, Type: field.Type, Precision: field.Precision, Scale: field.Scale})
	}
	return reportengine.Object{Key: source.Key, Fields: fields}
}

func (s *QueryService) readSnapshot(ctx context.Context, report reportmodel.ReportSchema, subject reportmodel.ReportSubject) (reportmodel.ReportSummary, error) {
	if report.Materialization == nil {
		return reportmodel.ReportSummary{}, reportError(400, "backend.report.materialization_not_enabled", nil)
	}
	if s.snapshots == nil {
		return reportmodel.ReportSummary{}, reportError(500, "backend.report.snapshot_unavailable", nil)
	}
	snapshot, found, err := s.snapshots.Latest(ctx, subject.Principal.WorkspaceID, report.Key, subject.AccessScopeHash)
	if err != nil {
		return reportmodel.ReportSummary{}, reportError(500, "backend.report.snapshot_read_failed", err)
	}
	if !found {
		return reportmodel.ReportSummary{}, reportError(404, "backend.report.snapshot_not_found", nil)
	}
	var summary reportmodel.ReportSummary
	if err := json.Unmarshal(snapshot.Summary, &summary); err != nil {
		return reportmodel.ReportSummary{}, reportError(500, "backend.report.snapshot_invalid", err)
	}
	refreshedAt, err := time.Parse(time.RFC3339Nano, snapshot.RefreshedAt)
	if err != nil {
		return reportmodel.ReportSummary{}, reportError(500, "backend.report.snapshot_invalid", err)
	}
	lag := s.clock().UTC().Sub(refreshedAt)
	if lag < 0 {
		lag = 0
	}
	summary.ExecutionMode = "snapshot"
	summary.Snapshot = &reportmodel.ReportSnapshotFreshness{SnapshotID: snapshot.ID, Watermark: snapshot.Watermark, SourceVersions: snapshot.SourceVersions, RefreshedAt: snapshot.RefreshedAt, LagSeconds: int64(lag / time.Second), Stale: report.Materialization.MaximumLagSeconds > 0 && lag > time.Duration(report.Materialization.MaximumLagSeconds)*time.Second}
	return summary, nil
}

type stablePageCursor struct {
	Version           int    `json:"v"`
	Fingerprint       string `json:"fingerprint"`
	SourceVersionHash string `json:"source_version_sha256"`
	LastRowKey        string `json:"last_row_key"`
	Occurrence        int    `json:"occurrence"`
	ExecutionCursor   string `json:"execution_cursor,omitempty"`
	Position          int    `json:"position,omitempty"`
	Checksum          string `json:"checksum"`
}

func (s *QueryService) executeStablePage(ctx context.Context, report reportmodel.ReportSchema, page reportmodel.ReportPageRequest, fingerprint string, subject reportmodel.ReportSubject, execute func() (reportmodel.ReportSummary, error)) (reportmodel.ReportSummary, error) {
	if s.sourceVersions == nil || len(s.cursorKey) == 0 {
		return reportmodel.ReportSummary{}, reportError(500, "backend.report.pagination_unavailable", nil)
	}
	continued := strings.TrimSpace(page.Cursor) != ""
	var cursor stablePageCursor
	var err error
	if continued {
		cursor, err = s.decodePageCursor(page.Cursor, fingerprint)
		if err != nil {
			return reportmodel.ReportSummary{}, err
		}
	}
	for attempt := 0; attempt < 3; attempt++ {
		before, err := s.sourceVersions.ReadReportSourceVersion(ctx, report, subject)
		if err != nil {
			return reportmodel.ReportSummary{}, normalizeHostError(err, "backend.report.source_version_failed")
		}
		beforeHash := canonicalHash(before)
		if continued && cursor.SourceVersionHash != beforeHash {
			return reportmodel.ReportSummary{}, reportError(409, "backend.report.cursor_stale", nil)
		}
		summary, err := execute()
		if err != nil {
			return reportmodel.ReportSummary{}, err
		}
		after, err := s.sourceVersions.ReadReportSourceVersion(ctx, report, subject)
		if err != nil {
			return reportmodel.ReportSummary{}, normalizeHostError(err, "backend.report.source_version_failed")
		}
		if canonicalHash(after) == beforeHash {
			return s.paginate(summary, page, fingerprint, beforeHash, cursor)
		}
		if continued {
			return reportmodel.ReportSummary{}, reportError(409, "backend.report.cursor_stale", nil)
		}
	}
	return reportmodel.ReportSummary{}, reportError(409, "backend.report.source_changed", nil)
}

func (s *QueryService) executeStableObjectSQLPage(ctx context.Context, report reportmodel.ReportSchema, parameters map[string]any, page reportmodel.ReportPageRequest, fingerprint string, subject reportmodel.ReportSubject) (reportmodel.ReportSummary, error) {
	if s.sourceVersions == nil || len(s.cursorKey) == 0 {
		return reportmodel.ReportSummary{}, reportError(500, "backend.report.pagination_unavailable", nil)
	}
	pageSize := page.PageSize
	if pageSize == 0 {
		pageSize = reportmodel.ReportPageDefaultSize
	}
	if pageSize < 1 || pageSize > reportmodel.ReportPageMaximumSize {
		return reportmodel.ReportSummary{}, reportError(400, "backend.report.page_size_invalid", nil)
	}
	continued := strings.TrimSpace(page.Cursor) != ""
	cursor := stablePageCursor{}
	var err error
	if continued {
		cursor, err = s.decodeObjectSQLPageCursor(page.Cursor, fingerprint)
		if err != nil {
			return reportmodel.ReportSummary{}, err
		}
	}
	for attempt := 0; attempt < 3; attempt++ {
		before, err := s.sourceVersions.ReadReportSourceVersion(ctx, report, subject)
		if err != nil {
			return reportmodel.ReportSummary{}, normalizeHostError(err, "backend.report.source_version_failed")
		}
		beforeHash := canonicalHash(before)
		if continued && cursor.SourceVersionHash != beforeHash {
			return reportmodel.ReportSummary{}, reportError(409, "backend.report.cursor_stale", nil)
		}
		summary, execution, err := s.executeObjectSQLPage(ctx, report, parameters, subject, cursor.ExecutionCursor, cursor.Position, pageSize)
		if err != nil {
			return reportmodel.ReportSummary{}, err
		}
		if len(summary.Rows) > pageSize || execution.HasMore && (len(summary.Rows) == 0 || strings.TrimSpace(execution.NextCursor) == "") {
			return reportmodel.ReportSummary{}, reportError(409, "backend.report.pagination_contract_invalid", nil)
		}
		after, err := s.sourceVersions.ReadReportSourceVersion(ctx, report, subject)
		if err != nil {
			return reportmodel.ReportSummary{}, normalizeHostError(err, "backend.report.source_version_failed")
		}
		if canonicalHash(after) != beforeHash {
			if continued {
				return reportmodel.ReportSummary{}, reportError(409, "backend.report.cursor_stale", nil)
			}
			continue
		}
		position := cursor.Position + len(summary.Rows)
		summary.PageSize, summary.RowCount = pageSize, len(summary.Rows)
		summary.Truncated, summary.ExecutionCursor, summary.NextCursor = execution.HasMore, execution.NextCursor, ""
		if execution.TotalKnown {
			if execution.Total < position {
				return reportmodel.ReportSummary{}, reportError(409, "backend.report.pagination_contract_invalid", nil)
			}
			summary.Total, summary.TotalSemantics = execution.Total, reportmodel.ReportTotalExact
		} else {
			summary.Total, summary.TotalSemantics = position, reportmodel.ReportTotalAtLeast
			if execution.HasMore {
				summary.Total++
			}
		}
		if execution.HasMore {
			summary.NextCursor = s.encodePageCursor(stablePageCursor{Version: 2, Fingerprint: fingerprint, SourceVersionHash: beforeHash, ExecutionCursor: execution.NextCursor, Position: position})
		}
		return summary, nil
	}
	return reportmodel.ReportSummary{}, reportError(409, "backend.report.source_changed", nil)
}

func (s *QueryService) paginateImmutable(summary reportmodel.ReportSummary, page reportmodel.ReportPageRequest, fingerprint, version string) (reportmodel.ReportSummary, error) {
	var cursor stablePageCursor
	if strings.TrimSpace(page.Cursor) != "" {
		decoded, err := s.decodePageCursor(page.Cursor, fingerprint)
		if err != nil {
			return reportmodel.ReportSummary{}, err
		}
		if decoded.SourceVersionHash != version {
			return reportmodel.ReportSummary{}, reportError(409, "backend.report.cursor_stale", nil)
		}
		cursor = decoded
	}
	return s.paginate(summary, page, fingerprint, version, cursor)
}

func (s *QueryService) paginate(summary reportmodel.ReportSummary, page reportmodel.ReportPageRequest, fingerprint, version string, cursor stablePageCursor) (reportmodel.ReportSummary, error) {
	pageSize := page.PageSize
	if pageSize == 0 {
		pageSize = reportmodel.ReportPageDefaultSize
	}
	if pageSize < 1 || pageSize > reportmodel.ReportPageMaximumSize {
		return reportmodel.ReportSummary{}, reportError(400, "backend.report.page_size_invalid", nil)
	}
	allRows := summary.Rows
	start := 0
	if cursor.Version != 0 {
		start = rowAfterCursor(allRows, cursor)
		if start < 0 {
			return reportmodel.ReportSummary{}, reportError(409, "backend.report.cursor_stale", nil)
		}
	}
	total := len(allRows)
	end := start + pageSize
	if end > total {
		end = total
	}
	rows := make([]reportmodel.ReportResultRow, end-start)
	copy(rows, allRows[start:end])
	summary.Rows, summary.RowCount, summary.PageSize = rows, len(rows), pageSize
	summary.Total, summary.TotalSemantics, summary.Truncated, summary.NextCursor = total, reportmodel.ReportTotalExact, end < total, ""
	if summary.Truncated && len(rows) != 0 {
		key := stableRowKey(rows[len(rows)-1])
		occurrence := 0
		for index := 0; index < end; index++ {
			if stableRowKey(allRows[index]) == key {
				occurrence++
			}
		}
		summary.NextCursor = s.encodePageCursor(stablePageCursor{Version: 1, Fingerprint: fingerprint, SourceVersionHash: version, LastRowKey: key, Occurrence: occurrence})
	}
	return summary, nil
}

func rowAfterCursor(rows []reportmodel.ReportResultRow, cursor stablePageCursor) int {
	occurrence := 0
	for index, row := range rows {
		if stableRowKey(row) != cursor.LastRowKey {
			continue
		}
		occurrence++
		if occurrence == cursor.Occurrence {
			return index + 1
		}
	}
	return -1
}

func stableRowKey(row reportmodel.ReportResultRow) string { return canonicalHash(row) }

func (s *QueryService) encodePageCursor(cursor stablePageCursor) string {
	cursor.Checksum = s.pageCursorChecksum(cursor)
	content, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(content)
}

func (s *QueryService) decodePageCursor(value, fingerprint string) (stablePageCursor, error) {
	content, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return stablePageCursor{}, reportError(400, "backend.report.cursor_invalid", err)
	}
	var cursor stablePageCursor
	if err := json.Unmarshal(content, &cursor); err != nil || cursor.Version != 1 || cursor.Fingerprint != fingerprint || cursor.SourceVersionHash == "" || cursor.LastRowKey == "" || cursor.Occurrence < 1 || !hmac.Equal([]byte(cursor.Checksum), []byte(s.pageCursorChecksum(cursor))) {
		return stablePageCursor{}, reportError(400, "backend.report.cursor_invalid", err)
	}
	return cursor, nil
}

func (s *QueryService) decodeObjectSQLPageCursor(value, fingerprint string) (stablePageCursor, error) {
	content, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return stablePageCursor{}, reportError(400, "backend.report.cursor_invalid", err)
	}
	var cursor stablePageCursor
	if err := json.Unmarshal(content, &cursor); err != nil || cursor.Version != 2 || cursor.Fingerprint != fingerprint || cursor.SourceVersionHash == "" || strings.TrimSpace(cursor.ExecutionCursor) == "" || cursor.Position < 1 || !hmac.Equal([]byte(cursor.Checksum), []byte(s.pageCursorChecksum(cursor))) {
		return stablePageCursor{}, reportError(400, "backend.report.cursor_invalid", err)
	}
	return cursor, nil
}

func (s *QueryService) pageCursorChecksum(cursor stablePageCursor) string {
	mac := hmac.New(sha256.New, s.cursorKey)
	if cursor.Version == 2 {
		_, _ = mac.Write([]byte(fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%d", cursor.Version, cursor.Fingerprint, cursor.SourceVersionHash, cursor.ExecutionCursor, cursor.Position)))
	} else {
		_, _ = mac.Write([]byte(fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%d", cursor.Version, cursor.Fingerprint, cursor.SourceVersionHash, cursor.LastRowKey, cursor.Occurrence)))
	}
	return hex.EncodeToString(mac.Sum(nil))
}

func reportFingerprint(report reportmodel.ReportSchema, selectors any, subject reportmodel.ReportSubject) string {
	return canonicalHash(map[string]any{"report": report, "selectors": selectors, "access_scope_sha256": subject.AccessScopeHash})
}

func canonicalHash(value any) string {
	content, _ := json.Marshal(value)
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func (s *QueryService) auditCrossWorkspace(ctx context.Context, report reportmodel.ReportSchema, summary reportmodel.ReportSummary, subject reportmodel.ReportSubject) error {
	if !reportmodel.ReportCrossWorkspaceAggregate(report) {
		return nil
	}
	if s.audit == nil {
		return reportError(500, "backend.report.execution_audit_unavailable", nil)
	}
	if err := s.audit.AppendReportExecution(ctx, report, summary, subject); err != nil {
		return normalizeHostError(err, "backend.report.execution_audit_failed")
	}
	return nil
}

func reportError(status int, code string, cause error) error {
	return &reportsdk.Error{StatusCode: status, Code: code, Cause: cause}
}

func normalizeHostError(err error, code string) error {
	var stable *reportsdk.Error
	if errors.As(err, &stable) {
		return err
	}
	return reportError(500, code, err)
}

func datasetPlanApplicationError(err error) error {
	var planErr *reportmodel.ReportDatasetPlanError
	if errors.As(err, &planErr) {
		return &reportsdk.Error{StatusCode: 400, Code: planErr.Code, Params: planErr.Params, Cause: planErr}
	}
	return reportError(400, "backend.report.dataset_invalid", err)
}

func objectSQLApplicationError(err error) error {
	var planErr *reportmodel.ReportObjectSQLPlanError
	if errors.As(err, &planErr) {
		return &reportsdk.Error{StatusCode: 400, Code: planErr.Code, Params: planErr.Params, Cause: planErr}
	}
	return reportError(400, "backend.report.object_sql_invalid", err)
}

func predicateApplicationError(err error) error {
	var predicate *reportengine.PredicateError
	if errors.As(err, &predicate) {
		return reportError(400, predicate.Code, predicate)
	}
	return reportError(400, "backend.report.predicate_invalid", err)
}

var _ reportsdk.Queries = (*QueryService)(nil)
