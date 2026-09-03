package reportsdk

import "testing"

func TestReportExportScopeContractDoesNotAcceptAuthorizationFacts(t *testing.T) {
	schema := reportExportScopeOpenAPISchema()
	if schema["additionalProperties"] != false {
		t.Fatalf("report export scope must reject unknown fields: %#v", schema)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("report export scope properties: %#v", schema["properties"])
	}
	for _, forbidden := range []string{"role_key", "data_scope", "data_scopes", "permissions", "data_permissions"} {
		if _, exists := properties[forbidden]; exists {
			t.Fatalf("client-controlled authorization fact %q leaked into report export scope", forbidden)
		}
	}
}
