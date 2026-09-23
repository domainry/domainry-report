package schema

import (
	"slices"
	"strings"
	"testing"

	"github.com/domainry/domainry-foundation/schemaownership"
)

func TestSchemaOwnershipMatchesEveryFreshReportTableAndPrimaryKey(t *testing.T) {
	tables := SchemaOwnership()
	if err := schemaownership.ValidateAll(tables); err != nil {
		t.Fatal(err)
	}
	statements, err := Statements("sqlite", "")
	if err != nil {
		t.Fatal(err)
	}
	created := map[string]string{}
	for _, statement := range statements {
		const prefix = `CREATE TABLE IF NOT EXISTS "`
		if !strings.HasPrefix(statement, prefix) {
			continue
		}
		name, _, found := strings.Cut(strings.TrimPrefix(statement, prefix), `"`)
		if !found || name == "" {
			t.Fatalf("invalid CREATE TABLE statement: %s", statement)
		}
		created[name] = statement
	}
	if len(created) != len(tables) || !slices.Equal(OwnedTables(), schemaownership.Names(tables)) {
		t.Fatalf("fresh Report tables=%v ownership=%+v", created, tables)
	}
	for _, table := range tables {
		statement, found := created[table.Name]
		if !found {
			t.Fatalf("Report table %s has ownership but no canonical DDL", table.Name)
		}
		quoted := make([]string, len(table.PrimaryKey))
		for index, column := range table.PrimaryKey {
			quoted[index] = `"` + column + `"`
		}
		if primaryKey := "PRIMARY KEY (" + strings.Join(quoted, ", ") + ")"; !strings.Contains(statement, primaryKey) {
			t.Fatalf("Report table %s ownership primary key %v does not match DDL: %s", table.Name, table.PrimaryKey, statement)
		}
	}
}

func TestReportOwnsOnlySnapshotTableAcrossDialects(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres", "mysql"} {
		statements, err := Statements(driver, "report_scope")
		if err != nil {
			t.Fatal(err)
		}
		wantStatements := 2
		if driver == "mysql" {
			wantStatements = 5
		}
		if len(statements) != wantStatements {
			t.Fatalf("driver=%s statements=%#v", driver, statements)
		}
		joined := strings.Join(statements, "\n")
		if !strings.Contains(joined, "_report_snapshots") {
			t.Fatalf("driver=%s missing Report snapshot table", driver)
		}
		for _, retired := range []string{"_report_definitions", "_report_operation_state_examples", "_report_sensitive_field_policies", "_report_export_controls"} {
			if strings.Contains(joined, retired) {
				t.Fatalf("driver=%s still creates retired definition table %s", driver, retired)
			}
		}
		if driver == "mysql" && (!strings.Contains(joined, "information_schema.statistics") || !strings.Contains(joined, "PREPARE domainry_report_index_stmt")) {
			t.Fatalf("MySQL adoption-safe index migration missing: %s", joined)
		}
		if driver == "mysql" && !strings.Contains(joined, "`refreshed_at` VARCHAR(40) NOT NULL") {
			t.Fatalf("MySQL latest-snapshot cursor must use a bounded key column: %s", joined)
		}
	}
}
