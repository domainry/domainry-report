package report

import (
	"fmt"
	"strings"

	actioncontract "github.com/domainry/domainry-foundation/action"
	reportsdk "github.com/domainry/domainry-report-sdk"
)

const AuthorizationOwner = "module:report"

// AuthorizationActions is Report's complete public Action manifest. Report
// execution is authenticated-principal based and every public entry owns one
// same-key Permission. Report definitions may add narrower source permissions,
// but they never replace the executable entry Permission.
func AuthorizationActions() ([]actioncontract.ActionDefinition, error) {
	definitions := []actioncontract.ActionDefinition{
		reportAction(reportsdk.ActionReportSummaryGet, "GET /report/{reportKey}/summary", "Get report summary", actioncontract.EffectRead, actioncontract.RiskLow, "not_applicable", "owner_read_audit_policy"),
		reportAction(reportsdk.ActionReportQueryExecute, "POST /report/{reportKey}/query", "Execute report query", actioncontract.EffectRead, actioncontract.RiskLow, "not_applicable", "owner_read_audit_policy"),
		reportAction(reportsdk.ActionReportSnapshotsRefresh, "POST /report/{reportKey}/snapshots/refresh", "Refresh report snapshot", actioncontract.EffectWrite, actioncontract.RiskMedium, "caller_key_required", "mutation_audit_required"),
		reportAction(reportsdk.ActionReportExportsPrepare, "POST /report/{reportKey}/exports/{objectKey}/prepare", "Prepare report export", actioncontract.EffectWrite, actioncontract.RiskHigh, "caller_key_required", "business_export_prepare_audit", actioncontract.ApprovalReason, actioncontract.ApprovalConfirmation),
	}
	read := reportAction(reportsdk.ActionReportResultsRead, "", "Read saved report and analysis results", actioncontract.EffectRead, actioncontract.RiskLow, "not_applicable", "owner_read_audit_policy")
	read.HTTP, read.SourceKind = nil, "module"
	read.NonHTTP = []actioncontract.NonHTTPBinding{{Kind: "sdk", InvocationKey: reportsdk.ActionReportResultsRead}}
	definitions = append(definitions, read)
	result := make([]actioncontract.ActionDefinition, 0, len(definitions))
	for _, definition := range definitions {
		normalized, err := actioncontract.NormalizeDefinition(definition)
		if err != nil {
			return nil, fmt.Errorf("normalize Report Action %q: %w", definition.Key, err)
		}
		result = append(result, normalized)
	}
	return result, nil
}

func reportAction(key, pattern, label string, effect actioncontract.EffectClass, risk actioncontract.RiskLevel, idempotency, audit string, approvals ...actioncontract.ApprovalPolicy) actioncontract.ActionDefinition {
	method, path, _ := strings.Cut(strings.TrimSpace(pattern), " ")
	separator := strings.LastIndex(key, ".")
	resourceKey, operationKey := key[:separator], key[separator+1:]
	return actioncontract.ActionDefinition{
		Key: key, Owner: AuthorizationOwner, SourceKind: "module_http",
		CapabilityKey: reportsdk.CapabilityReportBusiness, CapabilityLabel: "Business reports",
		OperationKey: operationKey, OperationLabel: label, Label: label,
		Exposures:     []actioncontract.Exposure{actioncontract.ExposurePublic},
		Authorization: actioncontract.Authorization{Strategy: actioncontract.AuthorizationAuthenticated},
		HTTP:          &actioncontract.HTTPBinding{Method: method, RouteTemplate: path},
		Permission: &actioncontract.PermissionDefinition{
			Key: key, Owner: AuthorizationOwner, ModuleKey: "report", GroupKey: reportsdk.CapabilityReportBusiness,
			ResourceKey: resourceKey, OperationKey: operationKey, Label: label, Category: "Business reports",
			GrantMode: actioncontract.PermissionGrantInternal, ScopeKind: actioncontract.PermissionScopeNone,
			DefaultLocale: "en", I18n: reportPermissionText(resourceKey, operationKey, label),
			LifecycleStatus: actioncontract.LifecycleActive,
		},
		EffectClass: effect, RiskLevel: risk, ApprovalPolicies: append([]actioncontract.ApprovalPolicy(nil), approvals...),
		IdempotencyDecision: idempotency, AuditClass: audit, LifecycleStatus: actioncontract.LifecycleActive,
	}
}

func reportPermissionText(resourceKey, operationKey, englishLabel string) actioncontract.PermissionLocalizedText {
	resourceEnglish := map[string]string{
		"report.summary": "Report summary", "report.query": "Report query", "report.snapshots": "Report snapshots",
		"report.exports": "Report exports", "report.results": "Saved report results",
	}[resourceKey]
	resourceChinese := map[string]string{
		"report.summary": "报表摘要", "report.query": "报表查询", "report.snapshots": "报表快照",
		"report.exports": "报表导出", "report.results": "已保存的报表结果",
	}[resourceKey]
	operationEnglish := map[string]string{"get": "View", "execute": "Run query", "refresh": "Refresh", "prepare": "Prepare export", "read": "Read"}[operationKey]
	operationChinese := map[string]string{"get": "查看", "execute": "执行查询", "refresh": "刷新", "prepare": "准备导出", "read": "读取"}[operationKey]
	return actioncontract.PermissionLocalizedText{
		"en":    {ModuleLabel: "Reports", GroupLabel: "Business reports", ResourceLabel: resourceEnglish, OperationLabel: operationEnglish, Label: englishLabel, Description: "Provides access to " + resourceEnglish + " for the “" + operationEnglish + "” operation."},
		"zh-CN": {ModuleLabel: "报表", GroupLabel: "业务报表", ResourceLabel: resourceChinese, OperationLabel: operationChinese, Label: resourceChinese + " · " + operationChinese, Description: "允许对“" + resourceChinese + "”执行“" + operationChinese + "”操作。"},
	}
}
