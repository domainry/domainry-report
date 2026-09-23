package report

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/domainry/domainry-orm/query"
	"github.com/domainry/domainry-report-sdk/modulehost"
	reportpersistence "github.com/domainry/domainry-report-sdk/persistence"
	reportschema "github.com/domainry/domainry-report/internal/infrastructure/persistence/database/schema"
)

var _ reportpersistence.SnapshotRepository = (*ReportSnapshotStore)(nil)

type ReportSnapshotStore struct {
	host modulehost.Host
}

func NewReportSnapshotStore(host modulehost.Host) *ReportSnapshotStore {
	return &ReportSnapshotStore{host: host}
}

func (s *ReportSnapshotStore) Claim(ctx context.Context, request reportpersistence.SnapshotBeginRequest) (reportpersistence.SnapshotClaim, error) {
	if err := reportSnapshotRequestValid(request); err != nil {
		return reportpersistence.SnapshotClaim{}, err
	}
	if current, ok, err := s.reportSnapshotByIdempotency(ctx, request); err != nil {
		return reportpersistence.SnapshotClaim{}, err
	} else if ok {
		if current.Status == "succeeded" {
			return reportSnapshotClaim(current, reportpersistence.SnapshotClaimReplay), nil
		}
		if current.Status == "refreshing" && current.LeaseExpiresAt > request.StartedAt {
			return reportSnapshotClaim(current, reportpersistence.SnapshotClaimRunning), nil
		}
		claimable := query.Or(query.Equal("status", "failed"), query.And(query.Equal("status", "refreshing"), query.LessThanOrEqual("lease_expires_at", request.StartedAt)))
		queryValue, args, buildErr := query.NewWorkspaceUpdateBuilder(s.host.Dialect(), reportschema.SnapshotTableName, request.WorkspaceID).Set("status", "refreshing").Set("started_at", request.StartedAt).Set("error_code", "").Set("lease_owner", request.LeaseOwner).Set("lease_expires_at", request.LeaseExpiresAt).SetExpression("fencing_token", query.Add(query.Column("fencing_token"), query.Value(1))).Where(query.And(query.Equal("id", current.ID), claimable)).Build()
		if buildErr != nil {
			return reportpersistence.SnapshotClaim{}, buildErr
		}
		result, err := s.host.DatabaseFor(ctx).ExecContext(ctx, queryValue, args...)
		if err != nil {
			return reportpersistence.SnapshotClaim{}, err
		}
		if err := reportSnapshotRequireAffected(result); err != nil {
			latest, found, readErr := s.reportSnapshotByIdempotency(ctx, request)
			if readErr != nil {
				return reportpersistence.SnapshotClaim{}, readErr
			}
			if found {
				return reportSnapshotInactiveClaim(latest), nil
			}
			return reportpersistence.SnapshotClaim{}, err
		}
		claimed, found, readErr := s.reportSnapshotByIdempotency(ctx, request)
		if readErr != nil {
			return reportpersistence.SnapshotClaim{}, readErr
		}
		if !found {
			return reportpersistence.SnapshotClaim{}, fmt.Errorf("report snapshot claim disappeared after acquisition")
		}
		if claimed.LeaseOwner == request.LeaseOwner {
			return reportSnapshotClaim(claimed, reportpersistence.SnapshotClaimAcquired), nil
		}
		return reportSnapshotInactiveClaim(claimed), nil
	}
	id := reportSnapshotID(request)
	queryValue, args, buildErr := query.NewWorkspaceInsertBuilder(s.host.Dialect(), reportschema.SnapshotTableName, request.WorkspaceID).Columns("id", "report_key", "access_scope_hash", "idempotency_key", "status", "summary_json", "watermark", "source_versions_json", "row_count", "source_row_count", "started_at", "refreshed_at", "error_code", "lease_owner", "lease_expires_at", "fencing_token").Values(id, request.ReportKey, request.AccessScopeHash, request.IdempotencyKey, "refreshing", "{}", "", "{}", 0, 0, request.StartedAt, "", "", request.LeaseOwner, request.LeaseExpiresAt, 1).Build()
	if buildErr != nil {
		return reportpersistence.SnapshotClaim{}, buildErr
	}
	_, err := s.host.DatabaseFor(ctx).ExecContext(ctx, queryValue, args...)
	if err != nil {
		if current, ok, readErr := s.reportSnapshotByIdempotency(ctx, request); readErr == nil && ok {
			return reportSnapshotInactiveClaim(current), nil
		}
		return reportpersistence.SnapshotClaim{}, err
	}
	snapshot := reportpersistence.Snapshot{ID: id, WorkspaceID: request.WorkspaceID, ReportKey: request.ReportKey, AccessScopeHash: request.AccessScopeHash, IdempotencyKey: request.IdempotencyKey, Status: "refreshing", StartedAt: request.StartedAt, LeaseOwner: request.LeaseOwner, LeaseExpiresAt: request.LeaseExpiresAt, FencingToken: 1}
	return reportSnapshotClaim(snapshot, reportpersistence.SnapshotClaimAcquired), nil
}

func reportSnapshotClaim(snapshot reportpersistence.Snapshot, disposition reportpersistence.SnapshotClaimDisposition) reportpersistence.SnapshotClaim {
	return reportpersistence.SnapshotClaim{Snapshot: snapshot, Disposition: disposition}
}

func reportSnapshotInactiveClaim(snapshot reportpersistence.Snapshot) reportpersistence.SnapshotClaim {
	if snapshot.Status == "succeeded" {
		return reportSnapshotClaim(snapshot, reportpersistence.SnapshotClaimReplay)
	}
	return reportSnapshotClaim(snapshot, reportpersistence.SnapshotClaimRunning)
}

func (s *ReportSnapshotStore) Complete(ctx context.Context, request reportpersistence.SnapshotCompleteRequest) error {
	if strings.TrimSpace(request.Snapshot.WorkspaceID) == "" {
		return fmt.Errorf("report snapshot workspace is required")
	}
	summaryJSON := request.Snapshot.Summary
	if len(summaryJSON) == 0 {
		summaryJSON = json.RawMessage("{}")
	}
	var counts struct {
		RowCount       int `json:"row_count"`
		SourceRowCount int `json:"source_row_count"`
	}
	if err := json.Unmarshal(summaryJSON, &counts); err != nil {
		return fmt.Errorf("decode Report snapshot summary: %w", err)
	}
	versionsJSON, _ := json.Marshal(request.Snapshot.SourceVersions)
	builder := query.NewWorkspaceUpdateBuilder(s.host.Dialect(), reportschema.SnapshotTableName, request.Snapshot.WorkspaceID)
	queryValue, args, buildErr := builder.Set("status", "succeeded").Set("summary_json", string(summaryJSON)).Set("watermark", request.Snapshot.Watermark).Set("source_versions_json", string(versionsJSON)).Set("row_count", counts.RowCount).Set("source_row_count", counts.SourceRowCount).Set("refreshed_at", request.Snapshot.RefreshedAt).Set("error_code", "").Set("lease_owner", "").Set("lease_expires_at", "").Where(reportSnapshotFencePredicate(request.Snapshot.ID, request.ExpectedStatus, request.LeaseOwner, request.FencingToken)).Build()
	if buildErr != nil {
		return buildErr
	}
	result, err := s.executor(ctx).ExecContext(ctx, queryValue, args...)
	if err != nil {
		return err
	}
	return reportSnapshotRequireAffected(result)
}

func (s *ReportSnapshotStore) Fail(ctx context.Context, request reportpersistence.SnapshotFailRequest) error {
	if strings.TrimSpace(request.WorkspaceID) == "" {
		return fmt.Errorf("report snapshot workspace is required")
	}
	queryValue, args, buildErr := query.NewWorkspaceUpdateBuilder(s.host.Dialect(), reportschema.SnapshotTableName, request.WorkspaceID).Set("status", "failed").Set("error_code", request.ErrorCode).Set("lease_owner", "").Set("lease_expires_at", "").Where(reportSnapshotFencePredicate(request.ID, request.ExpectedStatus, request.LeaseOwner, request.FencingToken)).Build()
	if buildErr != nil {
		return buildErr
	}
	result, err := s.executor(ctx).ExecContext(ctx, queryValue, args...)
	if err != nil {
		return err
	}
	return reportSnapshotRequireAffected(result)
}

func (s *ReportSnapshotStore) executor(ctx context.Context) modulehost.DBTX {
	return s.host.DatabaseFor(ctx)
}

func (s *ReportSnapshotStore) Latest(ctx context.Context, workspaceID, reportKey, scopeHash string) (reportpersistence.Snapshot, bool, error) {
	queryValue, args, buildErr := query.NewWorkspaceSelectBuilder(s.host.Dialect(), reportschema.SnapshotTableName, workspaceID).Columns(reportSnapshotStoreColumns()...).Where(query.And(query.Equal("report_key", reportKey), query.Equal("access_scope_hash", scopeHash), query.Equal("status", "succeeded"))).OrderBy(query.Descending("refreshed_at"), query.Descending("id")).Limit(1).Build()
	if buildErr != nil {
		return reportpersistence.Snapshot{}, false, buildErr
	}
	return reportSnapshotScan(s.host.DatabaseFor(ctx).QueryRowContext(ctx, queryValue, args...))
}

func (s *ReportSnapshotStore) reportSnapshotByIdempotency(ctx context.Context, request reportpersistence.SnapshotBeginRequest) (reportpersistence.Snapshot, bool, error) {
	queryValue, args, buildErr := query.NewWorkspaceSelectBuilder(s.host.Dialect(), reportschema.SnapshotTableName, request.WorkspaceID).Columns(reportSnapshotStoreColumns()...).Where(query.And(query.Equal("report_key", request.ReportKey), query.Equal("access_scope_hash", request.AccessScopeHash), query.Equal("idempotency_key", request.IdempotencyKey))).Limit(1).Build()
	if buildErr != nil {
		return reportpersistence.Snapshot{}, false, buildErr
	}
	return reportSnapshotScan(s.host.DatabaseFor(ctx).QueryRowContext(ctx, queryValue, args...))
}

func reportSnapshotStoreColumns() []string {
	return []string{"id", "workspace_id", "report_key", "access_scope_hash", "idempotency_key", "status", "summary_json", "watermark", "source_versions_json", "started_at", "refreshed_at", "error_code", "lease_owner", "lease_expires_at", "fencing_token"}
}

func reportSnapshotScan(row *sql.Row) (reportpersistence.Snapshot, bool, error) {
	var snapshot reportpersistence.Snapshot
	var summaryJSON, versionsJSON string
	err := row.Scan(&snapshot.ID, &snapshot.WorkspaceID, &snapshot.ReportKey, &snapshot.AccessScopeHash, &snapshot.IdempotencyKey, &snapshot.Status, &summaryJSON, &snapshot.Watermark, &versionsJSON, &snapshot.StartedAt, &snapshot.RefreshedAt, &snapshot.ErrorCode, &snapshot.LeaseOwner, &snapshot.LeaseExpiresAt, &snapshot.FencingToken)
	if err == sql.ErrNoRows {
		return reportpersistence.Snapshot{}, false, nil
	}
	if err != nil {
		return reportpersistence.Snapshot{}, false, err
	}
	snapshot.Summary = json.RawMessage(summaryJSON)
	if err := json.Unmarshal([]byte(versionsJSON), &snapshot.SourceVersions); err != nil {
		return reportpersistence.Snapshot{}, false, err
	}
	return snapshot, true, nil
}

func reportSnapshotRequestValid(request reportpersistence.SnapshotBeginRequest) error {
	if strings.TrimSpace(request.WorkspaceID) == "" || strings.TrimSpace(request.ReportKey) == "" || strings.TrimSpace(request.AccessScopeHash) == "" || strings.TrimSpace(request.IdempotencyKey) == "" || strings.TrimSpace(request.StartedAt) == "" || strings.TrimSpace(request.LeaseOwner) == "" || strings.TrimSpace(request.LeaseExpiresAt) == "" {
		return fmt.Errorf("report snapshot scope, idempotency key, and started_at are required")
	}
	return nil
}

func reportSnapshotFencePredicate(id, status, leaseOwner string, fencingToken int64) query.Predicate {
	return query.And(query.Equal("id", id), query.Equal("status", status), query.Equal("lease_owner", strings.TrimSpace(leaseOwner)), query.Equal("fencing_token", fencingToken))
}

func reportSnapshotID(request reportpersistence.SnapshotBeginRequest) string {
	sum := sha256.Sum256([]byte(request.WorkspaceID + "\x00" + request.ReportKey + "\x00" + request.AccessScopeHash + "\x00" + request.IdempotencyKey))
	return "rptsnap_" + hex.EncodeToString(sum[:16])
}

func reportSnapshotRequireAffected(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("report snapshot fencing conflict")
	}
	return nil
}
