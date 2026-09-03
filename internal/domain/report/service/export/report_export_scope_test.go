package export

import (
	"context"
	"testing"

	"github.com/domainry/domainry-foundation/apperror"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

type exportAuthorizationStub struct {
	masked          map[string]bool
	denied          map[string]error
	authorizeSource func(string)
}

func (s exportAuthorizationStub) AuthorizeReportExportSource(_ context.Context, objectKey string) error {
	if s.authorizeSource != nil {
		s.authorizeSource(objectKey)
	}
	return nil
}

func (s exportAuthorizationStub) AuthorizeReportExportField(_ context.Context, _, fieldKey string) (bool, error) {
	if err := s.denied[fieldKey]; err != nil {
		return false, err
	}
	return s.masked[fieldKey], nil
}

func (exportAuthorizationStub) AuthorizeReportObjectSQLExport(context.Context, reportmodel.ReportSchema) error {
	return nil
}

func TestNormalizeScopeOwnsProjectionMaskingAndCanonicalDateRange(t *testing.T) {
	report := exportPolicyReport()
	request := reportmodel.ReportExportScopeRequest{
		Purpose:         " audit evidence ",
		Filters:         []reportmodel.ReportExportFilter{{DimensionKey: "status", Operator: "in", Values: []string{"paid", "pending"}}},
		DateRange:       &reportmodel.ReportExportDateRange{DimensionKey: "day", From: "2026-08-01", To: "2026-08-10"},
		TimeZone:        "Asia/Shanghai",
		FieldProjection: []string{" status ", "orders", "status"},
		Freshness:       reportmodel.ReportExportFreshness{Mode: "realtime"},
	}
	authorization := exportAuthorizationStub{masked: map[string]bool{"status": true}}
	scope, scoped, masked, err := NormalizeScope(t.Context(), report, "order", reportmodel.ReportExportControlSchema{SourceObjects: []string{"order"}, MaskingRequired: true}, request, authorization)
	if err != nil {
		t.Fatal(err)
	}
	if scope.Purpose != "audit evidence" {
		t.Fatalf("scope=%#v", scope)
	}
	if len(scope.FieldProjection) != 2 || scope.FieldProjection[0] != "status" || scope.FieldProjection[1] != "orders" || !masked["status"] {
		t.Fatalf("projection=%v masked=%v", scope.FieldProjection, masked)
	}
	if len(scoped.Dataset.Dimensions) != 1 || len(scoped.Dataset.Measures) != 1 || len(scoped.Dataset.Filters) != 2 {
		t.Fatalf("scoped dataset=%#v", scoped.Dataset)
	}
	dateFilter := scoped.Dataset.Filters[1]
	if scoped.Dataset.TimeZone != "Asia/Shanghai" || len(dateFilter.Values) != 2 || dateFilter.Values[0] != "2026-07-31T16:00:00Z" || dateFilter.Values[1] != "2026-08-10T15:59:59.999999999Z" {
		t.Fatalf("timezone=%q date filter=%#v", scoped.Dataset.TimeZone, dateFilter)
	}
}

func TestDeclaredPredicatesAreClosedAndControlledByReport(t *testing.T) {
	report := exportPolicyReport()
	report.Dataset.QueryPredicates = []reportmodel.ReportDatasetPredicate{{
		Key: "current", Filters: []reportmodel.ReportDatasetFilter{{Field: reportmodel.ReportDatasetField{SourceAlias: "order", FieldKey: "status"}, Operator: "eq", Value: "paid"}},
	}}
	report.Dataset.TagPredicates = []reportmodel.ReportDatasetPredicate{{
		Key: "priority", Filters: []reportmodel.ReportDatasetFilter{{Field: reportmodel.ReportDatasetField{SourceAlias: "order", FieldKey: "priority"}, Operator: "eq", Value: true}},
	}}
	scoped, queryKey, tags, err := ApplyDeclaredPredicates(report, " current ", []string{"priority", "priority"}, []string{"current"}, []string{"priority"}, true)
	if err != nil || queryKey != "current" || len(tags) != 1 || tags[0] != "priority" || len(scoped.Dataset.Filters) != 2 {
		t.Fatalf("query=%q tags=%v filters=%v err=%v", queryKey, tags, scoped.Dataset.Filters, err)
	}
	if _, _, _, err = ApplyDeclaredPredicates(report, "current", nil, nil, nil, true); apperror.CodeOf(err) != "backend.report.query_not_allowed" {
		t.Fatalf("closed allowlist err=%v", err)
	}
}

func TestExportScopeKeepsPathPermissionAcrossJoinedSources(t *testing.T) {
	report := exportPolicyReport()
	report.Dataset.Joins = []reportmodel.ReportDatasetJoin{{
		Alias: "customer", ObjectKey: "customer", Type: "left", LeftAlias: "order",
		LeftField: "customer_id", RightField: "id", Cardinality: "many_to_one",
	}}
	calls := []string{}
	authorization := exportAuthorizationStub{authorizeSource: func(objectKey string) { calls = append(calls, objectKey) }}
	_, _, _, err := NormalizeScope(t.Context(), report, "order", reportmodel.ReportExportControlSchema{
		SourceObjects: []string{"order", "customer"},
	}, reportmodel.ReportExportScopeRequest{Purpose: "audit", Freshness: reportmodel.ReportExportFreshness{Mode: "realtime"}}, authorization)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != "order" {
		t.Fatalf("joined source changed the exact export Permission: %v", calls)
	}

	_, _, _, err = NormalizeScope(t.Context(), report, "order", reportmodel.ReportExportControlSchema{
		SourceObjects: []string{"order"},
	}, reportmodel.ReportExportScopeRequest{Purpose: "audit", Freshness: reportmodel.ReportExportFreshness{Mode: "realtime"}}, authorization)
	if apperror.CodeOf(err) != "backend.report.export_control_invalid" {
		t.Fatalf("unowned joined source err=%v", err)
	}
}

func TestObjectSQLExportScopeKeepsPathPermissionAcrossSources(t *testing.T) {
	report := reportmodel.ReportSchema{Key: "orders", ObjectSQLV1: &reportmodel.ReportObjectSQLSchema{
		SQL:           "SELECT o.id AS id FROM `order` AS o JOIN customer AS c ON c.id = o.customer_id",
		SourceObjects: []string{"order", "customer"},
		ResultSchema:  []reportmodel.ReportResultColumnSchema{{Key: "id", Type: "text", Kind: "dimension"}},
	}}
	calls := []string{}
	authorization := exportAuthorizationStub{authorizeSource: func(objectKey string) { calls = append(calls, objectKey) }}
	_, _, _, err := NormalizeScope(t.Context(), report, "order", reportmodel.ReportExportControlSchema{
		SourceObjects: []string{"order", "customer"},
	}, reportmodel.ReportExportScopeRequest{Purpose: "audit", Freshness: reportmodel.ReportExportFreshness{Mode: "realtime"}}, authorization)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != "order" {
		t.Fatalf("Object SQL source changed the exact export Permission: %v", calls)
	}
}

func exportPolicyReport() reportmodel.ReportSchema {
	return reportmodel.ReportSchema{Key: "orders", Dataset: reportmodel.ReportDatasetSchema{
		Source:   reportmodel.ReportDatasetSource{ObjectKey: "order", Alias: "order"},
		TimeZone: "UTC",
		Dimensions: []reportmodel.ReportDatasetDimension{
			{Key: "status", Field: reportmodel.ReportDatasetField{SourceAlias: "order", FieldKey: "status"}},
			{Key: "day", Field: reportmodel.ReportDatasetField{SourceAlias: "order", FieldKey: "occurred_at"}, TimeGrain: "day"},
		},
		Measures: []reportmodel.ReportDatasetMeasure{{Key: "orders", Operation: "count", SourceAlias: "order"}},
	}}
}
