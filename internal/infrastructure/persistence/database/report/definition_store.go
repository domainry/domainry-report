package report

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	metadatasdk "github.com/domainry/domainry-metadata-sdk"
	metadatamodulehost "github.com/domainry/domainry-metadata-sdk/modulehost"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
	"github.com/domainry/domainry-report-sdk/modulehost"
	reportpersistence "github.com/domainry/domainry-report-sdk/persistence"
)

// DefinitionStore adapts Report's typed definition contract to the shared
// installation-scoped Definition store supplied by the host.
type DefinitionStore struct {
	host   modulehost.Host
	shared metadatasdk.DefinitionStore
}

func NewDefinitionStore(host modulehost.Host, shared metadatasdk.DefinitionStore) DefinitionStore {
	return DefinitionStore{host: host, shared: shared}
}

func (s DefinitionStore) SyncDefinitions(ctx context.Context, snapshot reportpersistence.DefinitionSnapshot) error {
	if err := s.validate(); err != nil {
		return err
	}
	snapshot.SchemaVersion = strings.TrimSpace(snapshot.SchemaVersion)
	snapshot.SourceKind = strings.TrimSpace(snapshot.SourceKind)
	snapshot.SourceID = strings.TrimSpace(snapshot.SourceID)
	if snapshot.SchemaVersion == "" || snapshot.SourceKind == "" || snapshot.SourceID == "" {
		return fmt.Errorf("Report definition snapshot identity is required")
	}
	definitions := make([]metadatasdk.Definition, 0, len(snapshot.Definitions))
	seen := make(map[string]bool, len(snapshot.Definitions))
	for index := range snapshot.Definitions {
		definition := snapshot.Definitions[index]
		if err := validateReportDefinition(definition); err != nil {
			return err
		}
		key := strings.TrimSpace(definition.Report.Key)
		if seen[key] {
			return fmt.Errorf("Report definition %q is duplicated", key)
		}
		seen[key] = true
		payload, err := json.Marshal(definition)
		if err != nil {
			return fmt.Errorf("encode Report definition %q: %w", key, err)
		}
		definitions = append(definitions, metadatasdk.Definition{
			Owner: metadatasdk.DefinitionOwnerReport, ResourceType: "report", ResourceKey: key,
			Name: strings.TrimSpace(definition.Report.Name), Payload: payload,
		})
	}
	return s.shared.ReplaceSourceSnapshot(s.sharedContext(ctx), metadatasdk.ProjectionSnapshot{
		Owner: metadatasdk.DefinitionOwnerReport, SchemaVersion: snapshot.SchemaVersion,
		SourceKind: snapshot.SourceKind, SourceID: snapshot.SourceID, Definitions: definitions,
	})
}

func (s DefinitionStore) DefinitionSnapshot(ctx context.Context) (reportpersistence.DefinitionSnapshot, error) {
	if err := s.validate(); err != nil {
		return reportpersistence.DefinitionSnapshot{}, err
	}
	values, err := s.shared.List(s.sharedContext(ctx), metadatasdk.DefinitionQuery{
		Owner: metadatasdk.DefinitionOwnerReport, ResourceType: "report",
	})
	if err != nil {
		return reportpersistence.DefinitionSnapshot{}, err
	}
	result := reportpersistence.DefinitionSnapshot{Definitions: make([]reportmodel.ReportDefinitionSchema, 0, len(values))}
	for _, value := range values {
		var definition reportmodel.ReportDefinitionSchema
		if err := json.Unmarshal(value.Payload, &definition); err != nil {
			return reportpersistence.DefinitionSnapshot{}, fmt.Errorf("decode Report definition %q: %w", value.ResourceKey, err)
		}
		if err := validateReportDefinition(definition); err != nil {
			return reportpersistence.DefinitionSnapshot{}, err
		}
		if strings.TrimSpace(definition.Report.Key) != strings.TrimSpace(value.ResourceKey) {
			return reportpersistence.DefinitionSnapshot{}, fmt.Errorf("Report definition %q has inconsistent payload identity", value.ResourceKey)
		}
		result.Definitions = append(result.Definitions, definition)
		if result.SchemaVersion == "" {
			result.SchemaVersion = strings.TrimSpace(value.SchemaVersion)
			result.SourceKind = strings.TrimSpace(value.SourceKind)
			result.SourceID = strings.TrimSpace(value.SourceID)
		}
	}
	return result, nil
}

func (s DefinitionStore) validate() error {
	if s.host == nil || s.host.Database() == nil || s.shared == nil {
		return fmt.Errorf("Report definition store is unavailable")
	}
	return nil
}

func (s DefinitionStore) sharedContext(ctx context.Context) context.Context {
	executor := s.host.DatabaseFor(ctx)
	databaseExecutor := modulehost.DBTX(s.host.Database())
	if executor != nil && executor != databaseExecutor {
		return metadatamodulehost.WithExecutor(ctx, executor)
	}
	return ctx
}

func validateReportDefinition(definition reportmodel.ReportDefinitionSchema) error {
	reportKey := strings.TrimSpace(definition.Report.Key)
	if reportKey == "" {
		return fmt.Errorf("Report definition key is required")
	}
	policyKeys := make(map[string]bool, len(definition.SensitiveFieldPolicies))
	for _, policy := range definition.SensitiveFieldPolicies {
		key := strings.TrimSpace(policy.Key)
		if key == "" || policyKeys[key] {
			return fmt.Errorf("Report %q sensitive-field policy identity is invalid", reportKey)
		}
		policyKeys[key] = true
	}
	exampleKeys := make(map[string]bool, len(definition.OperationStateExamples))
	for _, example := range definition.OperationStateExamples {
		key := strings.TrimSpace(example.Key)
		if key == "" || exampleKeys[key] {
			return fmt.Errorf("Report %q operation-state example identity is invalid", reportKey)
		}
		exampleKeys[key] = true
	}
	controlKeys := make(map[string]bool, len(definition.ExportControls))
	for _, control := range definition.ExportControls {
		key := strings.TrimSpace(control.Key)
		if key == "" || controlKeys[key] || strings.TrimSpace(control.ReportKey) != reportKey {
			return fmt.Errorf("Report %q export-control identity is invalid", reportKey)
		}
		controlKeys[key] = true
		for _, policyKey := range control.SensitiveFieldPolicyKeys {
			if !policyKeys[strings.TrimSpace(policyKey)] {
				return fmt.Errorf("Report %q export control %q references unknown sensitive-field policy %q", reportKey, key, policyKey)
			}
		}
	}
	return nil
}

var _ reportpersistence.DefinitionRepository = DefinitionStore{}
