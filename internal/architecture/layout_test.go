package architecture

import (
	"os"
	"strings"
	"testing"
)

func TestPersistenceImplementationUsesCanonicalInternalLayout(t *testing.T) {
	if _, err := os.Stat("../persistence"); !os.IsNotExist(err) {
		t.Fatal("Report must keep persistence under internal/infrastructure/persistence/database/report")
	}
	if info, err := os.Stat("../infrastructure/persistence/database/report"); err != nil || !info.IsDir() {
		t.Fatal("Report canonical persistence package is missing")
	}
}

func TestModuleUsesTaggedDependencies(t *testing.T) {
	content, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "replace ") || strings.Contains(string(content), "../domainry-") {
		t.Fatal("Report must consume released module tags, not local directory replacements")
	}
}
