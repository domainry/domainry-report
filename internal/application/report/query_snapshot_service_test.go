package report

import (
	"context"
	"errors"
	"testing"
	"time"

	identitysdk "github.com/domainry/domainry-identity-sdk"
	notificationmodel "github.com/domainry/domainry-notification-sdk/contract"
	reportsdk "github.com/domainry/domainry-report-sdk"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
	reportpersistence "github.com/domainry/domainry-report-sdk/persistence"
)

type applicationTestSubjects struct{ subject reportmodel.ReportSubject }

func (s applicationTestSubjects) ResolveReportSubject(context.Context, reportmodel.ReportAuthority) (reportmodel.ReportSubject, error) {
	return s.subject, nil
}

type applicationTestDefinitions struct{ reports []reportmodel.ReportSchema }

func (d applicationTestDefinitions) ReportDefinitions(context.Context) ([]reportmodel.ReportSchema, error) {
	return append([]reportmodel.ReportSchema(nil), d.reports...), nil
}

type applicationTestExportDefinitions struct {
	control reportmodel.ReportExportControlSchema
	found   bool
}

func (d applicationTestExportDefinitions) ReportExportControl(context.Context, string, string) (reportmodel.ReportExportControlSchema, bool, error) {
	return d.control, d.found, nil
}

type applicationTestExportGateway struct {
	report  reportmodel.ReportSchema
	control reportmodel.ReportExportControlSchema
	request reportmodel.ReportExportPrepareRequest
}

type applicationTestExportAuthorization struct{}

func (applicationTestExportAuthorization) AuthorizeReportExportSource(context.Context, string, reportmodel.ReportSubject) (string, error) {
	return "all_records", nil
}

func (applicationTestExportAuthorization) AuthorizeReportExportField(context.Context, string, string, reportmodel.ReportSubject) (bool, error) {
	return false, nil
}

func (g *applicationTestExportGateway) PrepareReportExport(_ context.Context, request reportmodel.ReportExportPrepareRequest, report reportmodel.ReportSchema, control reportmodel.ReportExportControlSchema, _ reportmodel.ReportSubject) (reportmodel.ReportExportJob, error) {
	g.request, g.report, g.control = request, report, control
	return reportmodel.ReportExportJob{ID: "job-1"}, nil
}

func TestExportServicePassesReportOwnedDefinitionsToHostGateway(t *testing.T) {
	report := applicationTestReport(false)
	control := reportmodel.ReportExportControlSchema{Key: "sales-export", ReportKey: report.Key, SourceObjects: []string{"event"}}
	queries := &QueryService{subjects: applicationTestSubjects{subject: applicationTestSubject()}, definitions: applicationTestDefinitions{reports: []reportmodel.ReportSchema{report}}}
	gateway := &applicationTestExportGateway{}
	service := NewExportService(queries, applicationTestExportDefinitions{control: control, found: true}, applicationTestExportAuthorization{}, gateway)
	request := reportmodel.ReportExportPrepareRequest{ReportKey: report.Key, ObjectKey: "event", AuditID: "audit-1", IdempotencyKey: "request-1", Scope: reportmodel.ReportExportScopeRequest{Purpose: "evidence"}}
	job, err := service.Prepare(t.Context(), request, reportmodel.ReportAuthority{AccessToken: "token"})
	if err != nil || job.ID != "job-1" {
		t.Fatalf("job=%#v err=%v", job, err)
	}
	if gateway.report.Key != report.Key || gateway.control.Key != control.Key || gateway.request.AuditID != "audit-1" {
		t.Fatalf("gateway report=%#v control=%#v request=%#v", gateway.report, gateway.control, gateway.request)
	}
	_, err = service.ResolveExecution(t.Context(), reportmodel.ReportExportExecutionRequest{ReportKey: report.Key, ObjectKey: "missing", Scope: request.Scope}, reportmodel.ReportAuthority{AccessToken: "token"})
	var stable *reportsdk.Error
	if !errors.As(err, &stable) || stable.Code != "backend.report.object_not_in_report" {
		t.Fatalf("object ownership err=%v", err)
	}
}

func TestExportServiceOwnsWorkerResolutionPagingAndSourceVersion(t *testing.T) {
	report := applicationTestReport(false)
	control := reportmodel.ReportExportControlSchema{Key: "sales-export", ReportKey: report.Key, SourceObjects: []string{"event"}, MaxRows: 100}
	queries := &QueryService{
		subjects: applicationTestSubjects{subject: applicationTestSubject()}, definitions: applicationTestDefinitions{reports: []reportmodel.ReportSchema{report}},
		datasets: applicationTestDataset(), sourceVersions: &applicationTestVersions{version: "version-1"}, cursorKey: []byte("report-export-cursor-key"), clock: time.Now,
	}
	service := NewExportService(queries, applicationTestExportDefinitions{control: control, found: true}, applicationTestExportAuthorization{}, &applicationTestExportGateway{})
	authority := reportmodel.ReportAuthority{AccessToken: "token"}
	request := reportmodel.ReportExportExecutionRequest{
		ReportKey: report.Key, ObjectKey: "event",
		Scope: reportmodel.ReportExportScopeRequest{Purpose: "evidence", FieldProjection: []string{"category", "count"}, Freshness: reportmodel.ReportExportFreshness{Mode: "realtime"}},
		Page:  reportmodel.ReportPageRequest{PageSize: 2},
	}
	resolved, err := service.ResolveExecution(t.Context(), request, authority)
	if err != nil || resolved.Definition.Report.Key != report.Key || resolved.Definition.Control.Key != control.Key || resolved.Scope.RoleKey != "manager" || resolved.Scope.DataScopes["event"] != "all_records" {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	first, err := service.ReadPage(t.Context(), request, authority)
	if err != nil || len(first.Rows) != 2 || !first.Truncated || first.NextCursor == "" || len(first.Analyses) != 0 {
		t.Fatalf("first export page=%#v err=%v", first, err)
	}
	request.Page.Cursor = first.NextCursor
	second, err := service.ReadPage(t.Context(), request, authority)
	if err != nil || len(second.Rows) != 1 || second.Truncated || second.NextCursor != "" {
		t.Fatalf("second export page=%#v err=%v", second, err)
	}
	version, err := service.SourceVersion(t.Context(), request, authority)
	if err != nil || version.Watermark != "version-1" {
		t.Fatalf("source version=%#v err=%v", version, err)
	}
}

func TestQueryServiceJoinsAuthorizedRecordCollectionsInsideReport(t *testing.T) {
	dataset := reportmodel.ReportDatasetSchema{
		Source: reportmodel.ReportDatasetSource{ObjectKey: "order", Alias: "orders"},
		Joins:  []reportmodel.ReportDatasetJoin{{Alias: "customers", ObjectKey: "customer", Type: "inner", LeftAlias: "orders", LeftField: "customer_id", RightField: "id", Cardinality: "many_to_one"}},
	}
	read := reportmodel.ReportDatasetReadResult{Records: map[string][]reportmodel.ReportSourceRecord{
		"orders":    {{ID: "order-1", Data: map[string]any{"customer_id": "customer-1"}}},
		"customers": {{ID: "customer-1", Data: map[string]any{"name": "Acme"}}},
	}}
	rows := reportDatasetSourceRows(read, dataset)
	if len(rows) != 1 || rows[0]["orders"] == nil || rows[0]["customers"] == nil || rows[0]["customers"].Data["name"] != "Acme" {
		t.Fatalf("joined rows=%#v", rows)
	}
}

type applicationTestDatasets struct {
	result reportmodel.ReportDatasetReadResult
	reads  int
}

func (d *applicationTestDatasets) ReadReportDataset(context.Context, reportmodel.ReportDatasetReadRequest) (reportmodel.ReportDatasetReadResult, error) {
	d.reads++
	return d.result, nil
}

type applicationTestVersions struct{ version string }

func (v *applicationTestVersions) ReadReportSourceVersion(context.Context, reportmodel.ReportSchema, reportmodel.ReportSubject) (reportmodel.ReportSnapshotSourceVersion, error) {
	return reportmodel.ReportSnapshotSourceVersion{Watermark: v.version, SourceVersions: map[string]string{"events": v.version}}, nil
}

type applicationTestObjectSQL struct {
	requests []reportmodel.ReportObjectSQLExecutionRequest
}

func (*applicationTestObjectSQL) ResolveReportObjectSQLSources(context.Context, reportmodel.ReportSchema, reportmodel.ReportSubject) (map[string]reportmodel.ReportSourceObject, error) {
	return map[string]reportmodel.ReportSourceObject{"order": {Key: "order", Fields: []reportmodel.ReportSourceField{{Key: "id", Type: "text"}, {Key: "status", Type: "text"}}}}, nil
}

func (*applicationTestObjectSQL) AuthorizeReportObjectSQLPlan(context.Context, reportmodel.ReportSchema, reportmodel.ReportObjectSQLPlan, reportmodel.ReportSubject) error {
	return nil
}

func (e *applicationTestObjectSQL) ExecuteReportObjectSQL(_ context.Context, request reportmodel.ReportObjectSQLExecutionRequest) (reportmodel.ReportObjectSQLExecutionResult, error) {
	e.requests = append(e.requests, request)
	if request.PageCursor == "" {
		return reportmodel.ReportObjectSQLExecutionResult{Rows: []map[string]string{{"status": "paid"}, {"status": "pending"}}, HasMore: true, Total: 3, TotalKnown: true, NextCursor: "database-cursor-2"}, nil
	}
	return reportmodel.ReportObjectSQLExecutionResult{Rows: []map[string]string{{"status": "refunded"}}, Total: 3, TotalKnown: true}, nil
}

func TestQueryServiceOwnsBoundedObjectSQLPagination(t *testing.T) {
	report := reportmodel.ReportSchema{Key: "orders", ObjectSQLV1: &reportmodel.ReportObjectSQLSchema{
		SQL: "SELECT o.status AS status FROM `order` AS o ORDER BY o.id", SourceObjects: []string{"order"},
		ResultSchema: []reportmodel.ReportResultColumnSchema{{Key: "status", Kind: "dimension", Type: "text"}},
	}}
	executor := &applicationTestObjectSQL{}
	versions := &applicationTestVersions{version: "version-1"}
	service := &QueryService{
		subjects: applicationTestSubjects{subject: applicationTestSubject()}, definitions: applicationTestDefinitions{reports: []reportmodel.ReportSchema{report}},
		objectSQL: executor, sourceVersions: versions, cursorKey: []byte("report-test-cursor-key"), clock: time.Now,
	}
	authority := reportmodel.ReportAuthority{AccessToken: "token"}
	first, err := service.QueryObjectSQL(t.Context(), reportmodel.ReportObjectSQLRequest{ReportKey: report.Key, Parameters: map[string]any{}, Page: reportmodel.ReportPageRequest{PageSize: 2}}, authority)
	if err != nil {
		t.Fatal(err)
	}
	if first.RowCount != 2 || first.Total != 3 || first.TotalSemantics != reportmodel.ReportTotalExact || !first.Truncated || first.NextCursor == "" {
		t.Fatalf("first page=%#v", first)
	}
	second, err := service.QueryObjectSQL(t.Context(), reportmodel.ReportObjectSQLRequest{ReportKey: report.Key, Parameters: map[string]any{}, Page: reportmodel.ReportPageRequest{PageSize: 2, Cursor: first.NextCursor}}, authority)
	if err != nil {
		t.Fatal(err)
	}
	if second.RowCount != 1 || second.Total != 3 || second.Truncated || second.NextCursor != "" {
		t.Fatalf("second page=%#v", second)
	}
	if len(executor.requests) != 2 || executor.requests[0].PageSize != 2 || executor.requests[1].PageCursor != "database-cursor-2" || executor.requests[1].PagePosition != 2 {
		t.Fatalf("execution requests=%#v", executor.requests)
	}
	versions.version = "version-2"
	_, err = service.QueryObjectSQL(t.Context(), reportmodel.ReportObjectSQLRequest{ReportKey: report.Key, Parameters: map[string]any{}, Page: reportmodel.ReportPageRequest{PageSize: 2, Cursor: first.NextCursor}}, authority)
	assertReportSDKErrorCode(t, err, "backend.report.cursor_stale")
}

func TestQueryServiceOwnsStablePaginationAndSourceVersionFence(t *testing.T) {
	report := applicationTestReport(false)
	datasets := applicationTestDataset()
	versions := &applicationTestVersions{version: "version-1"}
	service := &QueryService{
		subjects: applicationTestSubjects{subject: applicationTestSubject()}, definitions: applicationTestDefinitions{reports: []reportmodel.ReportSchema{report}},
		datasets: datasets, sourceVersions: versions, cursorKey: []byte("report-test-cursor-key"), clock: func() time.Time { return time.Unix(100, 0) },
	}
	authority := reportmodel.ReportAuthority{AccessToken: "token"}
	first, err := service.Summary(t.Context(), reportmodel.ReportSummaryRequest{ReportKey: report.Key, Page: reportmodel.ReportPageRequest{PageSize: 2}}, authority)
	if err != nil {
		t.Fatal(err)
	}
	if first.RowCount != 2 || first.Total != 3 || !first.Truncated || first.NextCursor == "" || first.TotalSemantics != reportmodel.ReportTotalExact {
		t.Fatalf("first page=%#v", first)
	}
	second, err := service.Summary(t.Context(), reportmodel.ReportSummaryRequest{ReportKey: report.Key, Page: reportmodel.ReportPageRequest{PageSize: 2, Cursor: first.NextCursor}}, authority)
	if err != nil {
		t.Fatal(err)
	}
	if second.RowCount != 1 || second.Total != 3 || second.Truncated || second.NextCursor != "" {
		t.Fatalf("second page=%#v", second)
	}
	if datasets.reads != 2 {
		t.Fatalf("dataset reads=%d want=2", datasets.reads)
	}

	versions.version = "version-2"
	_, err = service.Summary(t.Context(), reportmodel.ReportSummaryRequest{ReportKey: report.Key, Page: reportmodel.ReportPageRequest{PageSize: 2, Cursor: first.NextCursor}}, authority)
	assertReportSDKErrorCode(t, err, "backend.report.cursor_stale")
}

func TestQueryServiceRejectsUndeclaredPredicate(t *testing.T) {
	report := applicationTestReport(false)
	report.Dataset.QueryPredicates = []reportmodel.ReportDatasetPredicate{{Key: "active", Filters: []reportmodel.ReportDatasetFilter{{Field: reportmodel.ReportDatasetField{SourceAlias: "events", FieldKey: "status"}, Operator: "eq", Value: "active"}}}}
	service := &QueryService{
		subjects: applicationTestSubjects{subject: applicationTestSubject()}, definitions: applicationTestDefinitions{reports: []reportmodel.ReportSchema{report}},
		datasets: applicationTestDataset(), sourceVersions: &applicationTestVersions{version: "version-1"}, cursorKey: []byte("report-test-cursor-key"), clock: time.Now,
	}
	_, err := service.Summary(t.Context(), reportmodel.ReportSummaryRequest{ReportKey: report.Key, QueryKey: "request-supplied-filter"}, reportmodel.ReportAuthority{AccessToken: "token"})
	assertReportSDKErrorCode(t, err, "backend.report.query_not_allowed")
}

type applicationTestSnapshotStore struct {
	claim reportpersistence.SnapshotClaim
	begin reportpersistence.SnapshotBeginRequest
}

func (s *applicationTestSnapshotStore) Claim(_ context.Context, request reportpersistence.SnapshotBeginRequest) (reportpersistence.SnapshotClaim, error) {
	s.begin = request
	claim := s.claim
	if claim.Snapshot.LeaseOwner == "" {
		claim.Snapshot.LeaseOwner = request.LeaseOwner
	}
	if claim.Snapshot.LeaseExpiresAt == "" {
		claim.Snapshot.LeaseExpiresAt = request.LeaseExpiresAt
	}
	return claim, nil
}

func (*applicationTestSnapshotStore) Complete(context.Context, reportpersistence.SnapshotCompleteRequest) error {
	return errors.New("SnapshotService must use the atomic terminal host")
}

func (*applicationTestSnapshotStore) Fail(context.Context, reportpersistence.SnapshotFailRequest) error {
	return errors.New("SnapshotService must use the atomic terminal host")
}

func (*applicationTestSnapshotStore) Latest(context.Context, string, string, string) (reportpersistence.Snapshot, bool, error) {
	return reportpersistence.Snapshot{}, false, nil
}

type applicationTestTerminals struct {
	complete     *reportpersistence.SnapshotCompleteRequest
	completeNote notificationmodel.NotificationIntent
	fail         *reportpersistence.SnapshotFailRequest
}

func (t *applicationTestTerminals) CompleteReportSnapshot(_ context.Context, request reportpersistence.SnapshotCompleteRequest, notification notificationmodel.NotificationIntent) error {
	t.complete, t.completeNote = &request, notification
	return nil
}

func (t *applicationTestTerminals) FailReportSnapshot(_ context.Context, request reportpersistence.SnapshotFailRequest, _ notificationmodel.NotificationIntent) error {
	t.fail = &request
	return nil
}

func TestSnapshotServiceOwnsClaimFencingAndAtomicTerminalNotification(t *testing.T) {
	report := applicationTestReport(true)
	datasets := applicationTestDataset()
	versions := &applicationTestVersions{version: "version-1"}
	queries := &QueryService{
		subjects: applicationTestSubjects{subject: applicationTestSubject()}, definitions: applicationTestDefinitions{reports: []reportmodel.ReportSchema{report}},
		datasets: datasets, sourceVersions: versions, cursorKey: []byte("report-test-cursor-key"), clock: time.Now,
	}
	store := &applicationTestSnapshotStore{claim: reportpersistence.SnapshotClaim{Disposition: reportpersistence.SnapshotClaimAcquired, Snapshot: reportpersistence.Snapshot{
		ID: "snapshot-1", WorkspaceID: "workspace-1", ReportKey: report.Key, AccessScopeHash: "scope-1", IdempotencyKey: "refresh-1", Status: "refreshing", StartedAt: "2026-01-01T00:00:00Z", FencingToken: 7,
	}}}
	terminals := &applicationTestTerminals{}
	now := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	service := NewSnapshotService(queries, store, terminals, func() time.Time { return now })
	snapshot, err := service.Refresh(t.Context(), reportmodel.ReportSnapshotRefreshRequest{ReportKey: report.Key, IdempotencyKey: "refresh-1"}, reportmodel.ReportAuthority{AccessToken: "token"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != "succeeded" || snapshot.Watermark != "version-1" || terminals.complete == nil || terminals.fail != nil {
		t.Fatalf("snapshot=%#v complete=%#v fail=%#v", snapshot, terminals.complete, terminals.fail)
	}
	if terminals.complete.ExpectedStatus != "refreshing" || terminals.complete.FencingToken != 7 || terminals.complete.LeaseOwner == "" {
		t.Fatalf("completion fencing=%#v", terminals.complete)
	}
	if terminals.completeNote.EventType != "report.snapshot.completed" || terminals.completeNote.WorkspaceID != "workspace-1" || terminals.completeNote.SourceEventID != "report-snapshot:sales:refresh-1:completed" {
		t.Fatalf("notification=%#v", terminals.completeNote)
	}
	if store.begin.AccessScopeHash != "scope-1" || store.begin.IdempotencyKey != "refresh-1" || store.begin.LeaseOwner == "" {
		t.Fatalf("claim=%#v", store.begin)
	}
}

func applicationTestSubject() reportmodel.ReportSubject {
	return reportmodel.ReportSubject{Principal: identitysdk.Principal{Known: true, WorkspaceID: "workspace-1", UserID: "user-1", RoleKey: "manager"}, AccessScopeHash: "scope-1"}
}

func applicationTestReport(materialized bool) reportmodel.ReportSchema {
	report := reportmodel.ReportSchema{Key: "sales", Name: "Sales", Dataset: reportmodel.ReportDatasetSchema{
		Source:     reportmodel.ReportDatasetSource{ObjectKey: "event", Alias: "events"},
		Dimensions: []reportmodel.ReportDatasetDimension{{Key: "category", Field: reportmodel.ReportDatasetField{SourceAlias: "events", FieldKey: "category"}}},
		Measures:   []reportmodel.ReportDatasetMeasure{{Key: "count", Operation: "count"}},
		Sort:       []reportmodel.ReportDatasetSort{{Key: "category", Direction: "asc"}},
	}}
	if materialized {
		report.Materialization = &reportmodel.ReportMaterializationPolicy{MaximumLagSeconds: 300, ConsistencyRetries: 3}
	}
	return report
}

func applicationTestDataset() *applicationTestDatasets {
	rows := make([]reportmodel.ReportDatasetSourceRow, 0, 3)
	for index, category := range []string{"alpha", "beta", "gamma"} {
		rows = append(rows, reportmodel.ReportDatasetSourceRow{"events": &reportmodel.ReportSourceRecord{ID: category, Data: map[string]any{"category": category, "status": "active", "ordinal": index}}})
	}
	return &applicationTestDatasets{result: reportmodel.ReportDatasetReadResult{Rows: rows, Objects: map[string]reportmodel.ReportSourceObject{
		"events": {Key: "event", Fields: []reportmodel.ReportSourceField{{Key: "category", Type: "text"}, {Key: "status", Type: "text"}}},
	}}}
}

func assertReportSDKErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	var stable *reportsdk.Error
	if !errors.As(err, &stable) || stable.Code != want {
		t.Fatalf("error=%v code=%q want=%q", err, stableErrorCode(err, ""), want)
	}
}
