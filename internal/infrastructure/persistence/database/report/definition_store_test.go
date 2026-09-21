package report

import (
	"encoding/json"
	"testing"

	reportpersistence "github.com/domainry/domainry-report-sdk/persistence"
)

func TestDefinitionStoreSkipsWritesForAnUnchangedSnapshot(t *testing.T) {
	snapshotStore := openSnapshotTestStore(t)
	host := snapshotStore.host.(snapshotTestHost)
	store := DefinitionStore{database: host.db, dialect: host.dialect}
	snapshot := reportpersistence.DefinitionSnapshot{
		SchemaVersion: "1", SourceKind: "generated", SourceID: "office",
		Definitions: []reportpersistence.Definition{{
			ResourceType: "report", Key: "sales", ObjectKey: "invoice", Name: "Sales", Payload: json.RawMessage(`{"key":"sales"}`),
		}},
	}
	if err := store.SyncDefinitions(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := host.db.ExecContext(t.Context(), `UPDATE _report_definitions SET updated_at = 'sentinel' WHERE resource_key = 'sales'`); err != nil {
		t.Fatal(err)
	}
	if err := store.SyncDefinitions(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	var updatedAt string
	if err := host.db.QueryRowContext(t.Context(), `SELECT updated_at FROM _report_definitions WHERE resource_key = 'sales'`).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	if updatedAt != "sentinel" {
		t.Fatalf("unchanged definition was rewritten at %q", updatedAt)
	}
}

func TestDefinitionStoreRewritesAChangedSnapshot(t *testing.T) {
	snapshotStore := openSnapshotTestStore(t)
	host := snapshotStore.host.(snapshotTestHost)
	store := DefinitionStore{database: host.db, dialect: host.dialect}
	snapshot := reportpersistence.DefinitionSnapshot{
		SchemaVersion: "1", SourceKind: "generated", SourceID: "office",
		Definitions: []reportpersistence.Definition{{ResourceType: "report", Key: "sales", Name: "Sales", Payload: json.RawMessage(`{"key":"sales"}`)}},
	}
	if err := store.SyncDefinitions(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Definitions[0].Name = "Sales changed"
	if err := store.SyncDefinitions(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := host.db.QueryRowContext(t.Context(), `SELECT name FROM _report_definitions WHERE resource_key = 'sales'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Sales changed" {
		t.Fatalf("changed definition name = %q", name)
	}
}
