package reportsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/domainry/domainry-foundation/modulecapability"
	"github.com/domainry/domainry-foundation/modulehttp"
	reportsdk "github.com/domainry/domainry-report-sdk"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
	reportquery "github.com/domainry/domainry-report-sdk/query"
	reportobjectsql "github.com/domainry/domainry-report/internal/domain/report/service/objectsql"
	reportplan "github.com/domainry/domainry-report/internal/domain/report/service/plan"
)

const reportBusinessCategory = reportsdk.CapabilityReportBusiness

type reportObjectAuthoringFragment struct {
	Key            string                               `json:"key"`
	Name           string                               `json:"name"`
	Description    string                               `json:"description"`
	I18n           json.RawMessage                      `json:"i18n,omitempty"`
	Fields         []reportObjectFieldAuthoringFragment `json:"fields"`
	Validations    json.RawMessage                      `json:"validations,omitempty"`
	UX             json.RawMessage                      `json:"ux,omitempty"`
	Config         json.RawMessage                      `json:"config,omitempty"`
	VersionHistory json.RawMessage                      `json:"version_history,omitempty"`
	Provenance     json.RawMessage                      `json:"provenance,omitempty"`
}

type reportObjectFieldAuthoringFragment struct {
	Key            string          `json:"key"`
	Name           string          `json:"name"`
	Type           string          `json:"type"`
	I18n           json.RawMessage `json:"i18n,omitempty"`
	Config         map[string]any  `json:"config"`
	Validation     json.RawMessage `json:"validation,omitempty"`
	Options        json.RawMessage `json:"options,omitempty"`
	Required       bool            `json:"required"`
	Unique         bool            `json:"unique,omitempty"`
	DefaultValue   json.RawMessage `json:"default_value,omitempty"`
	VersionHistory json.RawMessage `json:"version_history,omitempty"`
	Provenance     json.RawMessage `json:"provenance,omitempty"`
}

func NewCapabilityBinding(validator modulecapability.Validator) (*modulecapability.StaticBinding, error) {
	routes, operations, err := reportHTTPContract()
	if err != nil {
		return nil, err
	}
	document, err := modulecapability.CategoryFromHTTPRoutes(modulecapability.HTTPRouteCategory{
		Owner: "report",
		Category: modulecapability.CategorySummary{
			Key: reportBusinessCategory, Name: "Business reports", Description: "Execute authorized report summaries and Object SQL queries, refresh snapshots, and prepare governed exports.",
			AssemblyChains:   []string{"identity_scope_to_report_execution", "scheduler_to_report_snapshot_refresh", "report_export_to_data_exchange_job", "report_result_to_notification_delivery"},
			ValidationScopes: []string{"report.definition"},
		},
		Routes: routes, Operations: operations,
		Components: map[string]map[string]json.RawMessage{
			"schemas":         {"Error": json.RawMessage(`{"type":"object","required":["code"],"properties":{"code":{"type":"string"},"message":{"type":"string"},"params":{"type":"object","additionalProperties":{"type":"string"}}},"additionalProperties":false}`)},
			"securitySchemes": {"BearerAuth": json.RawMessage(`{"type":"http","scheme":"bearer","bearerFormat":"JWT"}`)},
		},
	})
	if err != nil {
		return nil, err
	}
	document.ValidationContracts = []modulecapability.ValidationScopeContract{{
		Kind: "report.definition", Description: "Validate one project report definition against Report's dataset or Object SQL contract.",
		Coverage: modulecapability.ValidationCoverageAllCandidates, CandidateCollections: []string{"reports"}, ReferencedCollections: []string{"objects"},
	}}
	summary := modulecapability.ModuleSummary{
		Identity: modulecapability.ModuleIdentity{
			Key: "report", SourceOwner: "report", ModuleVersion: "domainry-report-protocol-v3",
			ValidationRevision: "report-owner-validation-v1", SupportedDeploymentModes: []modulecapability.DeploymentMode{modulecapability.DeploymentModeModule},
		},
		Name: "Report", Description: "Source-owned report definitions, authorized analytical execution, materialized snapshots, stable paging, and governed export preparation.",
		Scenarios: modulecapability.AdaptationScenarios{
			UseWhen:              []string{"A PRD requires reusable analytical definitions, aggregates, grouped metrics, stable paged results, report snapshots, or governed exports"},
			DoNotUseWhen:         []string{"The requirement is a transactional record list or detail view that can be served directly by the owning business object without analytical definition or export governance"},
			RequirementSignals:   []string{"dashboard metric", "aggregate report", "group by", "materialized snapshot", "scheduled report", "CSV export", "cross-workspace aggregate"},
			ProvidedCapabilities: []string{"report.definition", "report.query", "report.snapshot", "report.stable_paging", "report.export_prepare"},
			RequiredModules:      []string{"identity"}, OptionalModules: []string{"audit", "data_exchange", "notification", "scheduler"}, ConflictingModules: []string{},
			AssemblyChains:    []string{"identity_scope_to_report_execution", "scheduler_to_report_snapshot_refresh", "report_export_to_data_exchange_job", "report_result_to_notification_delivery"},
			ValidationScopes:  []string{"report.definition"},
			SelectionExamples: []modulecapability.ScenarioExample{{Requirement: "Finance needs a role-scoped monthly revenue summary and an audited CSV export", Reason: "Report owns analytical definitions and governed export preparation; Data Exchange can assemble artifact delivery"}},
			RejectionExamples: []modulecapability.ScenarioExample{{Requirement: "Show the current customer's latest five orders", Reason: "A normal business-object query is sufficient unless reusable analytical or export semantics are required"}},
		},
	}
	return modulecapability.NewStaticBinding(summary, []modulecapability.CategoryDocument{document}, validator)
}

func ValidateCapabilityCandidate(ctx context.Context, request modulecapability.ValidationRequest) (modulecapability.ValidationResult, error) {
	result := modulecapability.ValidationResult{Diagnostics: []modulecapability.Diagnostic{}}
	invalid := func(rule, field string, err error) (modulecapability.ValidationResult, error) {
		message := "Report candidate is invalid"
		if err != nil && strings.TrimSpace(err.Error()) != "" {
			message = err.Error()
		}
		result.Diagnostics = append(result.Diagnostics, modulecapability.Diagnostic{Owner: "report", RuleKey: rule, Severity: modulecapability.SeverityError, FieldPath: field, Message: message})
		return result, nil
	}
	switch request.Kind {
	case "report.definition":
		var report reportmodel.ReportSchema
		if err := modulecapability.DecodeKeyedAuthoringValue(request.Candidate, "key", &report); err != nil {
			return invalid("report.definition.invalid_json", "$.candidate.value", err)
		}
		if report.Key != request.Candidate.Key {
			return invalid("report.definition.key_mismatch", "$.candidate.value.key", fmt.Errorf("report key %q differs from fragment key %q", report.Key, request.Candidate.Key))
		}
		if report.ObjectSQLV1 != nil {
			objects := make(map[string]reportquery.Object)
			for _, fragment := range modulecapability.ReferencedFragments(request, "objects") {
				var source reportObjectAuthoringFragment
				if err := modulecapability.DecodeKeyedAuthoringValue(fragment, "key", &source); err != nil {
					return invalid("report.definition.object_context_invalid", "$.referenced_context", err)
				}
				if source.Key != fragment.Key {
					return invalid("report.definition.object_context_key_mismatch", "$.referenced_context", fmt.Errorf("object key %q differs from fragment key %q", source.Key, fragment.Key))
				}
				fields := make([]reportquery.Field, 0, len(source.Fields))
				for _, field := range source.Fields {
					fields = append(fields, reportquery.Field{Key: field.Key, Type: field.Type, Precision: reportConfigInt(field.Config, "precision"), Scale: int32(reportConfigInt(field.Config, "scale"))})
				}
				objects[source.Key] = reportquery.Object{Key: source.Key, Fields: fields}
			}
			if _, err := reportobjectsql.CompileReportObjectSQL(*report.ObjectSQLV1, objects); err != nil {
				return invalid("report.definition.object_sql_invalid", "$.candidate.value.object_sql_v1", err)
			}
		} else if _, err := reportplan.BuildReportDatasetPlan(report); err != nil {
			return invalid("report.definition.dataset_invalid", "$.candidate.value.dataset", err)
		}
	default:
		return modulecapability.ValidationResult{}, &modulecapability.Error{StatusCode: 400, Code: "module_capability.validation_scope_invalid"}
	}
	_ = ctx
	return result, nil
}

func reportConfigInt(config map[string]any, key string) int {
	switch value := config[key].(type) {
	case float64:
		return int(value)
	case json.Number:
		parsed, _ := value.Int64()
		return int(parsed)
	case int:
		return value
	default:
		return 0
	}
}

var _ modulehttp.Surface = (*reportHTTPSurface)(nil)
