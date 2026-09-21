package capability

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/domainry/domainry-foundation/modulecapability"
	"github.com/domainry/domainry-foundation/modulecapability/contracttest"
	reportcontract "github.com/domainry/domainry-report/contract"
	reportadapter "github.com/domainry/domainry-report/internal/adapter/reportsdk"
)

func TestReportCapabilityTracksOwnerRoutesAndValidation(t *testing.T) {
	binding, err := buildContract(func(ctx context.Context, request modulecapability.ValidationRequest) (modulecapability.ValidationResult, error) {
		return validateCapabilityCandidate(ctx, request)
	})
	if err != nil {
		t.Fatal(err)
	}
	contracttest.VerifyBinding(t, binding)
	summary, err := binding.CapabilitySummary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Identity.SupportedDeploymentModes) != 1 || summary.Identity.SupportedDeploymentModes[0] != modulecapability.DeploymentModeModule {
		t.Fatalf("Report topology=%v", summary.Identity.SupportedDeploymentModes)
	}
	routes, _, err := reportadapter.CapabilityHTTPContract()
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Categories) != 1 || summary.Categories[0].OperationCount != len(routes) {
		t.Fatalf("Report categories=%+v routes=%d", summary.Categories, len(routes))
	}
	document, err := binding.CapabilityCategory(t.Context(), reportBusinessCategory)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Projections) != 1 || document.Projections[0].Kind != "report.authoring_schema" || document.Projections[0].Key != "object_sql_v1" ||
		!bytes.Contains(document.Projections[0].Payload, []byte(`"kind":{"enum":["dimension","measure"]`)) ||
		!bytes.Contains(document.Projections[0].Payload, []byte(`"x-domainry-supported-sql"`)) ||
		!bytes.Contains(document.Projections[0].Payload, []byte(`"oneOf":[{"required":["sql"]},{"required":["sql_file"]}]`)) ||
		!bytes.Contains(document.Projections[0].Payload, []byte(`"from_time":"datetime!"`)) ||
		!bytes.Contains(document.Projections[0].Payload, []byte(`Quote every model-derived Object and field identifier with MySQL backticks`)) ||
		!bytes.Contains(document.Projections[0].Payload, []byte("FROM `sale` s LEFT JOIN `payment` p")) ||
		bytes.Contains(document.Projections[0].Payload, []byte("FROM sale s")) ||
		!bytes.Contains(document.Projections[0].Payload, []byte(`"legacy_join_cardinalities":"verified assertion only"`)) ||
		bytes.Contains(document.Projections[0].Payload, []byte(`"enum":["dimension","measure","metric"]`)) {
		t.Fatalf("Report Object SQL authoring projection=%+v", document.Projections)
	}
	request := modulecapability.ValidationRequest{
		ContractVersion: modulecapability.ValidationContractVersion, ModuleKey: "report", CategoryKey: reportBusinessCategory,
		ContractSHA256: summary.Identity.ContractSHA256, Kind: "report.definition",
		Candidate: modulecapability.AuthoringFragment{
			Collection: "reports", Key: "orders",
			Value: json.RawMessage(`{"key":"orders"}`),
		},
	}
	result, err := binding.ValidateCapabilityCandidate(t.Context(), request)
	if err != nil || len(result.Diagnostics) != 1 || result.Diagnostics[0].Owner != "report" {
		t.Fatalf("Report diagnostics=%+v err=%v", result.Diagnostics, err)
	}
}

func TestReportCapabilityPublishesGovernedExportPrerequisites(t *testing.T) {
	binding, err := Open(Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	document, err := binding.CapabilityCategory(t.Context(), reportBusinessCategory)
	if err != nil {
		t.Fatal(err)
	}
	raw := document.OpenAPI.Paths["/report/{reportKey}/exports/{objectKey}/prepare"]["post"]
	var operation map[string]any
	if err := json.Unmarshal(raw, &operation); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		reportcontract.IdempotencyKeyHeader,
		reportcontract.OperationReasonHeader,
		reportcontract.OperationConfirmationHeader,
	} {
		parameter := reportOpenAPIParameter(reportOpenAPIParameters(operation), name)
		if parameter == nil || parameter["required"] != true || parameter["in"] != "header" {
			t.Fatalf("capability export prerequisite %s=%#v", name, parameter)
		}
	}
	confirmation := reportOpenAPIParameter(reportOpenAPIParameters(operation), reportcontract.OperationConfirmationHeader)
	if values := confirmation["schema"].(map[string]any)["enum"].([]any); len(values) != 1 || values[0] != reportcontract.OperationConfirmationConfirmed {
		t.Fatalf("capability export confirmation enum=%#v", values)
	}
	var extension modulecapability.OperationExtension
	payload, err := json.Marshal(operation[modulecapability.OperationExtensionKey])
	if err != nil || json.Unmarshal(payload, &extension) != nil {
		t.Fatalf("decode capability extension: payload=%s err=%v", payload, err)
	}
	if extension.Idempotency.Mode != "caller_key_required" {
		t.Fatalf("capability export idempotency=%#v", extension.Idempotency)
	}
	prerequisites, _ := operation["x-domainry-operation-prerequisites"].(map[string]any)
	if prerequisites["contract_version"] != "domainry-report-operation-prerequisites-v1" {
		t.Fatalf("capability export prerequisites=%#v", prerequisites)
	}
	codes, _ := prerequisites["error_codes"].([]any)
	wantCodes := []any{
		reportcontract.IdempotencyKeyRequiredErrorCode,
		reportcontract.OperationConfirmationRequiredErrorCode,
		reportcontract.OperationReasonEncodingErrorCode,
		reportcontract.OperationReasonRequiredErrorCode,
	}
	if !reflect.DeepEqual(codes, wantCodes) {
		t.Fatalf("capability export governance error codes=%#v want=%#v", codes, wantCodes)
	}
}

func TestReportCapabilityPreservesObjectSQLResultKindDiagnostic(t *testing.T) {
	binding, err := buildContract(func(ctx context.Context, request modulecapability.ValidationRequest) (modulecapability.ValidationResult, error) {
		return validateCapabilityCandidate(ctx, request)
	})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := binding.CapabilitySummary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	request := modulecapability.ValidationRequest{
		ContractVersion: modulecapability.ValidationContractVersion, ModuleKey: "report", CategoryKey: reportBusinessCategory,
		ContractSHA256: summary.Identity.ContractSHA256, Kind: "report.definition",
		Candidate: modulecapability.AuthoringFragment{Collection: "reports", Key: "sale_total", Value: json.RawMessage(`{
            "key":"sale_total","name":"Sale total","object_sql_v1":{
			  "sql":"SELECT s.\u0060amount\u0060 AS estimated_amount FROM \u0060sale\u0060 s LIMIT 10",
              "result_schema":[{"key":"estimated_amount","type":"currency","kind":"metric","precision":19,"scale":2}]
            }}`)},
		ReferencedContext: []modulecapability.AuthoringFragment{{Collection: "objects", Key: "sale", Value: json.RawMessage(`{
            "key":"sale","name":"Sale","description":"Sale","fields":[
              {"key":"amount","name":"Amount","type":"currency","config":{"precision":19,"scale":2},"required":true}
            ]}`)}},
	}
	result, err := binding.ValidateCapabilityCandidate(t.Context(), request)
	if err != nil || len(result.Diagnostics) != 1 {
		t.Fatalf("Report diagnostics=%+v err=%v", result.Diagnostics, err)
	}
	diagnostic := result.Diagnostics[0]
	if diagnostic.RuleKey != "report.definition.object_sql_invalid" || diagnostic.FieldPath != "$.candidate.value.object_sql_v1.result_schema[0].kind" ||
		diagnostic.Params["cause_code"] != "backend.report.object_sql_result_schema_invalid" || diagnostic.Params["invalid_field"] != "kind" ||
		diagnostic.Params["result_key"] != "estimated_amount" || diagnostic.Params["actual"] != "metric" ||
		diagnostic.Params["allowed_values"] != "dimension,measure" || diagnostic.Params["replacement_value"] != "measure" {
		t.Fatalf("Report kind diagnostic=%+v", diagnostic)
	}
}

func TestReportCapabilityAcceptsSQLOnlyStructureBoundToObjectJSONRelations(t *testing.T) {
	request := modulecapability.ValidationRequest{
		ContractVersion: modulecapability.ValidationContractVersion, ModuleKey: "report", CategoryKey: reportBusinessCategory, Kind: "report.definition",
		Candidate: modulecapability.AuthoringFragment{Collection: "reports", Key: "payments", Value: json.RawMessage(`{
            "key":"payments","object_sql_v1":{"sql":"SELECT s.\u0060store\u0060 AS store, COUNT(DISTINCT p.\u0060id\u0060) AS payment_count FROM \u0060sale\u0060 s LEFT JOIN \u0060payment\u0060 p ON p.\u0060sale_id\u0060 = s.\u0060id\u0060 GROUP BY s.\u0060store\u0060 ORDER BY s.\u0060store\u0060 LIMIT 100"}}`)},
		ReferencedContext: []modulecapability.AuthoringFragment{
			{Collection: "objects", Key: "sale", Value: json.RawMessage(`{"key":"sale","name":"Sale","description":"Sale","fields":[{"key":"store","name":"Store","type":"text","config":{},"required":true}]}`)},
			{Collection: "objects", Key: "payment", Value: json.RawMessage(`{"key":"payment","name":"Payment","description":"Payment","fields":[{"key":"sale_id","name":"Sale","type":"relation","config":{"target":"sale","cardinality":"many_to_one"},"required":true}]}`)},
		},
	}
	result, err := validateCapabilityCandidate(t.Context(), request)
	if err != nil || len(result.Diagnostics) != 0 {
		t.Fatalf("diagnostics=%+v err=%v", result.Diagnostics, err)
	}
}

func TestReportCapabilityAcceptsCompleteObjectLifecycleContext(t *testing.T) {
	tests := []struct {
		name   string
		object json.RawMessage
	}{
		{
			name: "append only",
			object: json.RawMessage(`{
                "key":"lead_audit","name":"Lead audit","description":"Append-only audit facts",
                "fields":[{"key":"event","name":"Event","type":"text","config":{},"required":true}],
                "capabilities":{"create":false,"read":true,"update":false,"delete":false,"export":true},
                "lifecycle_policy":{"mode":"append_only"},
                "ledger_policy":{"integrity":"sha256_chain","signature":"hmac_sha256"},
                "export_assurance_policy":{"required_methods":["password"],"recent_reauth_max_age_seconds":300}
            }`),
		},
		{
			name: "immutable after state",
			object: json.RawMessage(`{
                "key":"lead","name":"Lead","description":"Sales lead",
                "fields":[
                  {"key":"status","name":"Status","type":"text","config":{},"required":true},
                  {"key":"expected_amount","name":"Expected amount","type":"currency","config":{"precision":19,"scale":2},"required":true}
                ],
                "lifecycle_policy":{"mode":"immutable_after_state","state_field":"status","immutable_states":["converted","lost"]}
            }`),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var objectKey string
			var decoded map[string]any
			if err := json.Unmarshal(test.object, &decoded); err != nil {
				t.Fatal(err)
			}
			objectKey, _ = decoded["key"].(string)
			fieldKey := "event"
			if objectKey == "lead" {
				fieldKey = "expected_amount"
			}
			request := modulecapability.ValidationRequest{
				ContractVersion: modulecapability.ValidationContractVersion, ModuleKey: "report", CategoryKey: reportBusinessCategory, Kind: "report.definition",
				Candidate: modulecapability.AuthoringFragment{Collection: "reports", Key: "lifecycle_report", Value: json.RawMessage(fmt.Sprintf(`{
                    "key":"lifecycle_report","object_sql_v1":{"sql":"SELECT o.\u0060%s\u0060 AS value FROM \u0060%s\u0060 o LIMIT 10"}}
                `, fieldKey, objectKey))},
				ReferencedContext: []modulecapability.AuthoringFragment{{Collection: "objects", Key: objectKey, Value: test.object}},
			}
			result, err := validateCapabilityCandidate(t.Context(), request)
			if err != nil || len(result.Diagnostics) != 0 {
				t.Fatalf("diagnostics=%+v err=%v", result.Diagnostics, err)
			}
		})
	}
}

func TestReportCapabilityRejectsUnknownObjectContextFields(t *testing.T) {
	tests := []struct {
		name    string
		unknown string
		object  json.RawMessage
	}{
		{
			name: "object field", unknown: "runtime_hook",
			object: json.RawMessage(`{
                "key":"lead","name":"Lead","description":"Sales lead",
                "fields":[{"key":"status","name":"Status","type":"text","config":{},"required":true}],
                "lifecycle_policy":{"mode":"append_only"},"runtime_hook":"unsafe"
            }`),
		},
		{
			name: "lifecycle policy field", unknown: "on_transition",
			object: json.RawMessage(`{
                "key":"lead","name":"Lead","description":"Sales lead",
                "fields":[{"key":"status","name":"Status","type":"text","config":{},"required":true}],
                "lifecycle_policy":{"mode":"append_only","on_transition":"unsafe"}
            }`),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := modulecapability.ValidationRequest{
				ContractVersion: modulecapability.ValidationContractVersion, ModuleKey: "report", CategoryKey: reportBusinessCategory, Kind: "report.definition",
				Candidate: modulecapability.AuthoringFragment{Collection: "reports", Key: "lead_report", Value: json.RawMessage(`{
                    "key":"lead_report","object_sql_v1":{"sql":"SELECT l.\u0060status\u0060 AS status FROM \u0060lead\u0060 l LIMIT 10"}}
                `)},
				ReferencedContext: []modulecapability.AuthoringFragment{{Collection: "objects", Key: "lead", Value: test.object}},
			}
			result, err := validateCapabilityCandidate(t.Context(), request)
			if err != nil || len(result.Diagnostics) != 1 {
				t.Fatalf("diagnostics=%+v err=%v", result.Diagnostics, err)
			}
			diagnostic := result.Diagnostics[0]
			if diagnostic.RuleKey != "report.definition.object_context_invalid" || !strings.Contains(diagnostic.Message, `unknown field "`+test.unknown+`"`) {
				t.Fatalf("diagnostic=%+v", diagnostic)
			}
		})
	}
}

func TestReportCapabilityAcceptsFieldUpgradeRuleAndSensitiveMarker(t *testing.T) {
	request := modulecapability.ValidationRequest{
		ContractVersion: modulecapability.ValidationContractVersion, ModuleKey: "report", CategoryKey: reportBusinessCategory, Kind: "report.definition",
		Candidate: modulecapability.AuthoringFragment{Collection: "reports", Key: "lead_report", Value: json.RawMessage(`{
            "key":"lead_report","object_sql_v1":{"sql":"SELECT l.\u0060status\u0060 AS status FROM \u0060lead\u0060 l LIMIT 10"}}
        `)},
		ReferencedContext: []modulecapability.AuthoringFragment{{Collection: "objects", Key: "lead", Value: json.RawMessage(`{
            "key":"lead","name":"Lead","description":"Sales lead",
            "fields":[
                {"key":"status","name":"Status","type":"text","config":{},"required":true},
                {"key":"owner_ref","name":"Owner","type":"relation","config":{},"required":true,"upgrade":{"legacy":"exempt"}},
                {"key":"pin_fingerprint","name":"PIN","type":"text","config":{},"required":false,"sensitive":true}
            ]
        }`)}},
	}
	result, err := validateCapabilityCandidate(t.Context(), request)
	if err != nil || len(result.Diagnostics) != 0 {
		t.Fatalf("a field upgrade rule or sensitive marker must not invalidate the object context: diagnostics=%+v err=%v", result.Diagnostics, err)
	}
}

func TestReportCapabilityLifecycleContextStillEnforcesObjectSQLFields(t *testing.T) {
	request := modulecapability.ValidationRequest{
		ContractVersion: modulecapability.ValidationContractVersion, ModuleKey: "report", CategoryKey: reportBusinessCategory, Kind: "report.definition",
		Candidate: modulecapability.AuthoringFragment{Collection: "reports", Key: "lead_report", Value: json.RawMessage(`{
            "key":"lead_report","object_sql_v1":{"sql":"SELECT l.\u0060missing_amount\u0060 AS amount FROM \u0060lead\u0060 l LIMIT 10"}}
        `)},
		ReferencedContext: []modulecapability.AuthoringFragment{{Collection: "objects", Key: "lead", Value: json.RawMessage(`{
            "key":"lead","name":"Lead","description":"Sales lead",
            "fields":[{"key":"status","name":"Status","type":"text","config":{},"required":true}],
            "lifecycle_policy":{"mode":"immutable_after_state","state_field":"status","immutable_states":["converted","lost"]}
		}`)}},
	}
	result, err := validateCapabilityCandidate(t.Context(), request)
	if err != nil || len(result.Diagnostics) != 1 {
		t.Fatalf("diagnostics=%+v err=%v", result.Diagnostics, err)
	}
	diagnostic := result.Diagnostics[0]
	if diagnostic.RuleKey != "report.definition.object_sql_invalid" ||
		diagnostic.FieldPath != "$.candidate.value.object_sql_v1.sql" ||
		diagnostic.Params["cause_code"] != "backend.report.field_not_found" ||
		diagnostic.Params["field_key"] != "missing_amount" || diagnostic.Params["object"] != "lead" {
		t.Fatalf("diagnostic=%+v", diagnostic)
	}
}

func reportOpenAPIParameters(operation map[string]any) []map[string]any {
	values, _ := operation["parameters"].([]any)
	parameters := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if parameter, ok := value.(map[string]any); ok {
			parameters = append(parameters, parameter)
		}
	}
	return parameters
}

func reportOpenAPIParameter(parameters []map[string]any, name string) map[string]any {
	for _, parameter := range parameters {
		if parameter["name"] == name {
			return parameter
		}
	}
	return nil
}
