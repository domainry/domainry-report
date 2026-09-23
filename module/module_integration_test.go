package module_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	identitysdk "github.com/domainry/domainry-identity-sdk"
	metadatasdk "github.com/domainry/domainry-metadata-sdk"
	notificationmodel "github.com/domainry/domainry-notification-sdk/contract"
	ormdialect "github.com/domainry/domainry-orm/dialect"
	ormmigration "github.com/domainry/domainry-orm/migration"
	ormquery "github.com/domainry/domainry-orm/query"
	ormschema "github.com/domainry/domainry-orm/schema"
	reportsdk "github.com/domainry/domainry-report-sdk"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
	"github.com/domainry/domainry-report-sdk/modulehost"
	reportpersistence "github.com/domainry/domainry-report-sdk/persistence"
	reportmodule "github.com/domainry/domainry-report/module"
	"github.com/shopspring/decimal"
	_ "modernc.org/sqlite"
)

type integrationTransactionKey struct{}

type integrationHost struct {
	db                  *sql.DB
	dialect             modulehost.Dialect
	registrar           *integrationMigrationRegistrar
	snapshots           reportpersistence.SnapshotRepository
	definitions         *integrationDefinitionStore
	failNextExport      atomic.Bool
	objectSQLExecutions atomic.Int64
}

func (h *integrationHost) Database() modulehost.Database { return h.db }
func (h *integrationHost) DatabaseFor(ctx context.Context) modulehost.DBTX {
	if transaction, ok := ctx.Value(integrationTransactionKey{}).(*sql.Tx); ok && transaction != nil {
		return transaction
	}
	return h.db
}
func (h *integrationHost) Dialect() modulehost.Dialect { return h.dialect }
func (h *integrationHost) Migrations() modulehost.MigrationRegistrar {
	return h.registrar
}
func (h *integrationHost) DefinitionStore() metadatasdk.DefinitionStore { return h.definitions }
func (h *integrationHost) ReportSubjects() modulehost.SubjectResolver   { return h }
func (h *integrationHost) ReportObjectSQL() modulehost.ObjectSQLExecutor {
	return h
}
func (h *integrationHost) ReportSourceVersions() modulehost.SourceVersionReader { return h }
func (h *integrationHost) ReportExecutionAudit() modulehost.ExecutionAudit      { return h }
func (h *integrationHost) ReportExportAuthorization() modulehost.ExportAuthorization {
	return h
}
func (h *integrationHost) ReportSnapshotTerminals() modulehost.SnapshotTerminalCommitter {
	return h
}
func (h *integrationHost) ReportExports() modulehost.ExportGateway { return h }
func (*integrationHost) ReportCursorSigningKey() []byte {
	return []byte("report-module-integration-cursor-signing-key")
}
func (*integrationHost) ReportClock() func() time.Time {
	return func() time.Time { return time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC) }
}

type integrationMigrationCall struct {
	owner      string
	migrations []modulehost.SchemaMigration
}

type integrationDefinitionStore struct {
	mu     sync.RWMutex
	values []metadatasdk.Definition
}

func (s *integrationDefinitionStore) List(_ context.Context, query metadatasdk.DefinitionQuery) ([]metadatasdk.Definition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := []metadatasdk.Definition{}
	for _, value := range s.values {
		if query.Owner != "" && value.Owner != query.Owner {
			continue
		}
		if query.ResourceType != "" && value.ResourceType != query.ResourceType {
			continue
		}
		if query.SourceID != "" && value.SourceID != query.SourceID {
			continue
		}
		values = append(values, value)
	}
	return values, nil
}

func (s *integrationDefinitionStore) Get(_ context.Context, owner, resourceType, key string) (metadatasdk.Definition, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, value := range s.values {
		if value.Owner == owner && value.ResourceType == resourceType && value.ResourceKey == key && value.Status == "active" {
			return value, true, nil
		}
	}
	return metadatasdk.Definition{}, false, nil
}

func (s *integrationDefinitionStore) Snapshot(ctx context.Context, query metadatasdk.DefinitionQuery) (metadatasdk.DefinitionSnapshot, error) {
	values, err := s.List(ctx, query)
	return metadatasdk.DefinitionSnapshot{Definitions: values}, err
}

func (s *integrationDefinitionStore) ReplaceSourceSnapshot(_ context.Context, snapshot metadatasdk.ProjectionSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	retained := make([]metadatasdk.Definition, 0, len(s.values)+len(snapshot.Definitions))
	for _, value := range s.values {
		if value.Owner != snapshot.Owner || value.SourceKind != snapshot.SourceKind || value.SourceID != snapshot.SourceID {
			retained = append(retained, value)
		}
	}
	for _, value := range snapshot.Definitions {
		hash := sha256.Sum256(value.Payload)
		value.Owner = snapshot.Owner
		value.SchemaVersion = snapshot.SchemaVersion
		value.SchemaHash = hex.EncodeToString(hash[:])
		value.SourceKind = snapshot.SourceKind
		value.SourceID = snapshot.SourceID
		value.Status = "active"
		value.CurrentVersionID = "definition-version:" + value.SchemaHash[:32]
		retained = append(retained, value)
	}
	s.values = retained
	return nil
}

func (*integrationDefinitionStore) Publish(context.Context, metadatasdk.DefinitionPublishCommand) (metadatasdk.DefinitionPublishResult, error) {
	return metadatasdk.DefinitionPublishResult{}, fmt.Errorf("integration Definition publication is not configured")
}
func (*integrationDefinitionStore) Disable(context.Context, metadatasdk.DefinitionDisableCommand) error {
	return fmt.Errorf("integration Definition disable is not configured")
}
func (*integrationDefinitionStore) GetVersion(context.Context, metadatasdk.DefinitionVersionQuery) (metadatasdk.DefinitionVersion, bool, error) {
	return metadatasdk.DefinitionVersion{}, false, nil
}

// integrationMigrationRegistrar models the host-owned, owner-qualified
// migration coordinator. Report supplies DDL, but only this registrar applies
// it and records it in the host's sole _schema_migrations ledger.
type integrationMigrationRegistrar struct {
	db      *sql.DB
	dialect modulehost.Dialect
	mu      sync.Mutex
	calls   []integrationMigrationCall
}

func (*integrationMigrationRegistrar) Driver() string { return "sqlite" }
func (*integrationMigrationRegistrar) Schema() string { return "" }

func (r *integrationMigrationRegistrar) ApplyOwnedMigrations(ctx context.Context, owner string, migrations []modulehost.SchemaMigration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copied := make([]modulehost.SchemaMigration, len(migrations))
	for index, migration := range migrations {
		copied[index] = modulehost.SchemaMigration{Version: migration.Version, Name: migration.Name, Statements: append([]string(nil), migration.Statements...)}
	}
	r.calls = append(r.calls, integrationMigrationCall{owner: owner, migrations: copied})
	for _, migration := range migrations {
		path := fmt.Sprintf("module_%s_%06d_%s", strings.TrimSpace(owner), migration.Version, strings.TrimSpace(migration.Name))
		checksum := ormmigration.Checksum(migration)
		selectLedger, arguments, err := ormquery.NewSelectBuilder(r.dialect, "_schema_migrations").
			Columns("checksum", "dirty").Where(ormquery.Equal("path", path)).Build()
		if err != nil {
			return err
		}
		var applied string
		var dirty bool
		err = r.db.QueryRowContext(ctx, selectLedger, arguments...).Scan(&applied, &dirty)
		if err == nil {
			if dirty || applied != checksum {
				return fmt.Errorf("host migration ledger conflict for %s", path)
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		transaction, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		committed := false
		defer func() {
			if !committed {
				_ = transaction.Rollback()
			}
		}()
		insertLedger, arguments, err := ormquery.NewInsertBuilder(r.dialect, "_schema_migrations").
			Columns("path", "version", "name", "kind", "checksum", "dirty", "applied_at").
			Values(path, fmt.Sprint(migration.Version), migration.Name, "module:"+owner, checksum, true, "").Build()
		if err != nil {
			return err
		}
		if _, err := transaction.ExecContext(ctx, insertLedger, arguments...); err != nil {
			return err
		}
		for _, statement := range migration.Statements {
			if _, err := transaction.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		completeLedger, arguments, err := ormquery.NewUpdateBuilder(r.dialect, "_schema_migrations").
			Set("dirty", false).Set("applied_at", "2026-09-03T12:00:00Z").Where(ormquery.Equal("path", path)).Build()
		if err != nil {
			return err
		}
		if _, err := transaction.ExecContext(ctx, completeLedger, arguments...); err != nil {
			return err
		}
		if err := transaction.Commit(); err != nil {
			return err
		}
		committed = true
	}
	return nil
}

func (r *integrationMigrationRegistrar) recordedCalls() []integrationMigrationCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]integrationMigrationCall(nil), r.calls...)
}

func (h *integrationHost) ResolveReportSubject(_ context.Context, authority reportmodel.ReportAuthority) (reportmodel.ReportSubject, error) {
	switch strings.TrimSpace(authority.AccessToken) {
	case "workspace-a-full":
		return integrationSubject("workspace-a", "user-a", reportsdk.ActionReportSummaryGet, reportsdk.ActionReportQueryExecute, reportsdk.ActionReportSnapshotsRefresh, reportsdk.ActionReportExportsPrepare, "sale.read", "sale.export"), nil
	case "workspace-b-read":
		return integrationSubject("workspace-b", "user-b", reportsdk.ActionReportSummaryGet, "sale.read"), nil
	case "workspace-a-read-only":
		return integrationSubject("workspace-a", "user-read", reportsdk.ActionReportSummaryGet, "sale.read"), nil
	case "workspace-a-export-without-data":
		return integrationSubject("workspace-a", "user-export", reportsdk.ActionReportExportsPrepare, "sale.read"), nil
	case "workspace-a-action-only":
		return integrationSubject("workspace-a", "user-action", reportsdk.ActionReportSummaryGet), nil
	default:
		return reportmodel.ReportSubject{}, &reportsdk.Error{StatusCode: 401, Code: "auth.token_invalid"}
	}
}

func integrationSubject(workspaceID, userID string, permissions ...string) reportmodel.ReportSubject {
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
		Known: true, WorkspaceID: workspaceID, UserID: userID, RoleKey: "analyst", AccessBundle: bundle,
	}, AccessScopeHash: "scope:" + workspaceID + ":" + userID}
}

func integrationSaleObject() reportmodel.ReportSourceObject {
	return reportmodel.ReportSourceObject{Key: "sale", Fields: []reportmodel.ReportSourceField{
		{Key: "id", Type: "text"}, {Key: "region", Type: "text"}, {Key: "status", Type: "text"},
		{Key: "amount", Type: "currency", Precision: 19, Scale: 2},
	}}
}

func (h *integrationHost) ResolveReportObjectSQLSources(_ context.Context, report reportmodel.ReportSchema, subject reportmodel.ReportSubject) (map[string]reportmodel.ReportSourceObject, error) {
	if report.ObjectSQLV1 == nil || !subject.HasAllPermissions(report.RequiredPermissions) {
		return nil, &reportsdk.Error{StatusCode: 403, Code: "backend.permission.denied"}
	}
	return map[string]reportmodel.ReportSourceObject{"sale": integrationSaleObject()}, nil
}

func (*integrationHost) AuthorizeReportObjectSQLPlan(_ context.Context, report reportmodel.ReportSchema, plan reportmodel.ReportObjectSQLPlan, subject reportmodel.ReportSubject) error {
	if !subject.HasAllPermissions(report.RequiredPermissions) || len(plan.Sources) != 1 || plan.Sources[0].ObjectKey != "sale" {
		return &reportsdk.Error{StatusCode: 403, Code: "backend.permission.denied"}
	}
	for _, field := range plan.Sources[0].Fields {
		if field != "id" && field != "region" && field != "status" && field != "amount" {
			return &reportsdk.Error{StatusCode: 403, Code: "backend.report.object_sql_field_authorization_failed"}
		}
	}
	return nil
}

func (h *integrationHost) ExecuteReportObjectSQL(ctx context.Context, request reportmodel.ReportObjectSQLExecutionRequest) (reportmodel.ReportObjectSQLExecutionResult, error) {
	if request.Report.Key == "sales-summary" {
		status, ok := request.Parameters["status"].(string)
		if !ok {
			return reportmodel.ReportObjectSQLExecutionResult{}, fmt.Errorf("normalized text parameter was not supplied")
		}
		rows, err := h.DatabaseFor(ctx).QueryContext(ctx, "SELECT region, SUM(amount), COUNT(id) FROM host_sales WHERE workspace_id = ? AND status = ? GROUP BY region ORDER BY region", request.Subject.Principal.WorkspaceID, status)
		if err != nil {
			return reportmodel.ReportObjectSQLExecutionResult{}, err
		}
		defer rows.Close()
		values := []map[string]string{}
		for rows.Next() {
			var region, total string
			var orders int
			if err := rows.Scan(&region, &total, &orders); err != nil {
				return reportmodel.ReportObjectSQLExecutionResult{}, err
			}
			amount, err := decimal.NewFromString(total)
			if err != nil {
				return reportmodel.ReportObjectSQLExecutionResult{}, err
			}
			values = append(values, map[string]string{"region": region, "total": amount.StringFixed(2), "orders": fmt.Sprint(orders)})
		}
		if err := rows.Err(); err != nil {
			return reportmodel.ReportObjectSQLExecutionResult{}, err
		}
		pageSize := request.PageSize
		if pageSize <= 0 {
			pageSize = len(values)
		}
		start := 0
		if request.PageCursor != "" {
			for start < len(values) && values[start]["region"] <= request.PageCursor {
				start++
			}
		}
		end := min(start+pageSize, len(values))
		result := reportmodel.ReportObjectSQLExecutionResult{Rows: values[start:end], Total: len(values), TotalKnown: true, HasMore: end < len(values)}
		if result.HasMore && end > start {
			result.NextCursor = values[end-1]["region"]
		}
		h.objectSQLExecutions.Add(1)
		return result, nil
	}
	minimum, ok := request.Parameters["minimum"].(string)
	if !ok {
		return reportmodel.ReportObjectSQLExecutionResult{}, fmt.Errorf("normalized decimal parameter was not supplied")
	}
	pageSize := request.PageSize
	if pageSize < 1 {
		return reportmodel.ReportObjectSQLExecutionResult{}, fmt.Errorf("bounded page size is required")
	}
	builder := ormquery.NewWorkspaceSelectBuilder(h.dialect, "host_sales", request.Subject.Principal.WorkspaceID).
		Columns("id", "region", "amount").Where(ormquery.GreaterThanOrEqual("amount", minimum)).
		OrderBy(ormquery.Ascending("id")).Limit(pageSize + 1)
	if strings.TrimSpace(request.PageCursor) != "" {
		builder.AfterID(request.PageCursor)
	}
	statement, arguments, err := builder.Build()
	if err != nil {
		return reportmodel.ReportObjectSQLExecutionResult{}, err
	}
	rows, err := h.DatabaseFor(ctx).QueryContext(ctx, statement, arguments...)
	if err != nil {
		return reportmodel.ReportObjectSQLExecutionResult{}, err
	}
	defer rows.Close()
	type resultRow struct{ id, region, amount string }
	values := []resultRow{}
	for rows.Next() {
		var value resultRow
		if err := rows.Scan(&value.id, &value.region, &value.amount); err != nil {
			return reportmodel.ReportObjectSQLExecutionResult{}, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return reportmodel.ReportObjectSQLExecutionResult{}, err
	}
	h.objectSQLExecutions.Add(1)
	hasMore := len(values) > pageSize
	if hasMore {
		values = values[:pageSize]
	}
	result := reportmodel.ReportObjectSQLExecutionResult{HasMore: hasMore, TotalKnown: false}
	for _, value := range values {
		amount, err := decimal.NewFromString(value.amount)
		if err != nil {
			return reportmodel.ReportObjectSQLExecutionResult{}, err
		}
		result.Rows = append(result.Rows, map[string]string{"region": value.region, "amount": amount.StringFixed(2)})
	}
	if hasMore && len(values) != 0 {
		result.NextCursor = values[len(values)-1].id
	}
	return result, nil
}

func (h *integrationHost) ReadReportSourceVersion(ctx context.Context, report reportmodel.ReportSchema, subject reportmodel.ReportSubject) (reportmodel.ReportSnapshotSourceVersion, error) {
	if !subject.HasAllPermissions(report.RequiredPermissions) {
		return reportmodel.ReportSnapshotSourceVersion{}, &reportsdk.Error{StatusCode: 403, Code: "backend.permission.denied"}
	}
	statement, arguments, err := ormquery.NewWorkspaceSelectBuilder(h.dialect, "host_sales", subject.Principal.WorkspaceID).
		Columns("id", "updated_at").OrderBy(ormquery.Ascending("id")).Build()
	if err != nil {
		return reportmodel.ReportSnapshotSourceVersion{}, err
	}
	rows, err := h.DatabaseFor(ctx).QueryContext(ctx, statement, arguments...)
	if err != nil {
		return reportmodel.ReportSnapshotSourceVersion{}, err
	}
	defer rows.Close()
	hash := sha256.New()
	for rows.Next() {
		var id, updatedAt string
		if err := rows.Scan(&id, &updatedAt); err != nil {
			return reportmodel.ReportSnapshotSourceVersion{}, err
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00", id, updatedAt)
	}
	if err := rows.Err(); err != nil {
		return reportmodel.ReportSnapshotSourceVersion{}, err
	}
	watermark := hex.EncodeToString(hash.Sum(nil))
	return reportmodel.ReportSnapshotSourceVersion{Watermark: watermark, SourceVersions: map[string]string{"sale": watermark}}, nil
}

func (h *integrationHost) AppendReportExecution(ctx context.Context, report reportmodel.ReportSchema, summary reportmodel.ReportSummary, subject reportmodel.ReportSubject) error {
	statement, arguments, err := ormquery.NewWorkspaceInsertBuilder(h.dialect, "host_report_audit", subject.Principal.WorkspaceID).
		Columns("id", "report_key", "row_count").Values(subject.RequestID, report.Key, summary.RowCount).Build()
	if err != nil {
		return err
	}
	_, err = h.DatabaseFor(ctx).ExecContext(ctx, statement, arguments...)
	return err
}

func (*integrationHost) AuthorizeReportExportSource(_ context.Context, objectKey string, subject reportmodel.ReportSubject) error {
	if !subject.HasPermission(strings.TrimSpace(objectKey) + ".export") {
		return &reportsdk.Error{StatusCode: 403, Code: "backend.permission.denied"}
	}
	return nil
}

func (*integrationHost) AuthorizeReportExportField(_ context.Context, objectKey, fieldKey string, subject reportmodel.ReportSubject) (bool, error) {
	if !subject.HasPermission(strings.TrimSpace(objectKey)+".export") || strings.TrimSpace(fieldKey) == "" {
		return false, &reportsdk.Error{StatusCode: 403, Code: "backend.permission.denied"}
	}
	return false, nil
}

func (h *integrationHost) CompleteReportSnapshot(ctx context.Context, request reportpersistence.SnapshotCompleteRequest, notification notificationmodel.NotificationIntent) error {
	return h.commitSnapshotTerminal(ctx, func(transactionContext context.Context) error {
		if err := h.snapshots.Complete(transactionContext, request); err != nil {
			return err
		}
		return h.insertNotification(transactionContext, notification)
	})
}

func (h *integrationHost) FailReportSnapshot(ctx context.Context, request reportpersistence.SnapshotFailRequest, notification notificationmodel.NotificationIntent) error {
	return h.commitSnapshotTerminal(ctx, func(transactionContext context.Context) error {
		if err := h.snapshots.Fail(transactionContext, request); err != nil {
			return err
		}
		return h.insertNotification(transactionContext, notification)
	})
}

func (h *integrationHost) commitSnapshotTerminal(ctx context.Context, operation func(context.Context) error) error {
	if h.snapshots == nil {
		return fmt.Errorf("snapshot repository is unavailable")
	}
	transaction, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	transactionContext := context.WithValue(ctx, integrationTransactionKey{}, transaction)
	if err := operation(transactionContext); err != nil {
		return err
	}
	return transaction.Commit()
}

func (h *integrationHost) insertNotification(ctx context.Context, notification notificationmodel.NotificationIntent) error {
	statement, arguments, err := ormquery.NewWorkspaceInsertBuilder(h.dialect, "host_notifications", notification.WorkspaceID).
		Columns("id", "event_type", "subject_id").Values(notification.ID, notification.EventType, notification.SubjectID).Build()
	if err != nil {
		return err
	}
	_, err = h.DatabaseFor(ctx).ExecContext(ctx, statement, arguments...)
	return err
}

func (h *integrationHost) PrepareReportExport(ctx context.Context, request reportmodel.ReportExportPrepareRequest, report reportmodel.ReportSchema, control reportmodel.ReportExportControlSchema, subject reportmodel.ReportSubject) (reportmodel.ReportExportJob, error) {
	if strings.TrimSpace(request.IdempotencyKey) == "" || strings.TrimSpace(request.AuditID) == "" || report.Key != request.ReportKey || control.ReportKey != request.ReportKey {
		return reportmodel.ReportExportJob{}, fmt.Errorf("invalid export submission")
	}
	transaction, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return reportmodel.ReportExportJob{}, err
	}
	defer transaction.Rollback()
	if existing, found, err := h.exportJobByIdempotency(ctx, transaction, subject.Principal.WorkspaceID, request.IdempotencyKey); err != nil {
		return reportmodel.ReportExportJob{}, err
	} else if found {
		if err := transaction.Commit(); err != nil {
			return reportmodel.ReportExportJob{}, err
		}
		return existing, nil
	}
	scopeJSON, err := json.Marshal(request.Scope)
	if err != nil {
		return reportmodel.ReportExportJob{}, err
	}
	digest := sha256.Sum256([]byte(subject.Principal.WorkspaceID + "\x00" + request.IdempotencyKey))
	now := h.ReportClock()().Format(time.RFC3339Nano)
	job := reportmodel.ReportExportJob{
		ID: "job_" + hex.EncodeToString(digest[:12]), AuditID: request.AuditID, ReportKey: request.ReportKey,
		ObjectKey: request.ObjectKey, Status: "queued", Scope: request.Scope, CreatedAt: now, UpdatedAt: now,
	}
	statement, arguments, err := ormquery.NewWorkspaceInsertBuilder(h.dialect, "host_export_jobs", subject.Principal.WorkspaceID).
		Columns("id", "idempotency_key", "audit_id", "report_key", "object_key", "status", "scope_json", "created_at", "updated_at").
		Values(job.ID, request.IdempotencyKey, request.AuditID, request.ReportKey, request.ObjectKey, job.Status, string(scopeJSON), now, now).Build()
	if err != nil {
		return reportmodel.ReportExportJob{}, err
	}
	if _, err := transaction.ExecContext(ctx, statement, arguments...); err != nil {
		return reportmodel.ReportExportJob{}, err
	}
	if h.failNextExport.CompareAndSwap(true, false) {
		return reportmodel.ReportExportJob{}, errors.New("injected host export failure after insert")
	}
	if err := transaction.Commit(); err != nil {
		return reportmodel.ReportExportJob{}, err
	}
	return job, nil
}

func (h *integrationHost) exportJobByIdempotency(ctx context.Context, executor modulehost.DBTX, workspaceID, idempotencyKey string) (reportmodel.ReportExportJob, bool, error) {
	statement, arguments, err := ormquery.NewWorkspaceSelectBuilder(h.dialect, "host_export_jobs", workspaceID).
		Columns("id", "audit_id", "report_key", "object_key", "status", "scope_json", "created_at", "updated_at").
		Where(ormquery.Equal("idempotency_key", idempotencyKey)).Limit(1).Build()
	if err != nil {
		return reportmodel.ReportExportJob{}, false, err
	}
	var job reportmodel.ReportExportJob
	var scopeJSON string
	err = executor.QueryRowContext(ctx, statement, arguments...).Scan(&job.ID, &job.AuditID, &job.ReportKey, &job.ObjectKey, &job.Status, &scopeJSON, &job.CreatedAt, &job.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return reportmodel.ReportExportJob{}, false, nil
	}
	if err != nil {
		return reportmodel.ReportExportJob{}, false, err
	}
	if err := json.Unmarshal([]byte(scopeJSON), &job.Scope); err != nil {
		return reportmodel.ReportExportJob{}, false, err
	}
	return job, true, nil
}

func newIntegrationHost(t *testing.T) *integrationHost {
	t.Helper()
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "report-module.db"))
	if err != nil {
		t.Fatal(err)
	}
	// Serializing SQLite connections keeps transaction contention deterministic;
	// concurrent callers still race through the public Report service and are
	// deduplicated by the database transaction and unique constraint.
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })
	dialect, err := ormdialect.New(ormdialect.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	renderer := dialect.WithSchema("")
	for _, table := range integrationHostTables(renderer) {
		statement, arguments, err := table.Build()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(t.Context(), statement, arguments...); err != nil {
			t.Fatal(err)
		}
	}
	host := &integrationHost{db: database, dialect: renderer, definitions: &integrationDefinitionStore{}}
	host.registrar = &integrationMigrationRegistrar{db: database, dialect: renderer}
	for _, sale := range []struct{ workspaceID, id, region, status, amount string }{
		{"workspace-a", "a-1", "north", "paid", "0.10"},
		{"workspace-a", "a-2", "north", "paid", "0.20"},
		{"workspace-a", "a-3", "south", "paid", "10.10"},
		{"workspace-a", "a-4", "south", "pending", "5.55"},
		{"workspace-b", "b-1", "north", "paid", "999.99"},
	} {
		statement, arguments, err := ormquery.NewWorkspaceInsertBuilder(renderer, "host_sales", sale.workspaceID).
			Columns("id", "region", "status", "amount", "updated_at").
			Values(sale.id, sale.region, sale.status, sale.amount, "2026-09-03T10:00:00Z").Build()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(t.Context(), statement, arguments...); err != nil {
			t.Fatal(err)
		}
	}
	return host
}

func integrationHostTables(renderer modulehost.Dialect) []*ormschema.TableBuilder {
	required := func(name string, kind ormschema.ColumnType) ormschema.ColumnDefinition {
		return ormschema.Column(name, kind).NotNull()
	}
	return []*ormschema.TableBuilder{
		ormschema.NewTable(renderer, "_schema_migrations").IfNotExists().Columns(
			required("path", ormschema.TextKey(255)), required("version", ormschema.TextKey(32)), required("name", ormschema.TextKey(191)),
			required("kind", ormschema.TextKey(64)), required("checksum", ormschema.TextKey(64)), required("dirty", ormschema.Boolean()), required("applied_at", ormschema.TextKey(64)),
		).PrimaryKey("path"),
		ormschema.NewTable(renderer, "host_sales").IfNotExists().Columns(
			required("workspace_id", ormschema.TextKey(191)), required("id", ormschema.TextKey(191)), required("region", ormschema.TextKey(64)),
			required("status", ormschema.TextKey(64)), required("amount", ormschema.Decimal(19, 2)), required("updated_at", ormschema.TextKey(64)),
		).PrimaryKey("id").Unique("workspace_id", "id"),
		ormschema.NewTable(renderer, "host_export_jobs").IfNotExists().Columns(
			required("workspace_id", ormschema.TextKey(191)), required("id", ormschema.TextKey(191)), required("idempotency_key", ormschema.TextKey(191)),
			required("audit_id", ormschema.TextKey(191)), required("report_key", ormschema.TextKey(191)), required("object_key", ormschema.TextKey(191)),
			required("status", ormschema.TextKey(32)), required("scope_json", ormschema.LongText()), required("created_at", ormschema.TextKey(64)), required("updated_at", ormschema.TextKey(64)),
		).PrimaryKey("id").Unique("workspace_id", "idempotency_key"),
		ormschema.NewTable(renderer, "host_notifications").IfNotExists().Columns(
			required("workspace_id", ormschema.TextKey(191)), required("id", ormschema.TextKey(255)), required("event_type", ormschema.TextKey(191)), required("subject_id", ormschema.TextKey(191)),
		).PrimaryKey("id"),
		ormschema.NewTable(renderer, "host_report_audit").IfNotExists().Columns(
			required("workspace_id", ormschema.TextKey(191)), required("id", ormschema.TextKey(191)), required("report_key", ormschema.TextKey(191)), required("row_count", ormschema.BigInt()),
		).PrimaryKey("id"),
	}
}

func integrationDefinitions(t *testing.T) reportpersistence.DefinitionSnapshot {
	t.Helper()
	summary := reportmodel.ReportSchema{
		Key: "sales-summary", Name: "Sales summary", RequiredPermissions: []string{"sale.read"}, AudienceRoles: []string{"analyst"},
		Materialization: &reportmodel.ReportMaterializationPolicy{MaximumLagSeconds: 300, ConsistencyRetries: 2},
		ObjectSQLV1: &reportmodel.ReportObjectSQLSchema{
			SQL:           "SELECT s.region AS region, SUM(s.amount) AS total, COUNT(s.id) AS orders FROM sale AS s WHERE s.status = :status GROUP BY s.region ORDER BY s.region LIMIT 100",
			SourceObjects: []string{"sale"}, Parameters: []reportmodel.ReportObjectSQLParameter{{Key: "status", Type: "text", Default: "paid"}},
			ResultSchema: []reportmodel.ReportResultColumnSchema{{Key: "region", Type: "text", Kind: "dimension"}, {Key: "total", Type: "currency", Kind: "measure", Precision: 19, Scale: 2}, {Key: "orders", Type: "integer", Kind: "measure"}},
		},
	}
	objectSQL := reportmodel.ReportSchema{
		Key: "sales-detail", Name: "Sales detail", RequiredPermissions: []string{"sale.read"}, AudienceRoles: []string{"analyst"},
		ObjectSQLV1: &reportmodel.ReportObjectSQLSchema{
			SQL:           "SELECT s.region AS region, s.amount AS amount FROM sale AS s WHERE s.amount >= :minimum ORDER BY s.id LIMIT 100",
			SourceObjects: []string{"sale"}, Parameters: []reportmodel.ReportObjectSQLParameter{{Key: "minimum", Type: "decimal", Required: true}},
			ResultSchema: []reportmodel.ReportResultColumnSchema{{Key: "region", Type: "text", Kind: "dimension"}, {Key: "amount", Type: "currency", Kind: "measure", Precision: 19, Scale: 2}},
		},
	}
	control := reportmodel.ReportExportControlSchema{
		Key: "sales-summary-export", ReportKey: summary.Key, SourceObjects: []string{"sale"}, MaxRows: 1000,
	}
	definitions := []reportmodel.ReportDefinitionSchema{
		{Report: summary, ExportControls: []reportmodel.ReportExportControlSchema{control}},
		{Report: objectSQL},
	}
	return reportpersistence.DefinitionSnapshot{SchemaVersion: "integration-v1", SourceKind: "integration", SourceID: "module-test", Definitions: definitions}
}

func integrationAuthority(token string) reportmodel.ReportAuthority {
	return reportmodel.ReportAuthority{AccessToken: token, RequestID: "request-" + token}
}

func integrationAssertErrorCode(t *testing.T, err error, expected string) {
	t.Helper()
	var stable *reportsdk.Error
	if !errors.As(err, &stable) || stable.Code != expected {
		t.Fatalf("error=%v code=%q want=%q", err, func() string {
			if stable == nil {
				return ""
			}
			return stable.Code
		}(), expected)
	}
}

func integrationExportRequest(idempotencyKey string) reportmodel.ReportExportPrepareRequest {
	return reportmodel.ReportExportPrepareRequest{
		ReportKey: "sales-summary", ObjectKey: "sale", AuditID: "audit-" + idempotencyKey, IdempotencyKey: idempotencyKey,
		Scope: reportmodel.ReportExportScopeRequest{
			Parameters: map[string]any{"status": "paid"}, Purpose: "financial audit", FieldProjection: []string{"region", "total", "orders"},
			Freshness: reportmodel.ReportExportFreshness{Mode: "realtime"},
		},
	}
}

func TestPublicModuleFacadeRunsRealHostDatabaseReportLifecycle(t *testing.T) {
	host := newIntegrationHost(t)
	binding, err := reportmodule.NewFactory().Open(t.Context(), reportsdk.ApplicationRef{RuntimeID: "report-integration"}, host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = binding.Close(context.Background()) })
	host.snapshots = binding.Snapshots()
	if err := binding.Definitions().SyncDefinitions(t.Context(), integrationDefinitions(t)); err != nil {
		t.Fatal(err)
	}
	binder, ok := binding.(reportsdk.ApplicationHostBinder)
	if !ok {
		t.Fatal("public Report binding does not expose ApplicationHostBinder")
	}
	if err := binder.BindApplicationHost(host); err != nil {
		t.Fatal(err)
	}
	application, ok := binding.(reportsdk.ApplicationBinding)
	if !ok || application.Queries() == nil || application.Exports() == nil || application.SnapshotCommands() == nil {
		t.Fatal("public Report binding did not expose assembled application services")
	}

	t.Run("host registrar and sole migration ledger", func(t *testing.T) {
		calls := host.registrar.recordedCalls()
		if len(calls) != 1 || calls[0].owner != "report" || len(calls[0].migrations) != 1 || calls[0].migrations[0].Name != "report_foundation" {
			t.Fatalf("migration calls=%#v", calls)
		}
		var path, kind string
		var dirty bool
		if err := host.db.QueryRowContext(t.Context(), "SELECT path, kind, dirty FROM _schema_migrations").Scan(&path, &kind, &dirty); err != nil {
			t.Fatal(err)
		}
		if path != "module_report_000001_report_foundation" || kind != "module:report" || dirty {
			t.Fatalf("ledger path=%q kind=%q dirty=%t", path, kind, dirty)
		}
		var ledgerCount int
		if err := host.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name LIKE '%schema_migrations%'").Scan(&ledgerCount); err != nil {
			t.Fatal(err)
		}
		if ledgerCount != 1 {
			t.Fatalf("migration ledger tables=%d want=1", ledgerCount)
		}
		second, err := reportmodule.NewFactory().Open(t.Context(), reportsdk.ApplicationRef{RuntimeID: "report-integration-reopen"}, host)
		if err != nil {
			t.Fatal(err)
		}
		_ = second.Close(t.Context())
		var rows int
		if err := host.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM _schema_migrations WHERE kind='module:report'").Scan(&rows); err != nil || rows != 1 {
			t.Fatalf("Report migration rows=%d err=%v", rows, err)
		}
	})

	t.Run("definition transaction rolls back atomically", func(t *testing.T) {
		invalid := integrationDefinitions(t)
		invalid.Definitions = append(invalid.Definitions, reportmodel.ReportDefinitionSchema{})
		if err := binding.Definitions().SyncDefinitions(t.Context(), invalid); err == nil {
			t.Fatal("invalid definition synchronization succeeded")
		}
		stored, err := binding.Definitions().DefinitionSnapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(stored.Definitions) != 2 {
			t.Fatalf("active definitions=%d want=2 after rollback", len(stored.Definitions))
		}
	})

	full := integrationAuthority("workspace-a-full")
	t.Run("workspace scoped decimal query and opaque pagination", func(t *testing.T) {
		first, err := application.Queries().Summary(t.Context(), reportmodel.ReportSummaryRequest{
			ReportKey: "sales-summary", Parameters: map[string]any{"status": "paid"}, Page: reportmodel.ReportPageRequest{PageSize: 1},
		}, full)
		if err != nil {
			t.Fatal(err)
		}
		if first.RowCount != 1 || first.Total != 2 || !first.Truncated || first.NextCursor == "" || first.Rows[0].Dimensions["region"] != "north" || first.Rows[0].Measures["total"] != "0.30" || first.Rows[0].Measures["orders"] != "2" {
			t.Fatalf("first summary page=%#v", first)
		}
		second, err := application.Queries().Summary(t.Context(), reportmodel.ReportSummaryRequest{
			ReportKey: "sales-summary", Parameters: map[string]any{"status": "paid"}, Page: reportmodel.ReportPageRequest{PageSize: 1, Cursor: first.NextCursor},
		}, full)
		if err != nil {
			t.Fatal(err)
		}
		if second.RowCount != 1 || second.Truncated || second.Rows[0].Dimensions["region"] != "south" || second.Rows[0].Measures["total"] != "10.10" {
			t.Fatalf("second summary page=%#v", second)
		}
		workspaceB, err := application.Queries().Summary(t.Context(), reportmodel.ReportSummaryRequest{ReportKey: "sales-summary", Parameters: map[string]any{"status": "paid"}}, integrationAuthority("workspace-b-read"))
		if err != nil {
			t.Fatal(err)
		}
		if workspaceB.RowCount != 1 || workspaceB.Rows[0].Measures["total"] != "999.99" || workspaceB.SourceRowCount != -1 {
			t.Fatalf("workspace-b summary=%#v", workspaceB)
		}
		_, err = application.Queries().Summary(t.Context(), reportmodel.ReportSummaryRequest{ReportKey: "sales-summary"}, integrationAuthority("workspace-a-action-only"))
		integrationAssertErrorCode(t, err, "backend.report.not_found")
	})

	t.Run("typed Object SQL parameters and database cursor", func(t *testing.T) {
		first, err := application.Queries().QueryObjectSQL(t.Context(), reportmodel.ReportObjectSQLRequest{
			ReportKey: "sales-detail", Parameters: map[string]any{"minimum": "0.10"}, Page: reportmodel.ReportPageRequest{PageSize: 2},
		}, full)
		if err != nil {
			t.Fatal(err)
		}
		if first.RowCount != 2 || !first.Truncated || first.NextCursor == "" || first.Rows[0].Measures["amount"] != "0.10" || first.TotalSemantics != reportmodel.ReportTotalAtLeast {
			t.Fatalf("first Object SQL page=%#v", first)
		}
		second, err := application.Queries().QueryObjectSQL(t.Context(), reportmodel.ReportObjectSQLRequest{
			ReportKey: "sales-detail", Parameters: map[string]any{"minimum": "0.10"}, Page: reportmodel.ReportPageRequest{PageSize: 2, Cursor: first.NextCursor},
		}, full)
		if err != nil {
			t.Fatal(err)
		}
		if second.RowCount != 2 || second.Truncated || second.Rows[1].Measures["amount"] != "5.55" {
			t.Fatalf("second Object SQL page=%#v", second)
		}
		_, err = application.Queries().QueryObjectSQL(t.Context(), reportmodel.ReportObjectSQLRequest{ReportKey: "sales-detail", Parameters: map[string]any{}}, full)
		integrationAssertErrorCode(t, err, "backend.report.object_sql_parameter_required")
		_, err = application.Queries().QueryObjectSQL(t.Context(), reportmodel.ReportObjectSQLRequest{ReportKey: "sales-detail", Parameters: map[string]any{"minimum": false}}, full)
		integrationAssertErrorCode(t, err, "backend.report.object_sql_parameter_type_invalid")
		_, err = application.Queries().QueryObjectSQL(t.Context(), reportmodel.ReportObjectSQLRequest{ReportKey: "sales-detail", Parameters: map[string]any{"minimum": "1", "injected": "value"}}, full)
		integrationAssertErrorCode(t, err, "backend.report.object_sql_parameter_unknown")
	})

	t.Run("snapshot terminal state shares the host transaction", func(t *testing.T) {
		snapshot, err := application.SnapshotCommands().Refresh(t.Context(), reportmodel.ReportSnapshotRefreshRequest{ReportKey: "sales-summary", IdempotencyKey: "snapshot-1"}, full)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Status != "succeeded" || snapshot.WorkspaceID != "workspace-a" || snapshot.Summary.Rows[0].Measures["total"] != "0.30" {
			t.Fatalf("snapshot=%#v", snapshot)
		}
		var snapshotStatus string
		if err := host.db.QueryRowContext(t.Context(), "SELECT status FROM _report_snapshots WHERE workspace_id=? AND idempotency_key=?", "workspace-a", "snapshot-1").Scan(&snapshotStatus); err != nil {
			t.Fatal(err)
		}
		var notifications int
		if err := host.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM host_notifications WHERE workspace_id=? AND subject_id=?", "workspace-a", "sales-summary").Scan(&notifications); err != nil {
			t.Fatal(err)
		}
		if snapshotStatus != "succeeded" || notifications != 1 {
			t.Fatalf("snapshot status=%q notifications=%d", snapshotStatus, notifications)
		}
		replayed, err := application.SnapshotCommands().Refresh(t.Context(), reportmodel.ReportSnapshotRefreshRequest{ReportKey: "sales-summary", IdempotencyKey: "snapshot-1"}, full)
		if err != nil || replayed.ID != snapshot.ID {
			t.Fatalf("snapshot replay=%#v err=%v", replayed, err)
		}
	})

	t.Run("governed export resolves and reads through Report", func(t *testing.T) {
		request := integrationExportRequest("export-1")
		job, err := application.Exports().Prepare(t.Context(), request, full)
		if err != nil {
			t.Fatal(err)
		}
		if job.ID == "" || job.Status != "queued" || job.Scope.Parameters["status"] != "paid" {
			t.Fatalf("export job=%#v", job)
		}
		workerRequest := reportmodel.ReportExportExecutionRequest{ReportKey: request.ReportKey, ObjectKey: request.ObjectKey, Scope: job.Scope, Page: reportmodel.ReportPageRequest{PageSize: 1}}
		resolved, err := application.Exports().ResolveExecution(t.Context(), workerRequest, full)
		if err != nil || resolved.Definition.Report.Key != request.ReportKey || resolved.Definition.Control.Key != "sales-summary-export" {
			t.Fatalf("resolved export=%#v err=%v", resolved, err)
		}
		page, err := application.Exports().ReadPage(t.Context(), workerRequest, full)
		if err != nil {
			t.Fatal(err)
		}
		if page.RowCount != 1 || !page.Truncated || page.Rows[0].Measures["total"] != "0.30" {
			t.Fatalf("export page=%#v", page)
		}
		_, err = application.Exports().Prepare(t.Context(), integrationExportRequest("denied-action"), integrationAuthority("workspace-a-read-only"))
		integrationAssertErrorCode(t, err, "backend.permission.denied")
		_, err = application.Exports().Prepare(t.Context(), integrationExportRequest("denied-data"), integrationAuthority("workspace-a-export-without-data"))
		integrationAssertErrorCode(t, err, "backend.permission.denied")
	})

	t.Run("export idempotency rollback and concurrency are durable", func(t *testing.T) {
		request := integrationExportRequest("repeat-key")
		first, err := application.Exports().Prepare(t.Context(), request, full)
		if err != nil {
			t.Fatal(err)
		}
		second, err := application.Exports().Prepare(t.Context(), request, full)
		if err != nil || second.ID != first.ID {
			t.Fatalf("duplicate export first=%#v second=%#v err=%v", first, second, err)
		}
		if rows := integrationExportRows(t, host, "repeat-key"); rows != 1 {
			t.Fatalf("repeat export rows=%d want=1", rows)
		}

		host.failNextExport.Store(true)
		failed := integrationExportRequest("rollback-key")
		_, err = application.Exports().Prepare(t.Context(), failed, full)
		integrationAssertErrorCode(t, err, "backend.report.export_prepare_failed")
		if rows := integrationExportRows(t, host, "rollback-key"); rows != 0 {
			t.Fatalf("rolled-back export rows=%d want=0", rows)
		}
		if _, err := application.Exports().Prepare(t.Context(), failed, full); err != nil {
			t.Fatalf("retry after rollback: %v", err)
		}

		concurrent := integrationExportRequest("concurrent-key")
		type result struct {
			job reportmodel.ReportExportJob
			err error
		}
		results := make(chan result, 8)
		var wait sync.WaitGroup
		for range 8 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				job, err := application.Exports().Prepare(t.Context(), concurrent, full)
				results <- result{job: job, err: err}
			}()
		}
		wait.Wait()
		close(results)
		jobID := ""
		for value := range results {
			if value.err != nil {
				t.Fatal(value.err)
			}
			if jobID == "" {
				jobID = value.job.ID
			}
			if value.job.ID != jobID {
				t.Fatalf("concurrent job ID=%q want=%q", value.job.ID, jobID)
			}
		}
		if rows := integrationExportRows(t, host, "concurrent-key"); rows != 1 {
			t.Fatalf("concurrent export rows=%d want=1", rows)
		}
	})

	if host.objectSQLExecutions.Load() == 0 {
		t.Fatal("real Object SQL host adapter was not exercised")
	}
}

func integrationExportRows(t *testing.T, host *integrationHost, idempotencyKey string) int {
	t.Helper()
	var rows int
	if err := host.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM host_export_jobs WHERE workspace_id=? AND idempotency_key=?", "workspace-a", idempotencyKey).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

var _ modulehost.ApplicationHost = (*integrationHost)(nil)
