package schema

import (
	"strings"
	"testing"
)

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
