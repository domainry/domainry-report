package schema

import (
	"strings"
	"testing"
)

func TestReportOwnsAllDefinitionAndSnapshotTablesAcrossDialects(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres", "mysql"} {
		statements, err := Statements(driver, "report_scope")
		if err != nil {
			t.Fatal(err)
		}
		wantStatements := 6
		if driver == "mysql" {
			wantStatements = 9
		}
		if len(statements) != wantStatements {
			t.Fatalf("driver=%s statements=%#v", driver, statements)
		}
		joined := strings.Join(statements, "\n")
		for _, table := range append(append([]string{}, definitionTables...), "_report_snapshots") {
			if !strings.Contains(joined, table) {
				t.Fatalf("driver=%s missing table %s", driver, table)
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
