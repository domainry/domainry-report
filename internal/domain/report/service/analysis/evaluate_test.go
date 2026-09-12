package analysis

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	model "github.com/domainry/domainry-report-sdk/model"
)

func mustCompile(t *testing.T, request model.AnalysisRequest) Plan {
	t.Helper()
	request.DatasetKey = "sale"
	p, err := Compile(request, testDataset())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func evaluate(t *testing.T, p Plan, rows ...[]map[string]string) Evaluation {
	t.Helper()
	results := make([]model.ReportObjectSQLExecutionResult, len(rows))
	for i, r := range rows {
		results[i] = model.ReportObjectSQLExecutionResult{Rows: r, TotalKnown: true, Total: len(r)}
	}
	out, err := Evaluate(p, results)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range out.Rows {
		for _, column := range p.Columns {
			if _, ok := row.Values[column.Key]; !ok {
				t.Fatalf("missing output column %s", column.Key)
			}
		}
	}
	for _, method := range p.Methods {
		found := false
		for _, column := range p.Columns {
			if column.Key == method.Column {
				found = true
			}
		}
		if !found || method.Numerics == "" {
			t.Fatalf("invalid method: %+v", method)
		}
	}
	return out
}

func wantCell(t *testing.T, row model.AnalysisRow, key string, expected *string) {
	t.Helper()
	if !reflect.DeepEqual(row.Values[key], expected) {
		t.Fatalf("%s: got %v, want %v; row=%+v", key, row.Values[key], expected, row)
	}
}

func hasIssue(row model.AnalysisRow, column, code string) bool {
	for _, issue := range row.Issues {
		if issue.Column == column && issue.Code == code {
			return true
		}
	}
	return false
}

func TestAnalysisAggregatesUseMatchedAndNonNullCountsWithoutInputPageSampling(t *testing.T) {
	p := mustCompile(t, model.AnalysisRequest{GroupBy: []string{"department"}, Measures: []model.AnalysisMeasure{
		{Key: "revenue", Function: "sum", Field: "amount"}, {Key: "mean", Function: "avg", Field: "amount"},
		{Key: "rows", Function: "count"}, {Key: "departments", Function: "count", Field: "department", Distinct: true},
	}})
	out := evaluate(t, p, []map[string]string{
		{"g0": "sales", "gn0": "0", "m0": "9007199254740993.01", "n0": "2", "m1": "9007199254740993.01", "n1": "2", "m2": "12001", "n2": "12001", "m3": "1", "n3": "12001", "source_count": "12001"},
		{"g0": "", "gn0": "0", "m0": "", "n0": "0", "m1": "", "n1": "0", "m2": "2", "n2": "2", "m3": "1", "n3": "2", "source_count": "2"},
		{"g0": "", "gn0": "1", "m0": "-0.000001", "n0": "2", "m1": "-0.000001", "n1": "2", "m2": "2", "n2": "2", "m3": "0", "n3": "0", "source_count": "2"},
	})
	if out.InputCounts["dataset"] != "12005" || len(out.Rows) != 3 {
		t.Fatal(out)
	}
	wantCell(t, out.Rows[0], "revenue", textPointer("9007199254740993.01"))
	wantCell(t, out.Rows[0], "mean", textPointer("4503599627370496.505000"))
	wantCell(t, out.Rows[1], "revenue", nil)
	wantCell(t, out.Rows[1], "department", textPointer(""))
	wantCell(t, out.Rows[2], "department", nil)
	wantCell(t, out.Rows[2], "mean", textPointer("0.000000"))
	if out.Rows[0].NonNullCounts["mean"] != "2" || out.Rows[0].InputCounts["dataset"] != "12001" {
		t.Fatal(out)
	}
	if strings.Contains(p.Queries[0].Report.ObjectSQLV1.SQL, "LIMIT 12001") {
		t.Fatal("input count became sample limit")
	}
}

func TestAnalysisExactHalfEvenRoundingAndNegativeZero(t *testing.T) {
	for _, tc := range []struct {
		in    string
		scale int
		want  string
	}{
		{"2.345", 2, "2.34"}, {"2.355", 2, "2.36"}, {"-2.345", 2, "-2.34"}, {"-2.355", 2, "-2.36"},
		{"-0.0000005", 6, "0.000000"}, {"0.0000015", 6, "0.000002"}, {"9007199254740993.015", 2, "9007199254740993.02"},
		{"99.999", 2, "100.00"}, {"-1.5", 0, "-2"}, {"-2.5", 0, "-2"},
	} {
		n, err := readNumber(tc.in)
		if err != nil {
			t.Fatal(err)
		}
		if got := roundNumber(n, tc.scale); got != tc.want {
			t.Fatalf("%s: %s != %s", tc.in, got, tc.want)
		}
	}
}

func TestAnalysisComparisonOuterGroupsZeroAndNegativeBaseline(t *testing.T) {
	p := mustCompile(t, model.AnalysisRequest{Mode: "compare", Comparison: &model.AnalysisComparison{}, GroupBy: []string{"department"}, Measures: []model.AnalysisMeasure{{Key: "revenue", Function: "sum", Field: "amount"}, {Key: "rows", Function: "count"}}})
	raw := func(group, total, count string) map[string]string {
		return map[string]string{"g0": group, "gn0": "0", "m0": total, "n0": count, "m1": count, "n1": count, "source_count": count}
	}
	out := evaluate(t, p, []map[string]string{raw("a", "-10", "2"), raw("b", "0", "1"), raw("c", "10", "1")}, []map[string]string{raw("a", "-5", "4"), raw("b", "10", "2"), raw("d", "20", "1")})
	if out.InputCounts["baseline"] != "4" || out.InputCounts["current"] != "7" || len(out.Rows) != 4 {
		t.Fatal(out)
	}
	wantCell(t, out.Rows[0], "delta_revenue", textPointer("5.000000"))
	wantCell(t, out.Rows[0], "change_pct_revenue", textPointer("50.000000"))
	wantCell(t, out.Rows[1], "change_pct_revenue", nil)
	if !hasIssue(out.Rows[1], "change_pct_revenue", "zero_baseline") {
		t.Fatal(out.Rows[1])
	}
	wantCell(t, out.Rows[2], "current_revenue", nil)
	wantCell(t, out.Rows[2], "current_rows", textPointer("0"))
	wantCell(t, out.Rows[2], "delta_rows", textPointer("-1.000000"))
	wantCell(t, out.Rows[3], "baseline_revenue", nil)
	if out.Rows[3].InputCounts["baseline"] != "0" {
		t.Fatal(out.Rows[3])
	}
	for _, col := range p.Columns {
		if strings.HasPrefix(col.Key, "delta_") && (col.Type != "decimal" || col.Scale != 6) {
			t.Fatal(col)
		}
	}
	p.Spec.MaxRows = 2
	_, err := Evaluate(p, []model.ReportObjectSQLExecutionResult{{Rows: []map[string]string{raw("a", "1", "1"), raw("b", "1", "1")}}, {Rows: []map[string]string{raw("c", "1", "1"), raw("d", "1", "1")}}})
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != "result_limit_exceeded" {
		t.Fatal(err)
	}
}

func TestAnalysisTrendUsesCalendarTimeAndPreviousObservedGroup(t *testing.T) {
	p := mustCompile(t, model.AnalysisRequest{Mode: "trend", Time: &model.AnalysisTimeBucket{Field: "occurred_at", Grain: "day", TimeZone: "America/New_York"}, GroupBy: []string{"department"}, Measures: []model.AnalysisMeasure{{Key: "revenue", Function: "sum", Field: "amount"}}})
	raw := func(group, period, total string) map[string]string {
		null := "0"
		if period == "" {
			null = "1"
		}
		return map[string]string{"g0": group, "gn0": "0", "g1": period, "gn1": null, "m0": total, "n0": "1", "source_count": "1"}
	}
	out := evaluate(t, p, []map[string]string{raw("b", "2026-03-09T00:00:00-04:00", "500"), raw("a", "2026-03-11T00:00:00-04:00", "40"), raw("a", "", "99"), raw("a", "2026-03-09T00:00:00-04:00", "20"), raw("a", "2026-03-08T00:00:00-05:00", "10")})
	wantCell(t, out.Rows[1], "previous_revenue", textPointer("10"))
	wantCell(t, out.Rows[1], "change_pct_revenue", textPointer("100.000000"))
	if hasIssue(out.Rows[1], "period", "non_adjacent_observed_period") {
		t.Fatal("DST calendar day treated as a missing period")
	}
	if !hasIssue(out.Rows[2], "period", "non_adjacent_observed_period") {
		t.Fatal("missing period silently filled")
	}
	wantCell(t, out.Rows[2], "previous_revenue", textPointer("20"))
	if !hasIssue(out.Rows[3], "period", "missing_time") {
		t.Fatal(out.Rows[3])
	}
	wantCell(t, out.Rows[3], "previous_revenue", nil)
	wantCell(t, out.Rows[4], "previous_revenue", nil)
	if len(out.Rows) != 5 {
		t.Fatal("invented zero-filled period")
	}
}

func TestAnalysisTableCalculationsRulesNullAndApproximateMetadata(t *testing.T) {
	ref := func(s string) model.AnalysisExpression { return model.AnalysisExpression{Reference: s} }
	constant := func(s string) model.AnalysisExpression { return model.AnalysisExpression{Decimal: s} }
	operation := func(op string, a, b model.AnalysisExpression) model.AnalysisExpression {
		return model.AnalysisExpression{Operator: op, Arguments: []model.AnalysisExpression{a, b}}
	}
	p := mustCompile(t, model.AnalysisRequest{Mode: "table", Select: []string{"amount", "quantity", "posted"}, Calculations: []model.AnalysisCalculation{
		{Key: "unit_price", Expression: operation("divide", ref("amount"), ref("quantity")), Scale: 2},
		{Key: "adjusted", Expression: operation("multiply", ref("unit_price"), constant("3")), Scale: 2},
	}, AnomalyRules: []model.AnalysisAnomalyRule{{Key: "high", Column: "unit_price", Operator: "gt", Values: []string{"0.32"}}, {Key: "outside", Column: "adjusted", Operator: "outside", Values: []string{"0.5", "1.1"}}}})
	out := evaluate(t, p, []map[string]string{{"v0": "1", "vn0": "0", "v1": "3", "vn1": "0", "v2": "1", "vn2": "0"}, {"v0": "10", "vn0": "0", "v1": "0", "vn1": "0", "v2": "0", "vn2": "0"}, {"v0": "", "vn0": "1", "v1": "3", "vn1": "0", "v2": "", "vn2": "1"}})
	wantCell(t, out.Rows[0], "unit_price", textPointer("0.33"))
	wantCell(t, out.Rows[0], "adjusted", textPointer("0.99"))
	wantCell(t, out.Rows[0], "posted", textPointer("true"))
	if !reflect.DeepEqual(out.Rows[0].Anomalies, []string{"high"}) {
		t.Fatal(out.Rows[0])
	}
	if !hasIssue(out.Rows[1], "unit_price", "division_by_zero") || !hasIssue(out.Rows[1], "adjusted", "null_input") || !hasIssue(out.Rows[1], "unit_price", "anomaly_rule_high_undefined") {
		t.Fatal(out.Rows[1])
	}
	if !hasIssue(out.Rows[2], "unit_price", "null_input") {
		t.Fatal(out.Rows[2])
	}
	d := testDataset()
	d.Columns[3].Type = "number"
	request := model.AnalysisRequest{DatasetKey: d.Key, Mode: "compare", Comparison: &model.AnalysisComparison{}, Measures: []model.AnalysisMeasure{{Key: "total", Field: d.Columns[3].Key, Function: "sum"}}}
	approx, err := Compile(request, d)
	if err != nil {
		t.Fatal(err)
	}
	for _, col := range approx.Columns {
		if col.Type != "number" {
			t.Fatalf("approximate source claimed exact: %+v", col)
		}
	}
	for _, method := range approx.Methods {
		if !strings.Contains(method.Numerics, "source_approximate") {
			t.Fatal(method)
		}
	}
}

func TestAnalysisRejectsPartialOrMalformedHostResults(t *testing.T) {
	base := func() map[string]string {
		return map[string]string{"g0": "sales", "gn0": "0", "m0": "2", "n0": "2", "source_count": "2"}
	}
	for _, tc := range []string{"sentinel", "more", "cursor", "partial_total", "missing_column", "null_flag", "bad_number", "bad_count", "negative_count", "more_non_null", "zero_group", "duplicate_group", "oversize_cell", "segments"} {
		t.Run(tc, func(t *testing.T) {
			p := mustCompile(t, model.AnalysisRequest{MaxRows: 1, GroupBy: []string{"department"}, Measures: []model.AnalysisMeasure{{Key: "rows", Function: "count"}}})
			r := model.ReportObjectSQLExecutionResult{Rows: []map[string]string{base()}}
			switch tc {
			case "sentinel":
				r.Rows = append(r.Rows, base())
			case "more":
				r.HasMore = true
			case "cursor":
				r.NextCursor = "next"
			case "partial_total":
				r.TotalKnown = true
				r.Total = 100
			case "missing_column":
				delete(r.Rows[0], "m0")
			case "null_flag":
				delete(r.Rows[0], "gn0")
			case "bad_number":
				r.Rows[0]["m0"] = "NaN"
			case "bad_count":
				r.Rows[0]["m0"] = "1"
			case "negative_count":
				r.Rows[0]["m0"] = "-2"
			case "more_non_null":
				r.Rows[0]["n0"] = "3"
			case "zero_group":
				r.Rows[0]["source_count"] = "0"
			case "duplicate_group":
				p.Spec.MaxRows = 2
				r.Rows = append(r.Rows, base())
			case "oversize_cell":
				r.Rows[0]["g0"] = strings.Repeat("x", 16385)
			}
			results := []model.ReportObjectSQLExecutionResult{r}
			if tc == "segments" {
				results = nil
			}
			if _, err := Evaluate(p, results); err == nil {
				t.Fatal("invalid host result accepted")
			}
		})
	}
}

func TestAnalysisEmptyAggregateAndTableHaveDistinctCountSemantics(t *testing.T) {
	p := mustCompile(t, model.AnalysisRequest{Measures: []model.AnalysisMeasure{{Key: "mean", Function: "avg", Field: "amount"}, {Key: "count", Function: "count"}}})
	out := evaluate(t, p, []map[string]string{{"m0": "", "n0": "0", "m1": "0", "n1": "0", "source_count": "0"}})
	if out.InputCounts["dataset"] != "0" || len(out.Rows) != 1 {
		t.Fatal(out)
	}
	wantCell(t, out.Rows[0], "mean", nil)
	wantCell(t, out.Rows[0], "count", textPointer("0"))
	if _, err := Evaluate(p, []model.ReportObjectSQLExecutionResult{{Rows: []map[string]string{}}}); err == nil {
		t.Fatal("empty execution accepted as scalar aggregate")
	}
	table := mustCompile(t, model.AnalysisRequest{Mode: "table", Select: []string{"name"}})
	empty := evaluate(t, table, []map[string]string{})
	if len(empty.Rows) != 0 || empty.InputCounts["dataset"] != "0" {
		t.Fatal(empty)
	}
	duplicates := evaluate(t, table, []map[string]string{{"v0": "same", "vn0": "0"}, {"v0": "same", "vn0": "0"}})
	if len(duplicates.Rows) != 2 {
		t.Fatal("table silently deduplicated source rows")
	}
}
