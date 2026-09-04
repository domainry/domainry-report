package report

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	notificationmodel "github.com/domainry/domainry-notification-sdk/contract"
	reportsdk "github.com/domainry/domainry-report-sdk"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
	"github.com/domainry/domainry-report-sdk/modulehost"
	reportpersistence "github.com/domainry/domainry-report-sdk/persistence"
)

type SnapshotService struct {
	queries   *QueryService
	store     reportpersistence.SnapshotRepository
	terminals modulehost.SnapshotTerminalCommitter
	clock     func() time.Time
}

func NewSnapshotService(queries *QueryService, store reportpersistence.SnapshotRepository, terminals modulehost.SnapshotTerminalCommitter, clock func() time.Time) *SnapshotService {
	if clock == nil {
		clock = time.Now
	}
	return &SnapshotService{queries: queries, store: store, terminals: terminals, clock: clock}
}

func (s *SnapshotService) Refresh(ctx context.Context, request reportmodel.ReportSnapshotRefreshRequest, authority reportmodel.ReportAuthority) (reportmodel.ReportSnapshot, error) {
	if s == nil || s.queries == nil || s.store == nil || s.terminals == nil {
		return reportmodel.ReportSnapshot{}, reportError(500, "backend.report.snapshot_unavailable", nil)
	}
	subject, report, err := s.queries.resolve(ctx, request.ReportKey, authority, reportsdk.ActionReportSnapshotsRefresh)
	if err != nil {
		return reportmodel.ReportSnapshot{}, err
	}
	if report.Materialization == nil {
		return reportmodel.ReportSnapshot{}, reportError(400, "backend.report.materialization_not_enabled", nil)
	}
	idempotencyKey := strings.TrimSpace(request.IdempotencyKey)
	if idempotencyKey == "" {
		return reportmodel.ReportSnapshot{}, reportError(400, "backend.idempotency.key_required", nil)
	}
	executionReport := reportForDataPermissions(report, reportDataPermissionKeys(report))
	accessScopeHash := reportSnapshotAccessScopeHash(subject, executionReport.RequiredPermissions)
	leaseOwner, err := newSnapshotLeaseOwner()
	if err != nil {
		return reportmodel.ReportSnapshot{}, reportError(500, "backend.report.snapshot_claim_failed", err)
	}
	now := s.clock().UTC()
	claim, err := s.store.Claim(ctx, reportpersistence.SnapshotBeginRequest{
		WorkspaceID: subject.Principal.WorkspaceID, ReportKey: report.Key, AccessScopeHash: accessScopeHash,
		IdempotencyKey: idempotencyKey, StartedAt: now.Format(time.RFC3339Nano), LeaseOwner: leaseOwner,
		LeaseExpiresAt: now.Add(2 * time.Minute).Format(time.RFC3339Nano),
	})
	if err != nil {
		return reportmodel.ReportSnapshot{}, reportError(500, "backend.report.snapshot_begin_failed", err)
	}
	if !claim.Acquired() {
		return snapshotModel(claim.Snapshot)
	}
	snapshot := claim.Snapshot
	retries := report.Materialization.ConsistencyRetries
	if retries <= 0 {
		retries = 3
	}
	var summary reportmodel.ReportSummary
	var version reportmodel.ReportSnapshotSourceVersion
	var terminalErr error
	sourceChanged, stable := false, false
	for attempt := 0; attempt < retries; attempt++ {
		before, readErr := s.queries.sourceVersions.ReadReportSourceVersion(ctx, executionReport, subject)
		if readErr != nil {
			terminalErr = normalizeHostError(readErr, "backend.report.source_version_failed")
			break
		}
		summary, terminalErr = s.queries.execute(ctx, executionReport, nil, subject)
		if terminalErr != nil {
			break
		}
		version, readErr = s.queries.sourceVersions.ReadReportSourceVersion(ctx, executionReport, subject)
		if readErr != nil {
			terminalErr = normalizeHostError(readErr, "backend.report.source_version_failed")
			break
		}
		if canonicalHash(before) == canonicalHash(version) {
			stable = true
			break
		}
		sourceChanged = true
	}
	if terminalErr == nil && !stable {
		code := "backend.report.snapshot_refresh_failed"
		if sourceChanged {
			code = "backend.report.snapshot_source_changed"
		}
		terminalErr = reportError(409, code, nil)
	}
	if terminalErr != nil {
		code := stableErrorCode(terminalErr, "backend.report.snapshot_refresh_failed")
		failed := reportpersistence.SnapshotFailRequest{WorkspaceID: snapshot.WorkspaceID, ID: snapshot.ID, ExpectedStatus: "refreshing", ErrorCode: code, LeaseOwner: snapshot.LeaseOwner, FencingToken: snapshot.FencingToken}
		if err := s.terminals.FailReportSnapshot(ctx, failed, snapshotNotification(report.Key, idempotencyKey, "failed", code, subject, s.clock())); err != nil {
			return reportmodel.ReportSnapshot{}, reportError(409, "backend.report.snapshot_terminal_commit_failed", err)
		}
		return reportmodel.ReportSnapshot{}, terminalErr
	}
	summary.ExecutionMode, summary.Snapshot = "snapshot", nil
	encoded, err := json.Marshal(summary)
	if err != nil {
		return reportmodel.ReportSnapshot{}, reportError(500, "backend.report.snapshot_invalid", err)
	}
	snapshot.Status, snapshot.Summary = "succeeded", encoded
	snapshot.Watermark, snapshot.SourceVersions = version.Watermark, version.SourceVersions
	snapshot.RefreshedAt = s.clock().UTC().Format(time.RFC3339Nano)
	complete := reportpersistence.SnapshotCompleteRequest{Snapshot: snapshot, ExpectedStatus: "refreshing", LeaseOwner: snapshot.LeaseOwner, FencingToken: snapshot.FencingToken}
	if err := s.terminals.CompleteReportSnapshot(ctx, complete, snapshotNotification(report.Key, idempotencyKey, "completed", "", subject, s.clock())); err != nil {
		return reportmodel.ReportSnapshot{}, reportError(409, "backend.report.snapshot_terminal_commit_failed", err)
	}
	return snapshotModel(snapshot)
}

func snapshotModel(snapshot reportpersistence.Snapshot) (reportmodel.ReportSnapshot, error) {
	result := reportmodel.ReportSnapshot{
		ID: snapshot.ID, WorkspaceID: snapshot.WorkspaceID, ReportKey: snapshot.ReportKey, AccessScopeHash: snapshot.AccessScopeHash,
		IdempotencyKey: snapshot.IdempotencyKey, Status: snapshot.Status, Watermark: snapshot.Watermark,
		SourceVersions: snapshot.SourceVersions, StartedAt: snapshot.StartedAt, RefreshedAt: snapshot.RefreshedAt,
		ErrorCode: snapshot.ErrorCode, LeaseOwner: snapshot.LeaseOwner, LeaseExpiresAt: snapshot.LeaseExpiresAt, FencingToken: snapshot.FencingToken,
	}
	if len(snapshot.Summary) != 0 {
		if err := json.Unmarshal(snapshot.Summary, &result.Summary); err != nil {
			return reportmodel.ReportSnapshot{}, reportError(500, "backend.report.snapshot_invalid", err)
		}
	}
	return result, nil
}

func snapshotNotification(reportKey, idempotencyKey, status, errorCode string, subject reportmodel.ReportSubject, now time.Time) notificationmodel.NotificationIntent {
	sourceID := "report-snapshot:" + strings.TrimSpace(reportKey) + ":" + strings.TrimSpace(idempotencyKey) + ":" + status
	return notificationmodel.NotificationIntent{
		ID: sourceID, WorkspaceID: subject.Principal.WorkspaceID, SourceEventID: sourceID, EventType: "report.snapshot." + status,
		RecipientUserIDs: []string{subject.Principal.UserID}, SubjectType: "report",
		SubjectID: strings.TrimSpace(reportKey), SubjectVersion: strings.TrimSpace(idempotencyKey) + ":" + status,
		DedupeKey: sourceID, OccurredAt: now.UTC().Format(time.RFC3339Nano),
		Variables: map[string]any{"report_key": strings.TrimSpace(reportKey), "status": status, "error_code": errorCode},
	}
}

func newSnapshotLeaseOwner() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "report-snapshot:" + hex.EncodeToString(value), nil
}

func stableErrorCode(err error, fallback string) string {
	var stable *reportsdk.Error
	if errors.As(err, &stable) && strings.TrimSpace(stable.Code) != "" {
		return stable.Code
	}
	return fallback
}

var _ reportsdk.SnapshotCommands = (*SnapshotService)(nil)
