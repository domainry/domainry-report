package module

import (
	"strings"
	"testing"
)

func TestModulePublishesCanonicalReportMigration(t *testing.T) {
	migrations, err := SchemaMigrations("sqlite", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 1 {
		t.Fatalf("Report migrations=%d", len(migrations))
	}
	statements := strings.Join(migrations[0].Statements, "\n")
	for _, table := range OwnedTables() {
		if !strings.Contains(statements, `CREATE TABLE IF NOT EXISTS "`+table+`"`) {
			t.Fatalf("canonical Report migration omits %s", table)
		}
	}
}
