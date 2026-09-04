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

func (g *applicationTestExportGateway) PrepareReportExport(_ context.Context, request reportmodel.ReportExportPrepareRequest, report reportmodel.ReportSchema, control reportmodel.ReportExportControlSchema, _ reportmodel.ReportSubject) (reportmodel.ReportExportJob, error) {
	g.request, g.report, g.control = request, report, control
	return reportmodel.ReportExportJob{ID: "job-1"}, nil
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

type applicationTestVersions struct {
	version string
	reports []reportmodel.ReportSchema
}

func (v *applicationTestVersions) ReadReportSourceVersion(_ context.Context, report reportmodel.ReportSchema, _ reportmodel.ReportSubject) (reportmodel.ReportSnapshotSourceVersion, error) {
	v.reports = append(v.reports, report)
	return reportmodel.ReportSnapshotSourceVersion{Watermark: v.version, SourceVersions: map[string]string{"event": v.version}}, nil
}

type applicationTestObjectSQL struct {
	requests          []reportmodel.ReportObjectSQLExecutionRequest
	resolvedReports   []reportmodel.ReportSchema
	authorizedReports []reportmodel.ReportSchema
}

func (e *applicationTestObjectSQL) ResolveReportObjectSQLSources(_ context.Context, report reportmodel.ReportSchema, _ reportmodel.ReportSubject) (map[string]reportmodel.ReportSourceObject, error) {
	e.resolvedReports = append(e.resolvedReports, report)
	return map[string]reportmodel.ReportSourceObject{
		"event": {Key: "event", Fields: []reportmodel.ReportSourceField{{Key: "category", Type: "text"}}},
		"order": {Key: "order", Fields: []reportmodel.ReportSourceField{{Key: "status", Type: "text"}}},
	}, nil
}

func (e *applicationTestObjectSQL) AuthorizeReportObjectSQLPlan(_ context.Context, report reportmodel.ReportSchema, _ reportmodel.ReportObjectSQLPlan, _ reportmodel.ReportSubject) error {
	e.authorizedReports = append(e.authorizedReports, report)
	return nil
}

func (e *applicationTestObjectSQL) ExecuteReportObjectSQL(_ context.Context, request reportmodel.ReportObjectSQLExecutionRequest) (reportmodel.ReportObjectSQLExecutionResult, error) {
	e.requests = append(e.requests, request)
	isOrder := request.Report.Key == "orders"
	if request.PageCursor == "" {
		if isOrder {
			return reportmodel.ReportObjectSQLExecutionResult{Rows: []map[string]string{{"status": "paid"}, {"status": "pending"}}, HasMore: true, Total: 3, TotalKnown: true, NextCursor: "database-cursor-2"}, nil
		}
		return reportmodel.ReportObjectSQLExecutionResult{Rows: []map[string]string{{"category": "alpha", "count": "1"}, {"category": "beta", "count": "1"}}, HasMore: true, Total: 3, TotalKnown: true, NextCursor: "database-cursor-2"}, nil
	}
	if isOrder {
		return reportmodel.ReportObjectSQLExecutionResult{Rows: []map[string]string{{"status": "refunded"}}, Total: 3, TotalKnown: true}, nil
	}
	return reportmodel.ReportObjectSQLExecutionResult{Rows: []map[string]string{{"category": "gamma", "count": "1"}}, Total: 3, TotalKnown: true}, nil
}

func TestExportServiceUsesOnlyObjectSQLDefinition(t *testing.T) {
	report := applicationTestReport(false)
	control := reportmodel.ReportExportControlSchema{Key: "sales-export", ReportKey: report.Key, SourceObjects: []string{"event"}, MaxRows: 100}
	executor := &applicationTestObjectSQL{}
	versions := &applicationTestVersions{version: "version-1"}
	queries := &QueryService{
		subjects: applicationTestSubjects{subject: applicationTestSubject()}, definitions: applicationTestDefinitions{reports: []reportmodel.ReportSchema{report}},
		objectSQL: executor, sourceVersions: versions, cursorKey: []byte("report-export-cursor-key"), clock: time.Now,
	}
	authorizedSources := []string{}
	gateway := &applicationTestExportGateway{}
	service := NewExportService(queries, applicationTestExportDefinitions{control: control, found: true}, applicationTestExportAuthorization{authorizedSources: &authorizedSources}, gateway)
	authority := reportmodel.ReportAuthority{AccessToken: "token"}
	scope := reportmodel.ReportExportScopeRequest{Purpose: "evidence", FieldProjection: []string{"category", "count"}}
	job, err := service.Prepare(t.Context(), reportmodel.ReportExportPrepareRequest{ReportKey: report.Key, ObjectKey: "event", AuditID: "audit-1", IdempotencyKey: "request-1", Scope: scope}, authority)
	if err != nil || job.ID != "job-1" || gateway.report.ObjectSQLV1 == nil {
		t.Fatalf("job=%#v report=%#v err=%v", job, gateway.report, err)
	}
	request := reportmodel.ReportExportExecutionRequest{ReportKey: report.Key, ObjectKey: "event", Scope: scope, Page: reportmodel.ReportPageRequest{PageSize: 2}}
	first, err := service.ReadPage(t.Context(), request, authority)
	if err != nil || len(first.Rows) != 2 || !first.Truncated || first.NextCursor == "" {
		t.Fatalf("first page=%#v err=%v", first, err)
	}
	request.Page.Cursor = first.NextCursor
	second, err := service.ReadPage(t.Context(), request, authority)
	if err != nil || len(second.Rows) != 1 || second.Truncated {
		t.Fatalf("second page=%#v err=%v", second, err)
	}
	if _, err := service.SourceVersion(t.Context(), request, authority); err != nil {
		t.Fatal(err)
	}
	if len(authorizedSources) != 4 {
		t.Fatalf("source authorization calls=%v", authorizedSources)
	}
	for _, source := range authorizedSources {
		if source != "event" {
			t.Fatalf("export switched source Permission: %v", authorizedSources)
		}
	}
}

func TestQueryServiceOwnsBoundedObjectSQLPagination(t *testing.T) {
	report := reportmodel.ReportSchema{Key: "orders", ObjectSQLV1: &reportmodel.ReportObjectSQLSchema{
		SQL: "SELECT o.status AS status FROM `order` AS o ORDER BY o.id LIMIT 100", SourceObjects: []string{"order"},
		ResultSchema: []reportmodel.ReportResultColumnSchema{{Key: "status", Kind: "dimension", Type: "text"}},
	}}
	executor := &applicationTestObjectSQL{}
	versions := &applicationTestVersions{version: "version-1"}
	service := &QueryService{subjects: applicationTestSubjects{subject: applicationTestSubject()}, definitions: applicationTestDefinitions{reports: []reportmodel.ReportSchema{report}}, objectSQL: executor, sourceVersions: versions, cursorKey: []byte("report-test-cursor-key"), clock: time.Now}
	authority := reportmodel.ReportAuthority{AccessToken: "token"}
	first, err := service.Summary(t.Context(), reportmodel.ReportSummaryRequest{ReportKey: report.Key, Page: reportmodel.ReportPageRequest{PageSize: 2}}, authority)
	if err != nil || first.RowCount != 2 || !first.Truncated || first.NextCursor == "" {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	second, err := service.Summary(t.Context(), reportmodel.ReportSummaryRequest{ReportKey: report.Key, Page: reportmodel.ReportPageRequest{PageSize: 2, Cursor: first.NextCursor}}, authority)
	if err != nil || second.RowCount != 1 || second.Truncated {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	if len(executor.requests) != 2 || executor.requests[1].PageCursor != "database-cursor-2" {
		t.Fatalf("requests=%#v", executor.requests)
	}
	versions.version = "version-2"
	_, err = service.Summary(t.Context(), reportmodel.ReportSummaryRequest{ReportKey: report.Key, Page: reportmodel.ReportPageRequest{PageSize: 2, Cursor: first.NextCursor}}, authority)
	assertReportSDKErrorCode(t, err, "backend.report.cursor_stale")
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
	complete *reportpersistence.SnapshotCompleteRequest
}

func (t *applicationTestTerminals) CompleteReportSnapshot(_ context.Context, request reportpersistence.SnapshotCompleteRequest, _ notificationmodel.NotificationIntent) error {
	t.complete = &request
	return nil
}
func (*applicationTestTerminals) FailReportSnapshot(context.Context, reportpersistence.SnapshotFailRequest, notificationmodel.NotificationIntent) error {
	return nil
}

func TestSnapshotServiceExecutesObjectSQL(t *testing.T) {
	report := applicationTestReport(true)
	subject := applicationTestSubject()
	versions := &applicationTestVersions{version: "version-1"}
	executor := &applicationTestObjectSQL{}
	queries := &QueryService{subjects: applicationTestSubjects{subject: subject}, definitions: applicationTestDefinitions{reports: []reportmodel.ReportSchema{report}}, objectSQL: executor, sourceVersions: versions, cursorKey: []byte("report-test-cursor-key"), clock: time.Now}
	accessScopeHash := reportSnapshotAccessScopeHash(subject, reportDataPermissionKeys(report))
	store := &applicationTestSnapshotStore{claim: reportpersistence.SnapshotClaim{Disposition: reportpersistence.SnapshotClaimAcquired, Snapshot: reportpersistence.Snapshot{ID: "snapshot-1", WorkspaceID: "workspace-1", ReportKey: report.Key, AccessScopeHash: accessScopeHash, IdempotencyKey: "refresh-1", Status: "refreshing", StartedAt: "2026-01-01T00:00:00Z", FencingToken: 7}}}
	terminals := &applicationTestTerminals{}
	snapshot, err := NewSnapshotService(queries, store, terminals, func() time.Time { return time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC) }).Refresh(t.Context(), reportmodel.ReportSnapshotRefreshRequest{ReportKey: report.Key, IdempotencyKey: "refresh-1"}, reportmodel.ReportAuthority{AccessToken: "token"})
	if err != nil || snapshot.Status != "succeeded" || terminals.complete == nil || len(executor.requests) != 1 {
		t.Fatalf("snapshot=%#v complete=%#v requests=%d err=%v", snapshot, terminals.complete, len(executor.requests), err)
	}
}

func TestTrustedProcessVisibilityUsesExactCapabilitiesInsteadOfHumanAudienceRole(t *testing.T) {
	report := applicationTestReport(false)
	report.AudienceRoles = []string{"sales_manager"}
	report.RequiredPermissions = []string{"event.read"}
	subject := reportmodel.ReportSubject{Principal: identitysdk.Principal{Known: true, WorkspaceID: "workspace-1", UserID: "runtime-target-executor"}, AccessScopeHash: "scope-1", TrustedProcess: true, ProcessCapabilities: []string{"report.snapshots.refresh"}}
	queries := &QueryService{subjects: applicationTestSubjects{subject: subject}, definitions: applicationTestDefinitions{reports: []reportmodel.ReportSchema{report}}}
	resolved, _, err := queries.resolve(t.Context(), report.Key, reportmodel.ReportAuthority{Subject: &subject}, reportsdk.ActionReportSnapshotsRefresh)
	if err != nil || !resolved.HasPermission("event.read") {
		t.Fatalf("subject=%#v err=%v", resolved, err)
	}
	subject.ProcessCapabilities = nil
	queries.subjects = applicationTestSubjects{subject: subject}
	if _, _, err := queries.resolve(t.Context(), report.Key, reportmodel.ReportAuthority{Subject: &subject}, reportsdk.ActionReportSnapshotsRefresh); err == nil {
		t.Fatal("trusted process bypassed the report operation permission")
	}
}

func TestReportEntryRequiresFunctionAndSameKeyDataPolicy(t *testing.T) {
	report := applicationTestReport(false)
	resolve := func(subject reportmodel.ReportSubject) error {
		service := &QueryService{subjects: applicationTestSubjects{subject: subject}, definitions: applicationTestDefinitions{reports: []reportmodel.ReportSchema{report}}}
		_, _, err := service.resolve(t.Context(), report.Key, reportmodel.ReportAuthority{AccessToken: "token"}, reportsdk.ActionReportSummaryGet)
		return err
	}
	assertReportSDKErrorCode(t, resolve(applicationTestSubjectWithPermissions("event.read")), "backend.permission.denied")
	assertReportSDKErrorCode(t, resolve(applicationTestSubjectWithPermissions(reportsdk.ActionReportSummaryGet)), "backend.report.not_found")
	if err := resolve(applicationTestSubjectWithPermissions(reportsdk.ActionReportSummaryGet, "event.read")); err != nil {
		t.Fatalf("complete Permission contracts rejected: %v", err)
	}
}

func applicationTestReport(materialized bool) reportmodel.ReportSchema {
	report := reportmodel.ReportSchema{Key: "sales", Name: "Sales", ObjectSQLV1: &reportmodel.ReportObjectSQLSchema{
		SQL:           "SELECT e.category AS category, COUNT(e.id) AS count FROM event e GROUP BY e.category ORDER BY e.category LIMIT 100",
		SourceObjects: []string{"event"},
		ResultSchema:  []reportmodel.ReportResultColumnSchema{{Key: "category", Type: "text", Kind: "dimension"}, {Key: "count", Type: "integer", Kind: "measure"}},
	}}
	if materialized {
		report.Materialization = &reportmodel.ReportMaterializationPolicy{MaximumLagSeconds: 300, ConsistencyRetries: 3}
	}
	return report
}

func applicationTestSubject() reportmodel.ReportSubject {
	return applicationTestSubjectWithPermissions(reportsdk.ActionReportSummaryGet, reportsdk.ActionReportQueryExecute, reportsdk.ActionReportSnapshotsRefresh, reportsdk.ActionReportExportsPrepare, "event.read", "event.export", "order.read", "order.export")
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
	return reportmodel.ReportSubject{Principal: identitysdk.Principal{Known: true, WorkspaceID: "workspace-1", UserID: "user-1", RoleKey: "manager", AccessBundle: bundle}, AccessScopeHash: "scope-1"}
}

func assertReportSDKErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	var stable *reportsdk.Error
	if !errors.As(err, &stable) || stable.Code != want {
		t.Fatalf("error=%v code=%q want=%q", err, stableErrorCode(err, ""), want)
	}
}
