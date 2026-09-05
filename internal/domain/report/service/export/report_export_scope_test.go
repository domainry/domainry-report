package export

import (
	"context"
	"testing"

	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

type objectSQLExportAuthorizationStub struct {
	source string
}

func (s *objectSQLExportAuthorizationStub) AuthorizeReportExportSource(_ context.Context, objectKey string) error {
	s.source = objectKey
	return nil
}

func (*objectSQLExportAuthorizationStub) AuthorizeReportExportField(context.Context, string, string) (bool, error) {
	return false, nil
}

func TestNormalizeObjectSQLExportScope(t *testing.T) {
	report := reportmodel.ReportSchema{Key: "orders", ObjectSQLV1: &reportmodel.ReportObjectSQLSchema{
		SQL:           "SELECT o.status AS status FROM orders o ORDER BY o.status LIMIT 100",
		SourceObjects: []string{"orders"},
		Parameters:    []reportmodel.ReportObjectSQLParameter{{Key: "status", Type: "text", Required: true}},
		ResultSchema:  []reportmodel.ReportResultColumnSchema{{Key: "status", Type: "text", Kind: "dimension"}},
	}}
	plan := reportmodel.ReportObjectSQLPlan{Sources: []reportmodel.ReportObjectSQLSource{{ObjectKey: "orders"}}, ResultSchema: report.ObjectSQLV1.ResultSchema}
	authorization := &objectSQLExportAuthorizationStub{}
	scope, _, _, err := NormalizeScope(t.Context(), report, plan, "orders", reportmodel.ReportExportControlSchema{SourceObjects: []string{"orders"}}, reportmodel.ReportExportScopeRequest{
		Parameters: map[string]any{"status": "paid"}, Purpose: " audit ",
	}, authorization)
	if err != nil {
		t.Fatal(err)
	}
	if authorization.source != "orders" || scope.Purpose != "audit" || len(scope.FieldProjection) != 1 || scope.FieldProjection[0] != "status" || scope.Freshness.Mode != "realtime" {
		t.Fatalf("scope=%+v source=%q", scope, authorization.source)
	}
	if err := ValidateFieldAccess(t.Context(), plan, authorization); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeObjectSQLExportScopeRejectsUndeclaredSourceAndColumn(t *testing.T) {
	report := reportmodel.ReportSchema{Key: "orders", ObjectSQLV1: &reportmodel.ReportObjectSQLSchema{
		SQL:           "SELECT o.status AS status FROM orders o ORDER BY o.status LIMIT 100",
		SourceObjects: []string{"orders"}, ResultSchema: []reportmodel.ReportResultColumnSchema{{Key: "status", Type: "text", Kind: "dimension"}},
	}}
	plan := reportmodel.ReportObjectSQLPlan{Sources: []reportmodel.ReportObjectSQLSource{{ObjectKey: "orders"}}, ResultSchema: report.ObjectSQLV1.ResultSchema}
	authorization := &objectSQLExportAuthorizationStub{}
	if _, _, _, err := NormalizeScope(t.Context(), report, plan, "orders", reportmodel.ReportExportControlSchema{}, reportmodel.ReportExportScopeRequest{Purpose: "audit"}, authorization); err == nil {
		t.Fatal("undeclared source accepted")
	}
	if _, _, _, err := NormalizeScope(t.Context(), report, plan, "orders", reportmodel.ReportExportControlSchema{SourceObjects: []string{"orders"}}, reportmodel.ReportExportScopeRequest{Purpose: "audit", FieldProjection: []string{"missing"}}, authorization); err == nil {
		t.Fatal("undeclared result column accepted")
	}
}

func TestNormalizeObjectSQLExportScopeUsesCanonicalPlanWhenAuthoredCopiesAreAbsent(t *testing.T) {
	report := reportmodel.ReportSchema{Key: "orders", ObjectSQLV1: &reportmodel.ReportObjectSQLSchema{
		SQL: "SELECT o.status AS status FROM orders o ORDER BY o.status LIMIT 100",
	}}
	plan := reportmodel.ReportObjectSQLPlan{
		Sources:      []reportmodel.ReportObjectSQLSource{{ObjectKey: "orders", Fields: []string{"status"}}},
		ResultSchema: []reportmodel.ReportResultColumnSchema{{Key: "status", Type: "text", Kind: "dimension"}},
	}
	authorization := &objectSQLExportAuthorizationStub{}
	scope, _, _, err := NormalizeScope(t.Context(), report, plan, "orders", reportmodel.ReportExportControlSchema{SourceObjects: []string{"orders"}}, reportmodel.ReportExportScopeRequest{Purpose: "audit"}, authorization)
	if err != nil || len(scope.FieldProjection) != 1 || scope.FieldProjection[0] != "status" {
		t.Fatalf("scope=%#v err=%v", scope, err)
	}
}
