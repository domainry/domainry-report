package report

import (
	"testing"

	actioncontract "github.com/domainry/domainry-foundation/action"
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
	if permissions := registry.PermissionDefinitions(); len(permissions) != 0 {
		t.Fatalf("authenticated Report Actions invented role Permissions: %#v", permissions)
	}
	for _, definition := range registry.Definitions() {
		if definition.Owner != AuthorizationOwner || definition.HTTP == nil || definition.Authorization.Strategy != actioncontract.AuthorizationAuthenticatedPrincipal || definition.Permission != nil {
			t.Fatalf("invalid Report Action: %#v", definition)
		}
	}
}
