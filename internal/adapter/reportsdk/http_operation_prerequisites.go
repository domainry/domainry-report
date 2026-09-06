package reportsdk

import (
	"fmt"
	"sort"
	"strings"

	actioncontract "github.com/domainry/domainry-foundation/action"
	reportcontract "github.com/domainry/domainry-report/contract"
)

const reportOperationPrerequisitesContractVersion = "domainry-report-operation-prerequisites-v1"

// applyReportOperationPrerequisites projects the canonical Action governance
// metadata into standard OpenAPI header parameters plus a machine-readable
// source-owner extension. Runtime consumes the same Action metadata when it
// installs its gate; Plane consumes the operation from Report's capability
// registry, so neither needs a Report-specific route table.
func applyReportOperationPrerequisites(operation map[string]any, action actioncontract.ActionDefinition) error {
	if operation == nil {
		return fmt.Errorf("Report Action %q has no OpenAPI operation", action.Key)
	}
	if reportActionHasApprovalPolicy(action, actioncontract.ApprovalConfirmation) && !reportActionHasApprovalPolicy(action, actioncontract.ApprovalReason) {
		return fmt.Errorf("Report Action %q confirmation must explicitly declare its auditable reason prerequisite", action.Key)
	}
	parameters := reportOpenAPIParameterValues(operation)
	prerequisites := []any{}
	errorCodes := []string{}
	if action.IdempotencyDecision == "caller_key_required" {
		parameter := reportRequiredHeaderParameter(
			reportcontract.IdempotencyKeyHeader,
			"Stable caller-supplied key for one logical operation and all of its retries",
			map[string]any{"type": "string", "minLength": 1, "pattern": `\S`},
			action.Key+":<stable-logical-operation-id>",
		)
		parameters = reportUpsertOpenAPIHeaderParameter(parameters, parameter)
		prerequisites = append(prerequisites, map[string]any{
			"kind": "idempotency", "condition": map[string]any{"action_field": "idempotency_decision", "equals": "caller_key_required"},
			"header": parameter, "error_codes": []string{reportcontract.IdempotencyKeyRequiredErrorCode},
		})
		errorCodes = append(errorCodes, reportcontract.IdempotencyKeyRequiredErrorCode)
	}
	if reportActionHasApprovalPolicy(action, actioncontract.ApprovalReason) {
		parameter := reportRequiredHeaderParameter(
			reportcontract.OperationReasonHeader,
			"Human-supplied auditable reason for this governed operation",
			map[string]any{"type": "string", "minLength": 1, "pattern": `\S`},
			"Approved governed export for the stated business purpose",
		)
		parameters = reportUpsertOpenAPIHeaderParameter(parameters, parameter)
		prerequisites = append(prerequisites, map[string]any{
			"kind": "approval", "policy": string(actioncontract.ApprovalReason),
			"condition": map[string]any{"action_field": "approval_policies", "contains": string(actioncontract.ApprovalReason)},
			"header":    parameter, "error_codes": []string{reportcontract.OperationReasonRequiredErrorCode, reportcontract.OperationReasonEncodingErrorCode},
		})
		errorCodes = append(errorCodes, reportcontract.OperationReasonRequiredErrorCode, reportcontract.OperationReasonEncodingErrorCode)
	}
	if reportActionHasApprovalPolicy(action, actioncontract.ApprovalConfirmation) {
		parameter := reportRequiredHeaderParameter(
			reportcontract.OperationConfirmationHeader,
			"Explicit confirmation required by the Report Action contract",
			map[string]any{"type": "string", "enum": []string{reportcontract.OperationConfirmationConfirmed}},
			reportcontract.OperationConfirmationConfirmed,
		)
		parameters = reportUpsertOpenAPIHeaderParameter(parameters, parameter)
		prerequisites = append(prerequisites, map[string]any{
			"kind": "approval", "policy": string(actioncontract.ApprovalConfirmation),
			"condition": map[string]any{"action_field": "approval_policies", "contains": string(actioncontract.ApprovalConfirmation)},
			"header":    parameter, "error_codes": []string{reportcontract.OperationConfirmationRequiredErrorCode},
		})
		errorCodes = append(errorCodes, reportcontract.OperationConfirmationRequiredErrorCode)
	}
	if len(parameters) != 0 {
		operation["parameters"] = parameters
	}
	if len(prerequisites) == 0 {
		return nil
	}
	sort.Strings(errorCodes)
	operation["x-domainry-operation-prerequisites"] = map[string]any{
		"contract_version": reportOperationPrerequisitesContractVersion,
		"action_key":       action.Key,
		"risk_level":       string(action.RiskLevel),
		"prerequisites":    prerequisites,
		"error_codes":      append([]string(nil), errorCodes...),
	}
	responses, ok := operation["responses"].(map[string]any)
	if !ok {
		return fmt.Errorf("Report Action %q OpenAPI responses are invalid", action.Key)
	}
	response, _ := responses["400"].(map[string]any)
	if response == nil {
		response = reportOpenAPIErrorResponse("Invalid governed operation request")
		responses["400"] = response
	}
	response["x-domainry-error-codes"] = append([]string(nil), errorCodes...)
	return nil
}

func reportRequiredHeaderParameter(name, description string, schema map[string]any, example string) map[string]any {
	return map[string]any{
		"name": name, "in": "header", "required": true, "description": description,
		"schema": schema, "example": example, "x-domainry-normalization": "trim_space",
	}
}

func reportOpenAPIParameterValues(operation map[string]any) []any {
	if operation == nil {
		return nil
	}
	switch values := operation["parameters"].(type) {
	case []any:
		return append([]any(nil), values...)
	case []map[string]any:
		result := make([]any, 0, len(values))
		for _, value := range values {
			result = append(result, value)
		}
		return result
	default:
		return nil
	}
}

func reportUpsertOpenAPIHeaderParameter(parameters []any, replacement map[string]any) []any {
	for index, value := range parameters {
		parameter, _ := value.(map[string]any)
		if strings.EqualFold(fmt.Sprint(parameter["in"]), "header") && strings.EqualFold(fmt.Sprint(parameter["name"]), fmt.Sprint(replacement["name"])) {
			parameters[index] = replacement
			return parameters
		}
	}
	return append(parameters, replacement)
}

func reportActionHasApprovalPolicy(action actioncontract.ActionDefinition, wanted actioncontract.ApprovalPolicy) bool {
	for _, policy := range action.ApprovalPolicies {
		if policy == wanted {
			return true
		}
	}
	return false
}
