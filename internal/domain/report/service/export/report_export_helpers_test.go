package export

import (
	"strings"
	"testing"
	"time"

	"github.com/domainry/domainry-foundation/apperror"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

func TestExportHelpersCoverClosedInputs(t *testing.T) {
	for _, test := range []struct {
		op     string
		values int
		want   bool
	}{{"is_null", 0, true}, {"not_null", 1, false}, {"eq", 1, true}, {"ne", 0, false}, {"between", 2, true}, {"between", 1, false}, {"in", 1, true}, {"not_in", 64, true}, {"in", 65, false}, {"unknown", 1, false}} {
		if got := validReportExportFilter(test.op, test.values); got != test.want {
			t.Fatalf("filter %s/%d=%v", test.op, test.values, got)
		}
	}
	field := reportmodel.ReportDatasetField{SourceAlias: "o", FieldKey: "value"}
	if got := reportMeasureFields(reportmodel.ReportDatasetMeasure{Field: &field, StartField: &field, EndField: &field}); len(got) != 3 {
		t.Fatalf("measure fields=%v", got)
	}
	if normalizeScopeStrings(nil, 1, 2) != nil || normalizeScopeStrings([]string{"a", "b"}, 1, 2) != nil || normalizeScopeStrings([]string{""}, 1, 2) != nil || normalizeScopeStrings([]string{"abc"}, 1, 2) != nil {
		t.Fatal("invalid scope strings accepted")
	}
	if got := normalizeScopeStrings([]string{" b ", "a", "a"}, 3, 2); strings.Join(got, ",") != "a,b" {
		t.Fatalf("scope strings=%v", got)
	}
	if got := normalizeScopeProjection([]string{" a ", "", "a", "b"}); strings.Join(got, ",") != "a,b" {
		t.Fatalf("projection=%v", got)
	}
	if got := stringValuesAsAny([]string{"a", "b"}); len(got) != 2 || got[1] != "b" {
		t.Fatalf("any values=%v", got)
	}
	if _, err := reportExportDate("2026-08-10T01:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := reportExportDate("2026-08-10"); err != nil {
		t.Fatal(err)
	}
	if _, err := reportExportDate("invalid"); err == nil {
		t.Fatal("invalid date accepted")
	}
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	from, err := reportExportDateBoundary("2026-08-10", shanghai, false)
	if err != nil || from.UTC().Format(time.RFC3339Nano) != "2026-08-09T16:00:00Z" {
		t.Fatalf("localized from=%s err=%v", from, err)
	}
	to, err := reportExportDateBoundary("2026-08-10", shanghai, true)
	if err != nil || to.UTC().Format(time.RFC3339Nano) != "2026-08-10T15:59:59.999999999Z" {
		t.Fatalf("localized to=%s err=%v", to, err)
	}
	if utc, err := reportExportDateBoundary("2026-08-10", nil, false); err != nil || utc.Location() != time.UTC {
		t.Fatalf("nil location boundary=%s err=%v", utc, err)
	}
	if _, err := CanonicalJSONSHA256(make(chan int)); err == nil {
		t.Fatal("unsupported JSON accepted")
	}
	if canonicalJSONEqual(make(chan int), map[string]string{}) {
		t.Fatal("unsupported JSON compared equal")
	}
	expected := []reportmodel.ReportMetricDefinitionRef{{Key: "a", Version: "v1"}, {Key: "b", Version: "v2"}}
	for _, requested := range [][]reportmodel.ReportMetricDefinitionRef{{{Key: "a", Version: "v1"}}, {{Key: "a", Version: "wrong"}, {Key: "b", Version: "v2"}}, {{Key: "", Version: "v1"}, {Key: "b", Version: "v2"}}, {{Key: "a", Version: ""}, {Key: "b", Version: "v2"}}, {{Key: "a", Version: "v1"}, {Key: "a", Version: "v1"}}} {
		if reportMetricDefinitionsEqual(requested, expected) {
			t.Fatalf("metric definitions accepted: %v", requested)
		}
	}
	if !reportMetricDefinitionsEqual([]reportmodel.ReportMetricDefinitionRef{{Key: " b ", Version: " v2 "}, {Key: "a", Version: "v1"}}, expected) {
		t.Fatal("ordered metric definition set rejected")
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
