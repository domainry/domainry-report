package report

import (
	"strings"
	"testing"
)

func TestReportOwnsAllDefinitionAndSnapshotTablesAcrossDialects(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres", "mysql"} {
		migrations, err := SchemaMigrations(driver, "report_scope")
		if err != nil {
			t.Fatal(err)
		}
		wantStatements := 6
		if driver == "mysql" {
			wantStatements = 9
		}
		if len(migrations) != 1 || len(migrations[0].Statements) != wantStatements {
			t.Fatalf("driver=%s migrations=%#v", driver, migrations)
		}
		joined := strings.Join(migrations[0].Statements, "\n")
		for _, table := range append(append([]string{}, definitionTables...), "report_snapshots") {
			if !strings.Contains(joined, table) {
				t.Fatalf("driver=%s missing table %s", driver, table)
			}
		}
		if driver == "mysql" && (!strings.Contains(joined, "information_schema.statistics") || !strings.Contains(joined, "PREPARE domainry_report_index_stmt")) {
			t.Fatalf("MySQL adoption-safe index migration missing: %s", joined)
		}
	}
}
