package report

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/domainry/domainry-orm/query"
	"github.com/domainry/domainry-report-sdk/modulehost"
	reportpersistence "github.com/domainry/domainry-report-sdk/persistence"
	reportschema "github.com/domainry/domainry-report/internal/infrastructure/persistence/database/schema"
)

type DefinitionStore struct {
	database modulehost.Database
	dialect  modulehost.Dialect
}

func NewDefinitionStore(database modulehost.Database, dialect modulehost.Dialect) DefinitionStore {
	return DefinitionStore{database: database, dialect: dialect}
}

var reportDefinitionTable = map[string]string{
	"report":                  "_report_definitions",
	"operation_state_example": "_report_operation_state_examples",
	"sensitive_field_policy":  "_report_sensitive_field_policies",
	"report_export_control":   "_report_export_controls",
}

type reportDefinitionIdentity struct {
	objectKey     string
	name          string
	schemaVersion string
	schemaHash    string
}

func normalizedReportDefinitions(snapshot reportpersistence.DefinitionSnapshot) (map[string]map[string]reportDefinitionIdentity, error) {
	result := make(map[string]map[string]reportDefinitionIdentity, len(reportDefinitionTable))
	for _, table := range reportDefinitionTable {
		result[table] = map[string]reportDefinitionIdentity{}
	}
	for _, definition := range snapshot.Definitions {
		table := reportDefinitionTable[strings.TrimSpace(definition.ResourceType)]
		key := strings.TrimSpace(definition.Key)
		if table == "" || key == "" || len(definition.Payload) == 0 {
			return nil, fmt.Errorf("Report definition identity is invalid")
		}
		sum := sha256.Sum256(definition.Payload)
		result[table][key] = reportDefinitionIdentity{
			objectKey: strings.TrimSpace(definition.ObjectKey), name: strings.TrimSpace(definition.Name),
			schemaVersion: snapshot.SchemaVersion, schemaHash: hex.EncodeToString(sum[:]),
		}
	}
	return result, nil
}

func (s DefinitionStore) definitionsMatch(ctx context.Context, snapshot reportpersistence.DefinitionSnapshot, expected map[string]map[string]reportDefinitionIdentity) (bool, error) {
	for _, table := range reportschema.DefinitionTables() {
		remaining := make(map[string]reportDefinitionIdentity, len(expected[table]))
		for key, identity := range expected[table] {
			remaining[key] = identity
		}
		statement, args, err := query.NewSelectBuilder(s.dialect, table).
			Columns("resource_key", "object_key", "name", "schema_version", "schema_hash").
			Where(query.And(query.Equal("source_kind", snapshot.SourceKind), query.Equal("source_id", snapshot.SourceID), query.IsNull("disabled_at"))).Build()
		if err != nil {
			return false, err
		}
		rows, err := s.database.QueryContext(ctx, statement, args...)
		if err != nil {
			return false, err
		}
		matches := true
		for rows.Next() {
			var key string
			var actual reportDefinitionIdentity
			if err := rows.Scan(&key, &actual.objectKey, &actual.name, &actual.schemaVersion, &actual.schemaHash); err != nil {
				rows.Close()
				return false, err
			}
			wanted, exists := remaining[strings.TrimSpace(key)]
			if !exists || wanted != actual {
				matches = false
			} else {
				delete(remaining, strings.TrimSpace(key))
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return false, err
		}
		rows.Close()
		if !matches || len(remaining) != 0 {
			return false, nil
		}
	}
	return true, nil
}

func (s DefinitionStore) SyncDefinitions(ctx context.Context, snapshot reportpersistence.DefinitionSnapshot) error {
	if s.database == nil || s.dialect == nil {
		return fmt.Errorf("Report definition store is unavailable")
	}
	if strings.TrimSpace(snapshot.SchemaVersion) == "" || strings.TrimSpace(snapshot.SourceKind) == "" || strings.TrimSpace(snapshot.SourceID) == "" {
		return fmt.Errorf("Report definition snapshot identity is required")
	}
	expected, err := normalizedReportDefinitions(snapshot)
	if err != nil {
		return err
	}
	matches, err := s.definitionsMatch(ctx, snapshot, expected)
	if err != nil {
		return err
	}
	if matches {
		return nil
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, table := range reportschema.DefinitionTables() {
		statement, args, err := query.NewUpdateBuilder(s.dialect, table).Set("disabled_at", now).Where(query.And(
			query.Equal("source_kind", snapshot.SourceKind), query.Equal("source_id", snapshot.SourceID), query.IsNull("disabled_at"),
		)).Build()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, statement, args...); err != nil {
			return err
		}
	}
	for _, definition := range snapshot.Definitions {
		table := reportDefinitionTable[strings.TrimSpace(definition.ResourceType)]
		key := strings.TrimSpace(definition.Key)
		identity := expected[table][key]
		update, args, err := query.NewUpdateBuilder(s.dialect, table).
			Set("object_key", identity.objectKey).Set("name", identity.name).
			Set("payload_json", definition.Payload).Set("schema_version", snapshot.SchemaVersion).
			Set("schema_hash", identity.schemaHash).Set("source_kind", snapshot.SourceKind).
			Set("source_id", snapshot.SourceID).Set("disabled_at", nil).Set("updated_at", now).
			Where(query.Equal("resource_key", key)).Build()
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, update, args...)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected > 0 {
			continue
		}
		statement, args, err := query.NewInsertBuilder(s.dialect, table).Columns(
			"id", "resource_key", "object_key", "name", "payload_json", "schema_version", "schema_hash", "source_kind", "source_id", "disabled_at", "created_at", "updated_at",
		).Values(definition.ResourceType+":"+key, key, identity.objectKey, identity.name, definition.Payload, snapshot.SchemaVersion, identity.schemaHash, snapshot.SourceKind, snapshot.SourceID, nil, now, now).Build()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, statement, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s DefinitionStore) DefinitionSnapshot(ctx context.Context) (reportpersistence.DefinitionSnapshot, error) {
	result := reportpersistence.DefinitionSnapshot{Definitions: []reportpersistence.Definition{}}
	for resourceType, table := range reportDefinitionTable {
		statement, args, err := query.NewSelectBuilder(s.dialect, table).Columns("resource_key", "object_key", "name", "payload_json", "schema_version", "source_kind", "source_id").Where(query.IsNull("disabled_at")).OrderBy(query.Ascending("resource_key")).Build()
		if err != nil {
			return reportpersistence.DefinitionSnapshot{}, err
		}
		rows, err := s.database.QueryContext(ctx, statement, args...)
		if err != nil {
			return reportpersistence.DefinitionSnapshot{}, err
		}
		for rows.Next() {
			var value reportpersistence.Definition
			var version, sourceKind, sourceID string
			if err := rows.Scan(&value.Key, &value.ObjectKey, &value.Name, &value.Payload, &version, &sourceKind, &sourceID); err != nil {
				rows.Close()
				return reportpersistence.DefinitionSnapshot{}, err
			}
			value.ResourceType = resourceType
			result.Definitions = append(result.Definitions, value)
			if result.SchemaVersion == "" {
				result.SchemaVersion, result.SourceKind, result.SourceID = version, sourceKind, sourceID
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return reportpersistence.DefinitionSnapshot{}, err
		}
		rows.Close()
	}
	return result, nil
}

var _ reportpersistence.DefinitionRepository = DefinitionStore{}
