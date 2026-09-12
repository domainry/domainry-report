package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	model "github.com/domainry/domainry-report-sdk/model"
)

func tableFixtureDataset() model.AnalysisDataset {
	return model.AnalysisDataset{Key: "expenses_file", Name: "完整费用表", Kind: "table_file", Version: "source-schema-v1", Columns: []model.AnalysisColumn{
		{Key: "dept", Type: "text"}, {Key: "amount", Type: "decimal", Unit: "CNY", Scale: 2}, {Key: "day", Type: "date"}, {Key: "stamp", Type: "datetime"}, {Key: "flag", Type: "boolean"},
	}}
}
func tableTestPlan(t *testing.T, r model.AnalysisRequest) (Plan, *TableAccumulator) {
	t.Helper()
	r.DatasetKey = "expenses_file"
	p, err := Compile(r, tableFixtureDataset())
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range p.Queries {
		if q.Report.ObjectSQLV1 != nil {
			t.Fatal("file table compiled to SQL")
		}
	}
	a, err := NewTableAccumulator(p)
	if err != nil {
		t.Fatal(err)
	}
	return p, a
}
func tableTestAdd(t *testing.T, a *TableAccumulator, row model.AnalysisTableRow) {
	t.Helper()
	projected := model.AnalysisTableRow{}
	for _, key := range a.keys {
		projected[key] = row[key]
	}
	if err := a.Add(t.Context(), projected); err != nil {
		t.Fatal(err)
	}
}
func tableTestValue(t *testing.T, row model.AnalysisRow, key, want string) {
	t.Helper()
	v := row.Values[key]
	if v == nil || *v != want {
		t.Fatalf("%s: %v want %s", key, row.Values, want)
	}
}

func TestStructuredTableAggregatesAllRowsExactlyAndPreservesNullDenominators(t *testing.T) {
	_, a := tableTestPlan(t, model.AnalysisRequest{GroupBy: []string{"dept"}, Measures: []model.AnalysisMeasure{{Key: "total", Field: "amount", Function: "sum"}, {Key: "mean", Field: "amount", Function: "avg"}, {Key: "unique", Field: "amount", Function: "count", Distinct: true}, {Key: "rows", Function: "count"}}})
	cell := "0.01"
	for i := 0; i < 1205; i++ {
		value := &cell
		if i == 0 {
			value = textPointer("9007199254740993.25")
		}
		if i == 1204 {
			value = nil
		}
		tableTestAdd(t, a, model.AnalysisTableRow{"dept": textPointer("研发"), "amount": value})
	}
	cell = "999999" // The source reused its cell buffer; prior values remain owned.
	out, err := a.Evaluate()
	if err != nil || len(out.Rows) != 1 || a.Rows() != 1205 {
		t.Fatal(out, err)
	}
	r := out.Rows[0]
	tableTestValue(t, r, "total", "9007199254741005.28")
	tableTestValue(t, r, "mean", "7481062503937.712027")
	tableTestValue(t, r, "unique", "2")
	tableTestValue(t, r, "rows", "1205")
	if out.InputCounts["dataset"] != "1205" || r.NonNullCounts["mean"] != "1204" {
		t.Fatal(out)
	}
}

func TestStructuredTableComparisonMergesTypedGroupsAndIndependentOverlaps(t *testing.T) {
	_, a := tableTestPlan(t, model.AnalysisRequest{Mode: "compare", GroupBy: []string{"amount"}, Measures: []model.AnalysisMeasure{{Key: "rows", Function: "count"}}, Comparison: &model.AnalysisComparison{Baseline: []model.AnalysisFilter{{Field: "dept", Operator: "in", Values: []any{"base", "both"}}}, Current: []model.AnalysisFilter{{Field: "dept", Operator: "in", Values: []any{"current", "both"}}}}})
	for _, r := range []model.AnalysisTableRow{{"dept": textPointer("base"), "amount": textPointer("1.00")}, {"dept": textPointer("current"), "amount": textPointer("1e0")}, {"dept": textPointer("both"), "amount": textPointer("1")}} {
		tableTestAdd(t, a, r)
	}
	out, err := a.Evaluate()
	if err != nil || len(out.Rows) != 1 {
		t.Fatal(out, err)
	}
	tableTestValue(t, out.Rows[0], "amount", "1")
	tableTestValue(t, out.Rows[0], "baseline_rows", "2")
	tableTestValue(t, out.Rows[0], "current_rows", "2")
	tableTestValue(t, out.Rows[0], "change_pct_rows", "0.000000")
	if out.InputCounts["baseline"] != "2" || out.InputCounts["current"] != "2" {
		t.Fatal(out)
	}
}

func TestStructuredTableFiltersNullsCalculationsRulesAndSourceOrder(t *testing.T) {
	r := model.AnalysisRequest{Mode: "table", Select: []string{"dept", "amount"}, Filters: []model.AnalysisFilter{{All: []model.AnalysisFilter{{Field: "flag", Operator: "eq", Values: []any{true}}, {Any: []model.AnalysisFilter{{Field: "amount", Operator: "ge", Values: []any{json.Number("9007199254740993")}}, {Field: "amount", Operator: "is_null"}}}}}}, Calculations: []model.AnalysisCalculation{{Key: "twice", Scale: 2, Expression: model.AnalysisExpression{Operator: "multiply", Arguments: []model.AnalysisExpression{{Reference: "amount"}, {Decimal: "2"}}}}}, AnomalyRules: []model.AnalysisAnomalyRule{{Key: "large", Column: "twice", Operator: "gt", Values: []string{"18014398509481986"}}}}
	_, a := tableTestPlan(t, r)
	for _, row := range []model.AnalysisTableRow{{"dept": textPointer("z"), "amount": textPointer("9007199254740993.25"), "flag": textPointer("true")}, {"dept": textPointer("ignored"), "amount": textPointer("5"), "flag": textPointer("true")}, {"dept": textPointer("a"), "amount": nil, "flag": textPointer("true")}} {
		tableTestAdd(t, a, row)
	}
	out, err := a.Evaluate()
	if err != nil || len(out.Rows) != 2 {
		t.Fatal(out, err)
	}
	tableTestValue(t, out.Rows[0], "dept", "z")
	tableTestValue(t, out.Rows[0], "twice", "18014398509481986.50")
	if len(out.Rows[0].Anomalies) != 1 || out.Rows[1].Values["twice"] != nil || len(out.Rows[1].Issues) != 2 || out.InputCounts["dataset"] != "2" {
		t.Fatal(out)
	}
	_, neg := tableTestPlan(t, model.AnalysisRequest{Measures: []model.AnalysisMeasure{{Key: "rows", Function: "count"}}, Filters: []model.AnalysisFilter{{Field: "amount", Operator: "not_in", Values: []any{json.Number("5")}}}})
	for _, v := range []*string{nil, textPointer("5.0"), textPointer("6")} {
		tableTestAdd(t, neg, model.AnalysisTableRow{"amount": v})
	}
	got, err := neg.Evaluate()
	if err != nil {
		t.Fatal(err)
	}
	tableTestValue(t, got.Rows[0], "rows", "1")
}

func TestStructuredTableTrendPreservesDSTAndDoesNotInventMissingPeriods(t *testing.T) {
	_, a := tableTestPlan(t, model.AnalysisRequest{Mode: "trend", Time: &model.AnalysisTimeBucket{Field: "stamp", Grain: "hour", TimeZone: "America/New_York"}, Measures: []model.AnalysisMeasure{{Key: "total", Field: "amount", Function: "sum"}}})
	for _, stamp := range []*string{textPointer("2026-11-01T01:30:00-04:00"), textPointer("2026-11-01T01:30:00-05:00"), textPointer("2026-11-01T04:00:00-05:00"), nil} {
		tableTestAdd(t, a, model.AnalysisTableRow{"stamp": stamp, "amount": textPointer("10")})
	}
	out, err := a.Evaluate()
	if err != nil || len(out.Rows) != 4 {
		t.Fatal(out, err)
	}
	tableTestValue(t, out.Rows[0], "period", "2026-11-01T01:00:00-04:00")
	tableTestValue(t, out.Rows[1], "period", "2026-11-01T01:00:00-05:00")
	tableTestValue(t, out.Rows[1], "previous_total", "10")
	found := false
	for _, issue := range out.Rows[2].Issues {
		found = found || issue.Code == "non_adjacent_observed_period"
	}
	if !found || out.Rows[3].Values["previous_total"] != nil {
		t.Fatal(out)
	}
}

func TestStructuredTableRejectsMalformedOrLimitedStreamsWithoutPartialResults(t *testing.T) {
	for _, kind := range []string{"missing", "extra", "bad_number", "bad_boolean", "bad_time", "cell_limit", "utf8", "nul", "output", "cancel", "input_rows", "input_bytes", "distinct_state"} {
		t.Run(kind, func(t *testing.T) {
			r := model.AnalysisRequest{Mode: "table", Select: []string{"amount", "flag", "stamp"}, MaxRows: 1}
			if kind == "input_rows" {
				r = model.AnalysisRequest{Measures: []model.AnalysisMeasure{{Key: "rows", Function: "count"}}}
			}
			if kind == "input_bytes" {
				r = model.AnalysisRequest{Measures: []model.AnalysisMeasure{{Key: "rows", Field: "dept", Function: "count"}}}
			}
			if kind == "distinct_state" {
				r = model.AnalysisRequest{}
				for i := 0; i < 16; i++ {
					r.Measures = append(r.Measures, model.AnalysisMeasure{Key: fmt.Sprintf("d%d", i), Field: "dept", Function: "count", Distinct: true})
				}
			}
			_, a := tableTestPlan(t, r)
			row := model.AnalysisTableRow{"amount": textPointer("1"), "flag": textPointer("true"), "stamp": textPointer("2026-01-01T00:00:00Z")}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var err error
			switch kind {
			case "missing":
				delete(row, "amount")
			case "extra":
				row["private"] = textPointer("secret")
			case "bad_number":
				row["amount"] = textPointer("NaN")
			case "bad_boolean":
				row["flag"] = textPointer("1")
			case "bad_time":
				row["stamp"] = textPointer("2026-01-01T00:00:00")
			case "cell_limit":
				row["amount"] = textPointer(strings.Repeat("1", 16385))
			case "utf8":
				row["amount"] = textPointer(string([]byte{255}))
			case "nul":
				row["amount"] = textPointer("1\x00")
			case "output":
				if e := a.Add(ctx, row); e != nil {
					t.Fatal(e)
				}
			case "cancel":
				cancel()
			case "input_rows":
				row = model.AnalysisTableRow{}
				for i := 0; i < MaximumTableRows; i++ {
					if e := a.Add(ctx, row); e != nil {
						t.Fatal(i, e)
					}
				}
			case "input_bytes", "distinct_state":
				for i := 0; i < MaximumTableRows; i++ {
					text := strings.Repeat("x", 16384)
					if kind == "distinct_state" {
						text = fmt.Sprint(i)
					}
					row = model.AnalysisTableRow{"dept": &text}
					err = a.Add(ctx, row)
					if err != nil {
						break
					}
				}
			}
			if err == nil {
				err = a.Add(ctx, row)
			}
			if err == nil {
				t.Fatal("invalid stream accepted")
			}
			out, e := a.Evaluate()
			if e == nil || len(out.Rows) != 0 {
				t.Fatal("partial result released", out, e)
			}
			if kind == "cancel" && !errors.Is(e, context.Canceled) {
				t.Fatal(e)
			}
		})
	}
}

func TestStructuredTableEmptyInputAndAllNullAreDifferentFromZero(t *testing.T) {
	r := model.AnalysisRequest{Measures: []model.AnalysisMeasure{{Key: "total", Field: "amount", Function: "sum"}, {Key: "rows", Function: "count"}}}
	_, a := tableTestPlan(t, r)
	empty, err := a.Evaluate()
	if err != nil || empty.Rows[0].Values["total"] != nil {
		t.Fatal(empty, err)
	}
	tableTestValue(t, empty.Rows[0], "rows", "0")
	tableTestAdd(t, a, model.AnalysisTableRow{"amount": nil})
	out, err := a.Evaluate()
	if err != nil || out.Rows[0].Values["total"] != nil {
		t.Fatal(out, err)
	}
	tableTestValue(t, out.Rows[0], "rows", "1")
	tableTestAdd(t, a, model.AnalysisTableRow{"amount": textPointer("0.00")})
	out, err = a.Evaluate()
	if err != nil {
		t.Fatal(err)
	}
	tableTestValue(t, out.Rows[0], "total", "0")
}
