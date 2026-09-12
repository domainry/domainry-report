package architecture

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestReportApplicationConsumesOnlyOwnerDomainAndPublicHostContracts(t *testing.T) {
	root := filepath.Join("..", "application")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, im := range file.Imports {
			name, err := strconv.Unquote(im.Path.Value)
			if err != nil {
				return err
			}
			if name == "database/sql" || name == "net/http" {
				t.Errorf("Report application imports concrete I/O: %s -> %s", path, name)
			}
			if !strings.HasPrefix(name, "github.com/domainry/") {
				continue
			}
			module := strings.Split(strings.TrimPrefix(name, "github.com/domainry/"), "/")[0]
			if module == "domainry-report" {
				if !strings.HasPrefix(name, "github.com/domainry/domainry-report/internal/domain/") && !strings.HasPrefix(name, "github.com/domainry/domainry-report/internal/application/") {
					t.Errorf("Report application depends on its outer layer: %s -> %s", path, name)
				}
			} else if !strings.HasSuffix(module, "-sdk") && module != "domainry-foundation" {
				t.Errorf("Report application imports another owner implementation: %s -> %s", path, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

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
