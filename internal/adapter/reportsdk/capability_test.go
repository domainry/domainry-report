package reportsdk

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/domainry/domainry-foundation/modulecapability"
	"github.com/domainry/domainry-foundation/modulecapability/contracttest"
)

func TestReportCapabilityTracksOwnerRoutesAndValidation(t *testing.T) {
	binding, err := NewCapabilityBinding(func(ctx context.Context, request modulecapability.ValidationRequest) (modulecapability.ValidationResult, error) {
		return ValidateCapabilityCandidate(ctx, request)
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
	routes, _, err := reportHTTPContract()
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

func TestReportCapabilityPreservesObjectSQLResultKindDiagnostic(t *testing.T) {
	binding, err := NewCapabilityBinding(func(ctx context.Context, request modulecapability.ValidationRequest) (modulecapability.ValidationResult, error) {
		return ValidateCapabilityCandidate(ctx, request)
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
	result, err := ValidateCapabilityCandidate(t.Context(), request)
	if err != nil || len(result.Diagnostics) != 0 {
		t.Fatalf("diagnostics=%+v err=%v", result.Diagnostics, err)
	}
}
