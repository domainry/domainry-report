package architecture

import (
	"os"
	"path/filepath"
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

func TestReportUsesInternalLayeredLayout(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	for _, required := range []string{
		"internal/application/report",
		"internal/domain/report/repository",
		"internal/domain/report/service/export",
		"internal/domain/report/service/objectsql",
		"internal/adapter/reportsdk",
		"internal/assembly/module",
		"internal/infrastructure/persistence/database/report",
		"internal/infrastructure/persistence/database/migration",
		"internal/infrastructure/persistence/database/schema",
		"module",
	} {
		if info, err := os.Stat(filepath.Join(root, required)); err != nil || !info.IsDir() {
			t.Errorf("required Report boundary %q is missing", required)
		}
	}
}

func TestPublicModuleIsThinFacade(t *testing.T) {
	entries, err := os.ReadDir("../../module")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || entry.Name() == "module.go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		t.Errorf("public module package contains implementation file %q", entry.Name())
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
