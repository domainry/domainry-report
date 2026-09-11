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
)

const reportBusinessCategory = reportsdk.CapabilityReportBusiness

type reportObjectAuthoringFragment struct {
	Key                   string                                    `json:"key"`
	Name                  string                                    `json:"name"`
	Description           string                                    `json:"description"`
	I18n                  json.RawMessage                           `json:"i18n,omitempty"`
	Fields                []reportObjectFieldAuthoringFragment      `json:"fields"`
	Validations           json.RawMessage                           `json:"validations,omitempty"`
	Capabilities          *reportObjectCapabilityContext            `json:"capabilities,omitempty"`
	LifecyclePolicy       *reportObjectLifecyclePolicyContext       `json:"lifecycle_policy,omitempty"`
	LedgerPolicy          *reportObjectLedgerPolicyContext          `json:"ledger_policy,omitempty"`
	ExportAssurancePolicy *reportObjectExportAssurancePolicyContext `json:"export_assurance_policy,omitempty"`
	UX                    json.RawMessage                           `json:"ux,omitempty"`
	Config                json.RawMessage                           `json:"config,omitempty"`
	VersionHistory        json.RawMessage                           `json:"version_history,omitempty"`
	Provenance            json.RawMessage                           `json:"provenance,omitempty"`
}

// These types mirror the non-query metadata disclosed by Plane's complete
// Object authoring fragment. Strict decoding keeps the context boundary closed
// to unknown fields, while Report deliberately does not interpret or enforce
// these owner-governed policies.
type reportObjectCapabilityContext struct {
	Create *bool `json:"create,omitempty"`
	Read   *bool `json:"read,omitempty"`
	Update *bool `json:"update,omitempty"`
	Delete *bool `json:"delete,omitempty"`
	Export *bool `json:"export,omitempty"`
}

type reportObjectLifecyclePolicyContext struct {
	Mode            string   `json:"mode"`
	StateField      string   `json:"state_field,omitempty"`
	ImmutableStates []string `json:"immutable_states,omitempty"`
}

type reportObjectLedgerPolicyContext struct {
	Integrity string `json:"integrity,omitempty"`
	Signature string `json:"signature,omitempty"`
}

type reportObjectExportAssurancePolicyContext struct {
	RequiredMethods           []string `json:"required_methods"`
	RecentReauthMaxAgeSeconds int      `json:"recent_reauth_max_age_seconds,omitempty"`
}

type reportObjectFieldAuthoringFragment struct {
	Key          string          `json:"key"`
	Name         string          `json:"name"`
	Type         string          `json:"type"`
	I18n         json.RawMessage `json:"i18n,omitempty"`
	Config       map[string]any  `json:"config"`
	Validation   json.RawMessage `json:"validation,omitempty"`
	Options      json.RawMessage `json:"options,omitempty"`
	Required     bool            `json:"required"`
	Unique       bool            `json:"unique,omitempty"`
	DefaultValue json.RawMessage `json:"default_value,omitempty"`
	// Upgrade and Sensitive are field-level authoring properties Plane retains on
	// its complete Object fragment (definition-upgrade rules for required fields
	// added to populated Objects, and the credential-derivative marker). Report
	// neither interprets nor enforces them; naming them keeps the strict decoder
	// from refusing an ordinary Object as an invalid report context.
	Upgrade        json.RawMessage `json:"upgrade,omitempty"`
	Sensitive      bool            `json:"sensitive,omitempty"`
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
	objectSQLContract, err := reportObjectSQLAuthoringContract()
	if err != nil {
		return nil, err
	}
	document.Projections = append(document.Projections, objectSQLContract)
	document.ValidationContracts = []modulecapability.ValidationScopeContract{{
		Kind: "report.definition", Description: "Validate one project report definition against Report's object_sql_v1 contract.",
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
	invalidWithParams := func(rule, field string, err error, params map[string]string) (modulecapability.ValidationResult, error) {
		message := "Report candidate is invalid"
		if err != nil && strings.TrimSpace(err.Error()) != "" {
			message = err.Error()
		}
		result.Diagnostics = append(result.Diagnostics, modulecapability.Diagnostic{Owner: "report", RuleKey: rule, Severity: modulecapability.SeverityError, FieldPath: field, Message: message, Params: params})
		return result, nil
	}
	invalid := func(rule, field string, err error) (modulecapability.ValidationResult, error) {
		return invalidWithParams(rule, field, err, nil)
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
					fields = append(fields, reportquery.Field{
						Key: field.Key, Type: field.Type, Precision: reportConfigInt(field.Config, "precision"), Scale: int32(reportConfigInt(field.Config, "scale")), Unique: field.Unique,
						RelationTarget: reportRelationTarget(field), RelationCardinality: reportRelationCardinality(field),
					})
				}
				objects[source.Key] = reportquery.Object{Key: source.Key, Fields: fields}
			}
			if _, err := reportobjectsql.CompileReportObjectSQL(*report.ObjectSQLV1, objects); err != nil {
				if planErr, ok := err.(*reportmodel.ReportObjectSQLPlanError); ok {
					params := make(map[string]string, len(planErr.Params)+2)
					for key, value := range planErr.Params {
						params[key] = value
					}
					params["cause_code"] = planErr.Code
					params["cause_path"] = planErr.Path
					return invalidWithParams("report.definition.object_sql_invalid", "$.candidate.value."+planErr.Path, err, params)
				}
				return invalid("report.definition.object_sql_invalid", "$.candidate.value.object_sql_v1", err)
			}
		} else {
			return invalid("report.definition.object_sql_required", "$.candidate.value.object_sql_v1", fmt.Errorf("object_sql_v1 is required"))
		}
	default:
		return modulecapability.ValidationResult{}, &modulecapability.Error{StatusCode: 400, Code: "module_capability.validation_scope_invalid"}
	}
	_ = ctx
	return result, nil
}

func reportObjectSQLAuthoringContract() (modulecapability.SourceProjection, error) {
	column := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"type":      map[string]any{"type": "string", "enum": []string{"text", "integer", "number", "decimal", "boolean", "date", "datetime", "currency"}},
			"kind":      map[string]any{"type": "string", "enum": []string{"dimension", "measure"}},
			"precision": map[string]any{"type": "integer"}, "scale": map[string]any{"type": "integer"},
		},
	}
	payload, err := json.Marshal(map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"title":   "Compact Object SQL v1 authoring contract",
		"type":    "object", "additionalProperties": false,
		"oneOf": []any{map[string]any{"required": []string{"sql"}}, map[string]any{"required": []string{"sql_file"}}},
		"properties": map[string]any{
			"sql":      map[string]any{"type": "string", "description": "One SELECT statement over model Object keys; SQL is the structural source of truth."},
			"sql_file": map[string]any{"type": "string", "description": "Exactly backend/reports/<report-key>.sql; Plane loads and hashes it before compilation."},
			"parameters": map[string]any{
				"type": "object", "description": "Optional parameters keyed by SQL placeholder name. Values use compact field DSL such as text! or datetime.",
				"additionalProperties": map[string]any{"type": "string"},
			},
			"result_schema":        map[string]any{"type": "object", "description": "Optional semantic overrides keyed by SELECT alias; type/order are derived from SQL.", "additionalProperties": column},
			"timeout_milliseconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 30000},
			"time_zone":            map[string]any{"type": "string"},
		},
		"x-domainry-path": "reports.<report>.object_sql_v1",
		"x-domainry-derived-fields": map[string]any{
			"source_objects": "FROM/JOIN Object keys", "result_schema.key_type_order": "SELECT aliases and bound expression types", "join_cardinality": "Object relation/unique metadata",
		},
		"x-domainry-supported-sql": map[string]any{
			"statement": []string{"SELECT"}, "joins": []string{"INNER JOIN ... ON", "LEFT JOIN ... ON"},
			"clauses":    []string{"WHERE", "GROUP BY", "HAVING", "ORDER BY", "LIMIT"},
			"aggregates": []string{"COUNT", "SUM", "AVG", "MIN", "MAX"},
			"functions":  []string{"ROUND", "FLOOR", "COALESCE", "NULLIF", "DATE_BUCKET", "CASE"},
			"forbidden":  []string{"SELECT *", "subqueries", "CTEs", "UNION", "window functions", "DML/DDL", "string literals"},
		},
		"x-domainry-identifiers": map[string]any{
			"aliases_required": true, "qualified_fields_required": true,
			"quoting": "Quote every model-derived Object and field identifier with MySQL backticks, even when the key is not a SQL keyword; keep aliases, parameter names, and result aliases unquoted.",
		},
		"x-domainry-join-proof": map[string]any{
			"authoring": "compiler_inferred", "metadata_source": "project Object JSON field.type/config/unique", "many_to_many": "rejected", "unprovable": "rejected with candidate relation fields", "legacy_join_cardinalities": "verified assertion only",
		},
		"x-domainry-result-semantics": map[string]any{
			"dimension": "default for non-aggregate expressions; optional override for business semantics",
			"measure":   "default for aggregate expressions; optional override for business semantics", "forbidden_aliases": []string{"metric"},
		},
		"x-domainry-parameter-semantics": map[string]any{
			"compact_field_dsl": true, "types": []string{"text", "integer", "number", "decimal", "boolean", "date", "datetime"},
			"required_suffix": "!", "binding": "SQL :name must match the surrounding map key",
		},
		"x-domainry-limits": map[string]any{"sql_characters": 32768, "joins": 8, "columns": 64, "limit_rows": 10000, "default_limit_rows": 1000, "timeout_milliseconds": 30000},
		"examples": []any{
			map[string]any{"sql_file": "backend/reports/payments_by_store.sql", "parameters": map[string]any{"from_time": "datetime!"}},
			map[string]any{"sql": "SELECT s.`store` AS store, COUNT(DISTINCT p.`id`) AS payment_count FROM `sale` s LEFT JOIN `payment` p ON p.`sale_id` = s.`id` GROUP BY s.`store` ORDER BY s.`store` LIMIT 100"},
		},
	})
	if err != nil {
		return modulecapability.SourceProjection{}, err
	}
	return modulecapability.SourceProjection{Kind: "report.authoring_schema", Key: "object_sql_v1", Payload: payload}, nil
}

func reportRelationTarget(field reportObjectFieldAuthoringFragment) string {
	for _, key := range []string{"target", "object_key"} {
		if value := strings.TrimSpace(fmt.Sprint(field.Config[key])); value != "" && value != "<nil>" {
			return value
		}
	}
	var validation struct {
		Target string `json:"target"`
	}
	_ = json.Unmarshal(field.Validation, &validation)
	return strings.TrimSpace(validation.Target)
}

func reportRelationCardinality(field reportObjectFieldAuthoringFragment) string {
	if strings.TrimSpace(field.Type) != "relation" {
		return ""
	}
	value := strings.TrimSpace(fmt.Sprint(field.Config["cardinality"]))
	if value == "" || value == "<nil>" {
		return "many_to_one"
	}
	return value
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

var _ modulehttp.Adapter = (*reportHTTPAdapter)(nil)
