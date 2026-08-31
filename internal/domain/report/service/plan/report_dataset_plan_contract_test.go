package plan

import (
	"testing"

	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

func TestBuildReportDatasetPlanRejectsOneToManyAmountAmplification(t *testing.T) {
	report := reportmodel.ReportSchema{Key: "revenue", Dataset: reportmodel.ReportDatasetSchema{
		Source:   reportmodel.ReportDatasetSource{ObjectKey: "order", Alias: "orders"},
		Joins:    []reportmodel.ReportDatasetJoin{{ObjectKey: "order_line", Alias: "lines", Type: "inner", LeftAlias: "orders", LeftField: "id", RightField: "order_id", Cardinality: "one_to_many"}},
		Measures: []reportmodel.ReportDatasetMeasure{{Key: "paid", Operation: "sum", Field: reportPlanField("orders", "paid_amount")}},
	}}
	_, err := BuildReportDatasetPlan(report)
	if reportPlanErrorCode(err) != "backend.report.join_measure_amplification" {
		t.Fatalf("error=%v", err)
	}
	planErr := err.(*reportmodel.ReportDatasetPlanError)
	if planErr.Params["measure"] != "paid" || planErr.Params["join_alias"] != "lines" {
		t.Fatalf("params=%#v", planErr.Params)
	}
}

func TestBuildReportDatasetPlanAllowsMeasuresOnManySideAndDuplicateInsensitiveParentMeasures(t *testing.T) {
	report := reportmodel.ReportSchema{Key: "lines", Dataset: reportmodel.ReportDatasetSchema{
		Source: reportmodel.ReportDatasetSource{ObjectKey: "order", Alias: "orders"},
		Joins:  []reportmodel.ReportDatasetJoin{{ObjectKey: "order_line", Alias: "lines", Type: "inner", LeftAlias: "orders", LeftField: "id", RightField: "order_id", Cardinality: "one_to_many"}},
		Measures: []reportmodel.ReportDatasetMeasure{
			{Key: "line_total", Operation: "sum", Field: reportPlanField("lines", "amount")},
			{Key: "orders", Operation: "distinct_count", Field: reportPlanField("orders", "id")},
			{Key: "max_order", Operation: "max", Field: reportPlanField("orders", "paid_amount")},
		},
	}}
	plan, err := BuildReportDatasetPlan(report)
	if err != nil {
		t.Fatal(err)
	}
	if plan.AliasObjects["lines"] != "order_line" || len(plan.MeasureSources["line_total"]) != 1 || plan.MeasureSources["line_total"][0] != "lines" {
		t.Fatalf("plan=%#v", plan)
	}
}

func TestBuildReportDatasetPlanRejectsSiblingFanoutAndRatioCycles(t *testing.T) {
	report := reportmodel.ReportSchema{Key: "fanout", Dataset: reportmodel.ReportDatasetSchema{
		Source: reportmodel.ReportDatasetSource{ObjectKey: "account", Alias: "accounts"},
		Joins: []reportmodel.ReportDatasetJoin{
			{ObjectKey: "payment", Alias: "payments", Type: "inner", LeftAlias: "accounts", LeftField: "id", RightField: "account_id", Cardinality: "one_to_many"},
			{ObjectKey: "refund", Alias: "refunds", Type: "inner", LeftAlias: "accounts", LeftField: "id", RightField: "account_id", Cardinality: "one_to_many"},
		},
		Measures: []reportmodel.ReportDatasetMeasure{{Key: "payment_total", Operation: "sum", Field: reportPlanField("payments", "amount")}},
	}}
	if _, err := BuildReportDatasetPlan(report); reportPlanErrorCode(err) != "backend.report.join_measure_amplification" {
		t.Fatalf("sibling fanout error=%v", err)
	}

	report.Dataset.Joins = nil
	report.Dataset.Measures = []reportmodel.ReportDatasetMeasure{
		{Key: "a", Operation: "ratio", NumeratorKey: "b", DenominatorKey: "base"},
		{Key: "b", Operation: "ratio", NumeratorKey: "a", DenominatorKey: "base"},
		{Key: "base", Operation: "count"},
	}
	if _, err := BuildReportDatasetPlan(report); reportPlanErrorCode(err) != "backend.report.measure_invalid" {
		t.Fatalf("ratio cycle error=%v", err)
	}
}

func TestBuildReportDatasetPlanRejectsInvalidAliasGraph(t *testing.T) {
	for name, report := range map[string]reportmodel.ReportSchema{
		"missing root":    {},
		"unknown left":    {Dataset: reportmodel.ReportDatasetSchema{Source: reportmodel.ReportDatasetSource{ObjectKey: "order", Alias: "orders"}, Joins: []reportmodel.ReportDatasetJoin{{ObjectKey: "line", Alias: "lines", Type: "inner", LeftAlias: "missing", LeftField: "id", RightField: "order_id", Cardinality: "one_to_many"}}}},
		"duplicate alias": {Dataset: reportmodel.ReportDatasetSchema{Source: reportmodel.ReportDatasetSource{ObjectKey: "order", Alias: "orders"}, Joins: []reportmodel.ReportDatasetJoin{{ObjectKey: "line", Alias: "orders", Type: "inner", LeftAlias: "orders", LeftField: "id", RightField: "order_id", Cardinality: "one_to_many"}}}},
		"bad cardinality": {Dataset: reportmodel.ReportDatasetSchema{Source: reportmodel.ReportDatasetSource{ObjectKey: "order", Alias: "orders"}, Joins: []reportmodel.ReportDatasetJoin{{ObjectKey: "line", Alias: "lines", Type: "inner", LeftAlias: "orders", LeftField: "id", RightField: "order_id", Cardinality: "many_to_many"}}}},
		"bad join type":   {Dataset: reportmodel.ReportDatasetSchema{Source: reportmodel.ReportDatasetSource{ObjectKey: "order", Alias: "orders"}, Joins: []reportmodel.ReportDatasetJoin{{ObjectKey: "line", Alias: "lines", Type: "cross", LeftAlias: "orders", LeftField: "id", RightField: "order_id", Cardinality: "one_to_many"}}}},
		"missing fields":  {Dataset: reportmodel.ReportDatasetSchema{Source: reportmodel.ReportDatasetSource{ObjectKey: "order", Alias: "orders"}, Joins: []reportmodel.ReportDatasetJoin{{ObjectKey: "line", Alias: "lines", Type: "inner", LeftAlias: "orders", Cardinality: "one_to_many"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := BuildReportDatasetPlan(report); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func reportPlanField(alias, field string) *reportmodel.ReportDatasetField {
	return &reportmodel.ReportDatasetField{SourceAlias: alias, FieldKey: field}
}

func reportPlanErrorCode(err error) string {
	if coded, ok := err.(interface{ ErrorCode() string }); ok {
		return coded.ErrorCode()
	}
	return ""
}
