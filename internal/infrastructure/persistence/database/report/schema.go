package report

import (
	"fmt"
	"strings"

	ormbuilder "github.com/domainry/domainry-orm/builder"
	ormdialect "github.com/domainry/domainry-orm/dialect"
	"github.com/domainry/domainry-report-sdk/modulehost"
)

const SchemaVersion uint = 1

var definitionTables = []string{
	"report_definitions",
	"operation_state_example_definitions",
	"sensitive_field_policy_definitions",
	"report_export_control_definitions",
}

func SchemaMigrations(driver, schema string) ([]modulehost.SchemaMigration, error) {
	parsed, err := ormdialect.Parse(driver)
	if err != nil {
		return nil, fmt.Errorf("Report database driver %q is unsupported: %w", driver, err)
	}
	dialect, err := ormdialect.New(parsed.Name())
	if err != nil {
		return nil, err
	}
	renderer := dialect.WithSchema(schema)
	statements := make([]string, 0, len(definitionTables)+1)
	for _, table := range definitionTables {
		statement, _, err := definitionTable(renderer, table).Build()
		if err != nil {
			return nil, fmt.Errorf("build Report definition table %s: %w", table, err)
		}
		statements = append(statements, statement)
	}
	snapshot, _, err := reportSnapshotTable(renderer).Build()
	if err != nil {
		return nil, fmt.Errorf("build Report snapshot table: %w", err)
	}
	statements = append(statements, snapshot)
	latestStatements, err := reportSnapshotLatestIndex(parsed.Name(), renderer)
	if err != nil {
		return nil, fmt.Errorf("build Report snapshot latest index: %w", err)
	}
	statements = append(statements, latestStatements...)
	return []modulehost.SchemaMigration{{Version: SchemaVersion, Name: "report_foundation", Statements: statements}}, nil
}

func reportSnapshotLatestIndex(driver ormdialect.Name, renderer modulehost.Dialect) ([]string, error) {
	builder := ormbuilder.NewCreateIndexBuilder(renderer, "idx_report_snapshot_latest", "report_snapshots").Columns("workspace_id", "report_key", "access_scope_hash", "status", "refreshed_at")
	if driver != ormdialect.MySQL {
		statement, _, err := builder.IfNotExists().Build()
		return []string{statement}, err
	}
	statement, _, err := builder.Build()
	if err != nil {
		return nil, err
	}
	// domainry-orm intentionally rejects CREATE INDEX IF NOT EXISTS for MySQL,
	// which has no portable equivalent. This narrowly-scoped migration uses
	// information_schema so a Runtime-created index can be adopted without
	// duplicate-index failure; the rendered DDL remains ORM-authored.
	escaped := strings.ReplaceAll(statement, "'", "''")
	return []string{
		"SET @domainry_report_index_sql = (SELECT IF(COUNT(*) = 0, '" + escaped + "', 'SELECT 1') FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'report_snapshots' AND index_name = 'idx_report_snapshot_latest')",
		"PREPARE domainry_report_index_stmt FROM @domainry_report_index_sql",
		"EXECUTE domainry_report_index_stmt",
		"DEALLOCATE PREPARE domainry_report_index_stmt",
	}, nil
}

func definitionTable(renderer modulehost.Dialect, name string) *ormbuilder.CreateTableBuilder {
	return ormbuilder.NewCreateTableBuilder(renderer, name).WithoutSystemColumns().IfNotExists().Columns(
		required("id", ormbuilder.TextKeyType(255)), required("resource_key", ormbuilder.TextKeyType(255)),
		required("object_key", ormbuilder.TextKeyType(255)), required("name", ormbuilder.TextType()),
		required("payload_json", ormbuilder.LongTextType()), required("schema_version", ormbuilder.TextKeyType(255)),
		required("schema_hash", ormbuilder.TextKeyType(255)), required("source_kind", ormbuilder.TextKeyType(255)),
		required("source_id", ormbuilder.TextKeyType(255)), optional("disabled_at", ormbuilder.TextKeyType(255)),
		required("created_at", ormbuilder.TextKeyType(255)), required("updated_at", ormbuilder.TextKeyType(255)),
	).PrimaryKey("id").Unique("resource_key")
}

func reportSnapshotTable(renderer modulehost.Dialect) *ormbuilder.CreateTableBuilder {
	return ormbuilder.NewCreateTableBuilder(renderer, "report_snapshots").WithoutSystemColumns().IfNotExists().Columns(
		required("id", ormbuilder.TextKeyType(255)), required("workspace_id", ormbuilder.TextKeyType(191)),
		required("report_key", ormbuilder.TextKeyType(191)), required("access_scope_hash", ormbuilder.TextKeyType(191)),
		required("idempotency_key", ormbuilder.TextKeyType(191)), required("status", ormbuilder.TextKeyType(32)),
		required("summary_json", ormbuilder.LongTextType()), required("watermark", ormbuilder.TextKeyType(255)),
		required("source_versions_json", ormbuilder.LongTextType()), required("row_count", ormbuilder.BigIntType()),
		required("source_row_count", ormbuilder.BigIntType()), required("started_at", ormbuilder.TextKeyType(255)),
		required("refreshed_at", ormbuilder.TextKeyType(255)), required("error_code", ormbuilder.TextKeyType(255)),
		required("lease_owner", ormbuilder.TextKeyType(255)), required("lease_expires_at", ormbuilder.TextKeyType(255)),
		required("fencing_token", ormbuilder.BigIntType()),
	).PrimaryKey("id").Unique("workspace_id", "id").Unique("workspace_id", "report_key", "access_scope_hash", "idempotency_key")
}

func required(name string, kind ormbuilder.ColumnType) ormbuilder.SchemaColumn {
	return ormbuilder.DefineColumn(name, kind).NotNull()
}
func optional(name string, kind ormbuilder.ColumnType) ormbuilder.SchemaColumn {
	return ormbuilder.DefineColumn(name, kind)
}
