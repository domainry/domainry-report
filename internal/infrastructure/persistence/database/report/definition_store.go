package report

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	ormbuilder "github.com/domainry/domainry-orm/builder"
	"github.com/domainry/domainry-report-sdk/modulehost"
	reportrepository "github.com/domainry/domainry-report-sdk/repository"
)

type DefinitionStore struct {
	database modulehost.Database
	dialect  modulehost.Dialect
}

func NewDefinitionStore(database modulehost.Database, dialect modulehost.Dialect) DefinitionStore {
	return DefinitionStore{database: database, dialect: dialect}
}

var reportDefinitionTable = map[string]string{
	"report":                  "report_definitions",
	"operation_state_example": "operation_state_example_definitions",
	"sensitive_field_policy":  "sensitive_field_policy_definitions",
	"report_export_control":   "report_export_control_definitions",
}

func (s DefinitionStore) SyncDefinitions(ctx context.Context, snapshot reportrepository.DefinitionSnapshot) error {
	if s.database == nil || s.dialect == nil {
		return fmt.Errorf("Report definition store is unavailable")
	}
	if strings.TrimSpace(snapshot.SchemaVersion) == "" || strings.TrimSpace(snapshot.SourceKind) == "" || strings.TrimSpace(snapshot.SourceID) == "" {
		return fmt.Errorf("Report definition snapshot identity is required")
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, table := range definitionTables {
		statement, args, err := ormbuilder.NewUpdateBuilder(s.dialect, table).Set("disabled_at", now).Where(ormbuilder.And(
			ormbuilder.Equal("source_kind", snapshot.SourceKind), ormbuilder.Equal("source_id", snapshot.SourceID), ormbuilder.IsNull("disabled_at"),
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
		if table == "" || key == "" || len(definition.Payload) == 0 {
			return fmt.Errorf("Report definition identity is invalid")
		}
		sum := sha256.Sum256(definition.Payload)
		update, args, err := ormbuilder.NewUpdateBuilder(s.dialect, table).
			Set("object_key", strings.TrimSpace(definition.ObjectKey)).Set("name", strings.TrimSpace(definition.Name)).
			Set("payload_json", definition.Payload).Set("schema_version", snapshot.SchemaVersion).
			Set("schema_hash", hex.EncodeToString(sum[:])).Set("source_kind", snapshot.SourceKind).
			Set("source_id", snapshot.SourceID).Set("disabled_at", nil).Set("updated_at", now).
			Where(ormbuilder.Equal("resource_key", key)).Build()
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
		statement, args, err := ormbuilder.NewInsertBuilder(s.dialect, table).Columns(
			"id", "resource_key", "object_key", "name", "payload_json", "schema_version", "schema_hash", "source_kind", "source_id", "disabled_at", "created_at", "updated_at",
		).Values(definition.ResourceType+":"+key, key, strings.TrimSpace(definition.ObjectKey), strings.TrimSpace(definition.Name), definition.Payload, snapshot.SchemaVersion, hex.EncodeToString(sum[:]), snapshot.SourceKind, snapshot.SourceID, nil, now, now).Build()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, statement, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s DefinitionStore) DefinitionSnapshot(ctx context.Context) (reportrepository.DefinitionSnapshot, error) {
	result := reportrepository.DefinitionSnapshot{Definitions: []reportrepository.Definition{}}
	for resourceType, table := range reportDefinitionTable {
		statement, args, err := ormbuilder.NewSelectBuilder(s.dialect, table).Columns("resource_key", "object_key", "name", "payload_json", "schema_version", "source_kind", "source_id").Where(ormbuilder.IsNull("disabled_at")).OrderBy(ormbuilder.Ascending("resource_key")).Build()
		if err != nil {
			return reportrepository.DefinitionSnapshot{}, err
		}
		rows, err := s.database.QueryContext(ctx, statement, args...)
		if err != nil {
			return reportrepository.DefinitionSnapshot{}, err
		}
		for rows.Next() {
			var value reportrepository.Definition
			var version, sourceKind, sourceID string
			if err := rows.Scan(&value.Key, &value.ObjectKey, &value.Name, &value.Payload, &version, &sourceKind, &sourceID); err != nil {
				rows.Close()
				return reportrepository.DefinitionSnapshot{}, err
			}
			value.ResourceType = resourceType
			result.Definitions = append(result.Definitions, value)
			if result.SchemaVersion == "" {
				result.SchemaVersion, result.SourceKind, result.SourceID = version, sourceKind, sourceID
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return reportrepository.DefinitionSnapshot{}, err
		}
		rows.Close()
	}
	return result, nil
}

var _ reportrepository.DefinitionRepository = DefinitionStore{}
