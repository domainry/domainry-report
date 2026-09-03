package report

import (
	"context"
	"errors"
	"strings"
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

type applicationTestExportAuthorization struct{ authorizedSources *[]string }

func (a applicationTestExportAuthorization) AuthorizeReportExportSource(_ context.Context, objectKey string, _ reportmodel.ReportSubject) error {
	if a.authorizedSources != nil {
		*a.authorizedSources = append(*a.authorizedSources, objectKey)
	}
	return nil
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
	authorizedSources := []string{}
	service := NewExportService(queries, applicationTestExportDefinitions{control: control, found: true}, applicationTestExportAuthorization{authorizedSources: &authorizedSources}, &applicationTestExportGateway{})
	authority := reportmodel.ReportAuthority{AccessToken: "token"}
	request := reportmodel.ReportExportExecutionRequest{
		ReportKey: report.Key, ObjectKey: "event",
		Scope: reportmodel.ReportExportScopeRequest{Purpose: "evidence", FieldProjection: []string{"category", "count"}, Freshness: reportmodel.ReportExportFreshness{Mode: "realtime"}},
		Page:  reportmodel.ReportPageRequest{PageSize: 2},
	}
	resolved, err := service.ResolveExecution(t.Context(), request, authority)
	if err != nil || resolved.Definition.Report.Key != report.Key || resolved.Definition.Control.Key != control.Key {
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
	if len(queries.datasets.(*applicationTestDatasets).requests) == 0 || !equalStrings(queries.datasets.(*applicationTestDatasets).requests[0].Report.RequiredPermissions, []string{"event.export"}) {
		t.Fatalf("export dataset did not preserve its exact Permission: %#v", queries.datasets.(*applicationTestDatasets).requests)
	}
	if len(authorizedSources) != 4 {
		t.Fatalf("export source authorization calls=%v", authorizedSources)
	}
	for _, source := range authorizedSources {
		if source != "event" {
			t.Fatalf("export switched source Permission: %v", authorizedSources)
		}
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
	result   reportmodel.ReportDatasetReadResult
	reads    int
	requests []reportmodel.ReportDatasetReadRequest
}

func (d *applicationTestDatasets) ReadReportDataset(_ context.Context, request reportmodel.ReportDatasetReadRequest) (reportmodel.ReportDatasetReadResult, error) {
	d.reads++
	d.requests = append(d.requests, request)
	return d.result, nil
}

type applicationTestVersions struct {
	version string
	reports []reportmodel.ReportSchema
}

func (v *applicationTestVersions) ReadReportSourceVersion(_ context.Context, report reportmodel.ReportSchema, _ reportmodel.ReportSubject) (reportmodel.ReportSnapshotSourceVersion, error) {
	v.reports = append(v.reports, report)
	return reportmodel.ReportSnapshotSourceVersion{Watermark: v.version, SourceVersions: map[string]string{"events": v.version}}, nil
}

type applicationTestObjectSQL struct {
	requests          []reportmodel.ReportObjectSQLExecutionRequest
	resolvedReports   []reportmodel.ReportSchema
	authorizedReports []reportmodel.ReportSchema
}

func (e *applicationTestObjectSQL) ResolveReportObjectSQLSources(_ context.Context, report reportmodel.ReportSchema, _ reportmodel.ReportSubject) (map[string]reportmodel.ReportSourceObject, error) {
	e.resolvedReports = append(e.resolvedReports, report)
	return map[string]reportmodel.ReportSourceObject{"order": {Key: "order", Fields: []reportmodel.ReportSourceField{{Key: "id", Type: "text"}, {Key: "status", Type: "text"}}}}, nil
}

func (e *applicationTestObjectSQL) AuthorizeReportObjectSQLPlan(_ context.Context, report reportmodel.ReportSchema, _ reportmodel.ReportObjectSQLPlan, _ reportmodel.ReportSubject) error {
	e.authorizedReports = append(e.authorizedReports, report)
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
	if len(executor.resolvedReports) != 2 || len(executor.authorizedReports) != 2 || !equalStrings(executor.requests[0].Report.RequiredPermissions, []string{"order.read"}) || !equalStrings(executor.resolvedReports[0].RequiredPermissions, []string{"order.read"}) || !equalStrings(executor.authorizedReports[0].RequiredPermissions, []string{"order.read"}) {
		t.Fatalf("Object SQL exact Permission was not preserved through compile/authorize/execute: resolved=%#v authorized=%#v executed=%#v", executor.resolvedReports, executor.authorizedReports, executor.requests)
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
	if !equalStrings(datasets.requests[0].Report.RequiredPermissions, []string{"event.read"}) || !equalStrings(versions.reports[0].RequiredPermissions, []string{"event.read"}) {
		t.Fatalf("summary exact Permission did not reach dataset/source-version ports: datasets=%#v versions=%#v", datasets.requests, versions.reports)
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
	subject := applicationTestSubject()
	accessScopeHash := reportSnapshotAccessScopeHash(subject, reportDataPermissionKeys(report))
	datasets := applicationTestDataset()
	versions := &applicationTestVersions{version: "version-1"}
	queries := &QueryService{
		subjects: applicationTestSubjects{subject: subject}, definitions: applicationTestDefinitions{reports: []reportmodel.ReportSchema{report}},
		datasets: datasets, sourceVersions: versions, cursorKey: []byte("report-test-cursor-key"), clock: time.Now,
	}
	store := &applicationTestSnapshotStore{claim: reportpersistence.SnapshotClaim{Disposition: reportpersistence.SnapshotClaimAcquired, Snapshot: reportpersistence.Snapshot{
		ID: "snapshot-1", WorkspaceID: "workspace-1", ReportKey: report.Key, AccessScopeHash: accessScopeHash, IdempotencyKey: "refresh-1", Status: "refreshing", StartedAt: "2026-01-01T00:00:00Z", FencingToken: 7,
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
	if store.begin.AccessScopeHash != accessScopeHash || store.begin.AccessScopeHash == subject.AccessScopeHash || store.begin.IdempotencyKey != "refresh-1" || store.begin.LeaseOwner == "" {
		t.Fatalf("claim=%#v", store.begin)
	}
	if len(datasets.requests) != 1 || !equalStrings(datasets.requests[0].Report.RequiredPermissions, []string{"event.read"}) || len(versions.reports) != 2 || !equalStrings(versions.reports[0].RequiredPermissions, []string{"event.read"}) {
		t.Fatalf("snapshot exact Permission did not reach dataset/source-version ports: datasets=%#v versions=%#v", datasets.requests, versions.reports)
	}
}

func applicationTestSubject() reportmodel.ReportSubject {
	return applicationTestSubjectWithPermissions(
		reportsdk.ActionReportSummaryGet,
		reportsdk.ActionReportQueryExecute,
		reportsdk.ActionReportSnapshotsRefresh,
		reportsdk.ActionReportExportsPrepare,
		"event.read",
		"event.export",
		"order.read",
		"order.export",
	)

}

func applicationTestSubjectWithPermissions(permissions ...string) reportmodel.ReportSubject {
	bundle := &identitysdk.AccessBundle{}
	for _, permission := range permissions {
		separator := strings.LastIndex(permission, ".")
		if separator <= 0 || separator == len(permission)-1 {
			continue
		}
		resource, action := permission[:separator], permission[separator+1:]
		bundle.FunctionGrants = append(bundle.FunctionGrants, identitysdk.FunctionGrant{Resource: identitysdk.ResourceType(resource), Action: identitysdk.Action(action), Effect: identitysdk.EffectAllow})
		bundle.DataPolicies = append(bundle.DataPolicies, identitysdk.DataPolicy{Key: permission, Resource: identitysdk.ResourceType(resource), Action: identitysdk.Action(action), Effect: identitysdk.EffectAllow, DataScopes: []identitysdk.DataScope{identitysdk.DataScopeAll}})
	}
	return reportmodel.ReportSubject{Principal: identitysdk.Principal{
		Known: true, WorkspaceID: "workspace-1", UserID: "user-1", RoleKey: "manager", AccessBundle: bundle,
	}, AccessScopeHash: "scope-1"}
}

func TestCrossWorkspaceReportUsesExplicitPermissionsInsteadOfRoleName(t *testing.T) {
	const permission = "report.cross_workspace_summary.run"
	report := reportmodel.ReportSchema{
		Key:                 "cross-workspace-summary",
		RequiredPermissions: []string{permission},
		ExecutionScope:      &reportmodel.ReportExecutionScopeSchema{Mode: reportmodel.ReportExecutionScopeCrossWorkspaceAggregateV1},
	}
	subject := applicationTestSubject()
	subject.Principal.RoleKey = "analyst"
	subject.Principal.AccessBundle = &identitysdk.AccessBundle{FunctionGrants: []identitysdk.FunctionGrant{{
		Resource: identitysdk.ResourceType("report.cross_workspace_summary"),
		Action:   identitysdk.Action("run"),
		Effect:   identitysdk.EffectAllow,
	}}, DataPolicies: []identitysdk.DataPolicy{{
		Key: "report.cross_workspace_summary.run", Resource: identitysdk.ResourceType("report.cross_workspace_summary"),
		Action: identitysdk.Action("run"), Effect: identitysdk.EffectAllow, DataScopes: []identitysdk.DataScope{identitysdk.DataScopeAll},
	}}}
	if !reportVisibleToSubject(report, subject) {
		t.Fatal("an exact report permission must authorize independently of the role name")
	}

	subject.Principal.RoleKey = "superadmin"
	subject.Principal.AccessBundle = nil
	if reportVisibleToSubject(report, subject) {
		t.Fatal("a role name must not bypass an absent exact report permission")
	}
}

func TestReportEntryRequiresFunctionAndSameKeyDataPolicy(t *testing.T) {
	report := applicationTestReport(false)
	definitions := applicationTestDefinitions{reports: []reportmodel.ReportSchema{report}}
	resolve := func(subject reportmodel.ReportSubject) error {
		service := &QueryService{subjects: applicationTestSubjects{subject: subject}, definitions: definitions}
		_, _, err := service.resolve(t.Context(), report.Key, reportmodel.ReportAuthority{AccessToken: "token"}, reportsdk.ActionReportSummaryGet)
		return err
	}

	missingEntry := applicationTestSubjectWithPermissions("event.read")
	assertReportSDKErrorCode(t, resolve(missingEntry), "backend.permission.denied")

	functionOnly := applicationTestSubjectWithPermissions("event.read")
	functionOnly.Principal.AccessBundle.FunctionGrants = append(functionOnly.Principal.AccessBundle.FunctionGrants, identitysdk.FunctionGrant{
		Resource: "report.summary", Action: "get", Effect: identitysdk.EffectAllow,
	})
	assertReportSDKErrorCode(t, resolve(functionOnly), "backend.permission.denied")

	dataOnly := applicationTestSubjectWithPermissions("event.read")
	dataOnly.Principal.AccessBundle.DataPolicies = append(dataOnly.Principal.AccessBundle.DataPolicies, identitysdk.DataPolicy{
		Key: reportsdk.ActionReportSummaryGet, Resource: "report.summary", Action: "get", Effect: identitysdk.EffectAllow, DataScopes: []identitysdk.DataScope{identitysdk.DataScopeAll},
	})
	assertReportSDKErrorCode(t, resolve(dataOnly), "backend.permission.denied")

	missingSourcePolicy := applicationTestSubjectWithPermissions(reportsdk.ActionReportSummaryGet)
	assertReportSDKErrorCode(t, resolve(missingSourcePolicy), "backend.report.not_found")

	if err := resolve(applicationTestSubjectWithPermissions(reportsdk.ActionReportSummaryGet, "event.read")); err != nil {
		t.Fatalf("complete exact Permission contracts were rejected: %v", err)
	}
}

func TestReportDataPermissionNeverSwitchesForJoinedSources(t *testing.T) {
	report := reportmodel.ReportSchema{Dataset: reportmodel.ReportDatasetSchema{
		Source: reportmodel.ReportDatasetSource{ObjectKey: "order", Alias: "orders"},
		Joins:  []reportmodel.ReportDatasetJoin{{ObjectKey: "customer", Alias: "customers"}},
	}}
	if permissions := reportDataPermissionKeys(report); !equalStrings(permissions, []string{"order.read"}) {
		t.Fatalf("joined report permissions=%v", permissions)
	}
	report.RequiredPermissions = []string{" sales.region.read ", "sales.region.read"}
	if permissions := reportDataPermissionKeys(report); !equalStrings(permissions, []string{"sales.region.read"}) {
		t.Fatalf("authored report permissions=%v", permissions)
	}
	report = reportmodel.ReportSchema{ObjectSQLV1: &reportmodel.ReportObjectSQLSchema{SourceObjects: []string{"ledger", "employee"}}}
	if permissions := reportDataPermissionKeys(report); !equalStrings(permissions, []string{"ledger.read"}) {
		t.Fatalf("Object SQL permissions=%v", permissions)
	}
}

func TestSnapshotAccessScopeBindsRequesterOrganizationAndPermission(t *testing.T) {
	subject := applicationTestSubject()
	base := reportSnapshotAccessScopeHash(subject, []string{"event.read"})

	reordered := subject
	reordered.Principal.OrgScopeIDs = []string{"child-b", "child-a"}
	canonical := subject
	canonical.Principal.OrgScopeIDs = []string{"child-a", "child-b"}
	if reportSnapshotAccessScopeHash(reordered, []string{"event.read"}) != reportSnapshotAccessScopeHash(canonical, []string{"event.read"}) {
		t.Fatal("snapshot organization scope hashing is not canonical")
	}

	otherUser := subject
	otherUser.Principal.UserID = "user-2"
	if reportSnapshotAccessScopeHash(otherUser, []string{"event.read"}) == base {
		t.Fatal("snapshot scope did not bind the requester")
	}
	otherOrg := subject
	otherOrg.Principal.OrgID = "org-2"
	if reportSnapshotAccessScopeHash(otherOrg, []string{"event.read"}) == base {
		t.Fatal("snapshot scope did not bind the natural organization")
	}
	if reportSnapshotAccessScopeHash(subject, []string{"event.export"}) == base {
		t.Fatal("snapshot scope did not bind the exact data Permission")
	}
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

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
