package export

import (
	"strings"
	"testing"

	"github.com/domainry/domainry-foundation/apperror"
)

func TestObjectSQLExportHelpersCoverClosedInputs(t *testing.T) {
	if got := normalizeScopeProjection([]string{" a ", "", "a", "b"}); strings.Join(got, ",") != "a,b" {
		t.Fatalf("projection=%v", got)
	}
	if _, err := CanonicalJSONSHA256(make(chan int)); err == nil {
		t.Fatal("unsupported JSON accepted")
	}
	if canonicalJSONEqual(make(chan int), map[string]string{}) {
		t.Fatal("unsupported JSON compared equal")
	}
	if SafeFilename(" revenue / report ", "order:item") != "revenue___report-order_item.csv" || len(SHA256Hex([]byte("x"))) != 64 {
		t.Fatal("safe filename or hash mismatch")
	}
	if SafeFilename("azAZ09-_!{", "") != "azAZ09--.csv" {
		t.Fatal("safe filename character allowlist mismatch")
	}
	if apperror.CodeOf(exportScopeError("scope-error")) != "scope-error" {
		t.Fatal("scope error code mismatch")
	}
}
