package reportsdk

import (
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
	if len(summary.Categories) != 1 || summary.Categories[0].OperationCount != len(newReportHTTPSurface(&Binding{}).Routes()) {
		t.Fatalf("Report categories=%+v routes=%d", summary.Categories, len(newReportHTTPSurface(&Binding{}).Routes()))
	}
	request := modulecapability.ValidationRequest{
		ContractVersion: modulecapability.ValidationContractVersion, ModuleKey: "report", CategoryKey: reportBusinessCategory,
		ContractSHA256: summary.Identity.ContractSHA256, Kind: "report.definition",
		Candidate: modulecapability.AuthoringFragment{
			Collection: "reports", Key: "orders",
			Value: json.RawMessage(`{"key":"orders","dataset":{"source":{}}}`),
		},
	}
	result, err := binding.ValidateCapabilityCandidate(t.Context(), request)
	if err != nil || len(result.Diagnostics) != 1 || result.Diagnostics[0].Owner != "report" {
		t.Fatalf("Report diagnostics=%+v err=%v", result.Diagnostics, err)
	}
}
