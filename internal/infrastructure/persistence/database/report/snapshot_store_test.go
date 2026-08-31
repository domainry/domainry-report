package report

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	ormdialect "github.com/domainry/domainry-orm/dialect"
	"github.com/domainry/domainry-report-sdk/modulehost"
	reportpersistence "github.com/domainry/domainry-report-sdk/persistence"
	reportmigration "github.com/domainry/domainry-report/internal/infrastructure/persistence/database/migration"
	_ "modernc.org/sqlite"
)

type snapshotTestHost struct {
	db      *sql.DB
	dialect modulehost.Dialect
}

func (h snapshotTestHost) Database() modulehost.Database               { return h.db }
func (h snapshotTestHost) DatabaseFor(context.Context) modulehost.DBTX { return h.db }
func (h snapshotTestHost) Dialect() modulehost.Dialect                 { return h.dialect }
func (h snapshotTestHost) Migrations() modulehost.MigrationRegistrar {
	return snapshotTestMigrations{h: h}
}

type snapshotTestMigrations struct{ h snapshotTestHost }

func (snapshotTestMigrations) Driver() string { return "sqlite" }
func (snapshotTestMigrations) Schema() string { return "" }
func (m snapshotTestMigrations) ApplyOwnedMigrations(ctx context.Context, owner string, migrations []modulehost.SchemaMigration) error {
	for _, migration := range migrations {
		for _, statement := range migration.Statements {
			if _, err := m.h.db.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
	}
	return nil
}

func openSnapshotTestStore(t *testing.T) *ReportSnapshotStore {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "report.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	dialect, err := ormdialect.New(ormdialect.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	host := snapshotTestHost{db: db, dialect: dialect.WithSchema("")}
	migrations, err := reportmigration.Migrations("sqlite", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Migrations().ApplyOwnedMigrations(t.Context(), "report", migrations); err != nil {
		t.Fatal(err)
	}
	return NewReportSnapshotStore(host)
}

func TestSnapshotStoreOwnsIdempotencyFencingAndScope(t *testing.T) {
	store := openSnapshotTestStore(t)
	begin := reportpersistence.SnapshotBeginRequest{WorkspaceID: "workspace-a", ReportKey: "operations", AccessScopeHash: "scope-a", IdempotencyKey: "window-1", StartedAt: "2026-07-21T10:00:00Z", LeaseOwner: "worker-a", LeaseExpiresAt: "2026-07-21T10:02:00Z"}
	first, err := store.Claim(t.Context(), begin)
	if err != nil || !first.Acquired() {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	running, err := store.Claim(t.Context(), begin)
	if err != nil || running.Disposition != reportpersistence.SnapshotClaimRunning {
		t.Fatalf("running=%+v err=%v", running, err)
	}
	takeoverRequest := begin
	takeoverRequest.StartedAt, takeoverRequest.LeaseOwner, takeoverRequest.LeaseExpiresAt = "2026-07-21T10:03:00Z", "worker-b", "2026-07-21T10:05:00Z"
	takeover, err := store.Claim(t.Context(), takeoverRequest)
	if err != nil || !takeover.Acquired() || takeover.Snapshot.FencingToken != first.Snapshot.FencingToken+1 {
		t.Fatalf("takeover=%+v err=%v", takeover, err)
	}
	stale := first.Snapshot
	stale.Status, stale.RefreshedAt, stale.Summary = "succeeded", "2026-07-21T10:03:01Z", json.RawMessage(`{"row_count":1}`)
	if err := store.Complete(t.Context(), reportpersistence.SnapshotCompleteRequest{Snapshot: stale, ExpectedStatus: "refreshing", LeaseOwner: stale.LeaseOwner, FencingToken: stale.FencingToken}); err == nil {
		t.Fatal("stale lease completed")
	}
	current := takeover.Snapshot
	current.Status, current.RefreshedAt, current.Summary = "succeeded", "2026-07-21T10:04:00Z", json.RawMessage(`{"row_count":1,"source_row_count":2,"rows":[{"measures":{"total":"30.00"}}]}`)
	current.Watermark, current.SourceVersions = "watermark-1", map[string]string{"entries": "2:hash"}
	if err := store.Complete(t.Context(), reportpersistence.SnapshotCompleteRequest{Snapshot: current, ExpectedStatus: "refreshing", LeaseOwner: current.LeaseOwner, FencingToken: current.FencingToken}); err != nil {
		t.Fatal(err)
	}
	replay, err := store.Claim(t.Context(), begin)
	if err != nil || replay.Disposition != reportpersistence.SnapshotClaimReplay {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	latest, found, err := store.Latest(t.Context(), "workspace-a", "operations", "scope-a")
	if err != nil || !found || latest.Watermark != "watermark-1" {
		t.Fatalf("latest=%+v found=%v err=%v", latest, found, err)
	}
	if _, found, err := store.Latest(t.Context(), "workspace-a", "operations", "scope-b"); err != nil || found {
		t.Fatalf("cross scope found=%v err=%v", found, err)
	}
}
