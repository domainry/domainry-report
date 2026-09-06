package report

import (
	"reflect"
	"testing"

	actioncontract "github.com/domainry/domainry-foundation/action"
	reportsdk "github.com/domainry/domainry-report-sdk"
)

func TestAuthorizationActionsFreezeAsOneManifest(t *testing.T) {
	definitions, err := AuthorizationActions()
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 4 {
		t.Fatalf("Action count=%d", len(definitions))
	}
	registry := actioncontract.NewRegistry()
	if err := registry.Register(definitions...); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	if permissions := registry.PermissionDefinitions(); len(permissions) != 4 {
		t.Fatalf("Report exact Permissions=%#v", permissions)
	}
	for _, definition := range registry.Definitions() {
		if definition.Owner != AuthorizationOwner || definition.HTTP == nil || definition.Authorization.Strategy != actioncontract.AuthorizationAuthenticated || definition.Permission == nil || definition.Permission.Key != definition.Key {
			t.Fatalf("invalid Report Action: %#v", definition)
		}
		if definition.Key == reportsdk.ActionReportExportsPrepare {
			want := []actioncontract.ApprovalPolicy{actioncontract.ApprovalConfirmation, actioncontract.ApprovalReason}
			if definition.RiskLevel != actioncontract.RiskHigh || definition.IdempotencyDecision != "caller_key_required" || !reflect.DeepEqual(definition.ApprovalPolicies, want) {
				t.Fatalf("governed export Action=%#v", definition)
			}
		}
	}
}
