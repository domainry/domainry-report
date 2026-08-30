package report

import (
	"fmt"
	ormschema "github.com/domainry/domainry-orm/schema"
	"strings"

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
	builder := ormschema.NewIndex(renderer, "idx_report_snapshot_latest", "report_snapshots").Columns("workspace_id", "report_key", "access_scope_hash", "status", "refreshed_at")
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

func definitionTable(renderer modulehost.Dialect, name string) *ormschema.TableBuilder {
	return ormschema.NewTable(renderer, name).IfNotExists().Columns(
		required("id", ormschema.TextKey(255)), required("resource_key", ormschema.TextKey(255)),
		required("object_key", ormschema.TextKey(255)), required("name", ormschema.Text()),
		required("payload_json", ormschema.LongText()), required("schema_version", ormschema.TextKey(255)),
		required("schema_hash", ormschema.TextKey(255)), required("source_kind", ormschema.TextKey(255)),
		required("source_id", ormschema.TextKey(255)), optional("disabled_at", ormschema.TextKey(255)),
		required("created_at", ormschema.TextKey(255)), required("updated_at", ormschema.TextKey(255)),
	).PrimaryKey("id").Unique("resource_key")
}

func reportSnapshotTable(renderer modulehost.Dialect) *ormschema.TableBuilder {
	return ormschema.NewTable(renderer, "report_snapshots").IfNotExists().Columns(
		required("id", ormschema.TextKey(255)), required("workspace_id", ormschema.TextKey(191)),
		required("report_key", ormschema.TextKey(191)), required("access_scope_hash", ormschema.TextKey(191)),
		required("idempotency_key", ormschema.TextKey(191)), required("status", ormschema.TextKey(32)),
		required("summary_json", ormschema.LongText()), required("watermark", ormschema.TextKey(255)),
		required("source_versions_json", ormschema.LongText()), required("row_count", ormschema.BigInt()),
		required("source_row_count", ormschema.BigInt()), required("started_at", ormschema.TextKey(255)),
		required("refreshed_at", ormschema.TextKey(255)), required("error_code", ormschema.TextKey(255)),
		required("lease_owner", ormschema.TextKey(255)), required("lease_expires_at", ormschema.TextKey(255)),
		required("fencing_token", ormschema.BigInt()),
	).PrimaryKey("id").Unique("workspace_id", "id").Unique("workspace_id", "report_key", "access_scope_hash", "idempotency_key")
}

func required(name string, kind ormschema.ColumnType) ormschema.ColumnDefinition {
	return ormschema.Column(name, kind).NotNull()
}
func optional(name string, kind ormschema.ColumnType) ormschema.ColumnDefinition {
	return ormschema.Column(name, kind)
}
