package analysis

import (
	"encoding/json"
	"strings"
	"testing"

	model "github.com/domainry/domainry-report-sdk/model"
	query "github.com/domainry/domainry-report-sdk/query"
	objectsql "github.com/domainry/domainry-report-sdk/query/objectsql"
)

func testDataset() model.AnalysisDataset {
	return model.AnalysisDataset{Key: "sale", Name: "Sales", Kind: "business_object", Version: "schema-1", Columns: []model.AnalysisColumn{
		{Key: "id", Type: "text"}, {Key: "department", Type: "text"}, {Key: "name", Type: "text"},
		{Key: "amount", Type: "currency", Precision: 19, Scale: 2, Unit: "CNY"}, {Key: "quantity", Type: "integer", Unit: "items"},
		{Key: "occurred_at", Type: "datetime"}, {Key: "posted", Type: "boolean"},
	}}
}

func bindSQL(t *testing.T, plan Plan) {
	t.Helper()
	object := query.Object{Key: plan.Dataset.Key}
	for _, field := range plan.Dataset.Columns {
		object.Fields = append(object.Fields, query.Field{Key: field.Key, Type: field.Type, Precision: field.Precision, Scale: int32(field.Scale)})
	}
	for _, q := range plan.Queries {
		bound, err := objectsql.CompileReportObjectSQL(*q.Report.ObjectSQLV1, map[string]query.Object{object.Key: object})
		if err != nil {
			t.Fatalf("generated analysis SQL is not an executable Report plan: %v\n%s", err, q.Report.ObjectSQLV1.SQL)
		}
		if bound.Limit != plan.Spec.MaxRows+1 {
			t.Fatal("lost output overflow sentinel", bound.Limit)
		}
		if _, err := objectsql.NormalizeDeclaredParameters(bound.Parameters, q.Parameters); err != nil {
			t.Fatal(err, q.Parameters)
		}
	}
}

func TestStructuredAnalysisLowersThroughRealReportCompiler(t *testing.T) {
	base := model.AnalysisRequest{DatasetKey: "sale", GroupBy: []string{"department"}, Measures: []model.AnalysisMeasure{{Key: "revenue", Function: "sum", Field: "amount"}, {Key: "average", Function: "avg", Field: "amount"}, {Key: "orders", Function: "count"}}, MaxRows: 50}
	for _, mode := range []string{"aggregate", "compare", "trend", "table"} {
		t.Run(mode, func(t *testing.T) {
			request := base
			request.Mode = mode
			switch mode {
			case "compare":
				request.Comparison = &model.AnalysisComparison{Baseline: []model.AnalysisFilter{{Field: "posted", Operator: "eq", Values: []any{false}}}, Current: []model.AnalysisFilter{{Field: "posted", Operator: "eq", Values: []any{true}}}}
			case "trend":
				request.Time = &model.AnalysisTimeBucket{Field: "occurred_at", Grain: "month", TimeZone: "Asia/Shanghai"}
			case "table":
				request.GroupBy = nil
				request.Measures = nil
				request.Select = []string{"name", "amount", "quantity"}
				request.Calculations = []model.AnalysisCalculation{{Key: "line_total", Scale: 2, Expression: model.AnalysisExpression{Operator: "multiply", Arguments: []model.AnalysisExpression{{Reference: "amount"}, {Reference: "quantity"}}}}}
			}
			plan, err := Compile(request, testDataset())
			if err != nil {
				t.Fatal(err)
			}
			bindSQL(t, plan)
			if mode == "compare" && len(plan.Queries) != 2 {
				t.Fatal("comparison did not preserve independent filters")
			}
			if mode == "aggregate" && (!strings.Contains(plan.Queries[0].Report.ObjectSQLV1.SQL, "SUM(d.`amount`) AS m1") || !strings.Contains(plan.Queries[0].Report.ObjectSQLV1.SQL, "COUNT(d.`amount`) AS n1")) {
				t.Fatal("average must retain exact sum and non-null count")
			}
		})
	}
}

func TestAnalysisFiltersAreBoundAndTyped(t *testing.T) {
	text := "x' OR 1=1 --"
	r := model.AnalysisRequest{DatasetKey: "sale", Measures: []model.AnalysisMeasure{{Key: "orders", Function: "count"}}, Filters: []model.AnalysisFilter{{Any: []model.AnalysisFilter{{Field: "name", Operator: "contains", Values: []any{text}}, {Field: "quantity", Operator: "ge", Values: []any{json.Number("9007199254740993")}}}}}}
	plan, err := Compile(r, testDataset())
	if err != nil {
		t.Fatal(err)
	}
	bindSQL(t, plan)
	q := plan.Queries[0]
	if strings.Contains(q.Report.ObjectSQLV1.SQL, text) || q.Parameters["p0"] != text || q.Parameters["p1"] != json.Number("9007199254740993") {
		t.Fatal("unsafe or inexact parameters", q)
	}
}

func TestAnalysisRejectsUnboundOrAmbiguousSpecifications(t *testing.T) {
	base := model.AnalysisRequest{DatasetKey: "sale", Measures: []model.AnalysisMeasure{{Key: "revenue", Function: "sum", Field: "amount"}}}
	cases := map[string]func(*model.AnalysisRequest){
		"unknown_dataset": func(r *model.AnalysisRequest) { r.DatasetKey = "secret" },
		"unknown_field":   func(r *model.AnalysisRequest) { r.GroupBy = []string{"secret"} },
		"sql_operator": func(r *model.AnalysisRequest) {
			r.Filters = []model.AnalysisFilter{{Field: "amount", Operator: "=0 OR 1=1", Values: []any{1}}}
		},
		"identity_value": func(r *model.AnalysisRequest) {
			r.Filters = []model.AnalysisFilter{{Field: "amount", Operator: "eq", Values: []any{map[string]any{"user_id": "admin"}}}}
		},
		"numeric_string": func(r *model.AnalysisRequest) {
			r.Filters = []model.AnalysisFilter{{Field: "amount", Operator: "eq", Values: []any{"10"}}}
		},
		"negative_limit":   func(r *model.AnalysisRequest) { r.MaxRows = -1 },
		"mixed_modes":      func(r *model.AnalysisRequest) { r.Mode = "table"; r.Select = []string{"amount"} },
		"output_collision": func(r *model.AnalysisRequest) { r.Measures = append(r.Measures, r.Measures[0]) },
		"future_calculation": func(r *model.AnalysisRequest) {
			r.Calculations = []model.AnalysisCalculation{{Key: "x", Expression: model.AnalysisExpression{Reference: "future"}}}
		},
		"incompatible_units": func(r *model.AnalysisRequest) {
			r.Measures = append(r.Measures, model.AnalysisMeasure{Key: "items", Function: "sum", Field: "quantity"})
			r.Calculations = []model.AnalysisCalculation{{Key: "x", Expression: model.AnalysisExpression{Operator: "add", Arguments: []model.AnalysisExpression{{Reference: "revenue"}, {Reference: "items"}}}}}
		},
		"unknown_rule_column": func(r *model.AnalysisRequest) {
			r.AnomalyRules = []model.AnalysisAnomalyRule{{Key: "high", Column: "hidden", Operator: "gt", Values: []string{"1"}}}
		},
		"timezone": func(r *model.AnalysisRequest) {
			r.Mode = "trend"
			r.Time = &model.AnalysisTimeBucket{Field: "occurred_at", Grain: "month", TimeZone: "not-a-timezone"}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := base
			mutate(&r)
			if _, err := Compile(r, testDataset()); err == nil {
				t.Fatal("invalid analysis accepted")
			}
		})
	}
}
