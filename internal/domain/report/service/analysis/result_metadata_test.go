package analysis

import (
	"reflect"
	"testing"

	model "github.com/domainry/domainry-report-sdk/model"
)

func resultText(value string) *string { return &value }

func TestDescribeResultReturnsDeclarativeChartCoverageAndReferences(t *testing.T) {
	dataset := model.AnalysisDataset{
		Key: "sales", Name: "Sales", Kind: "business_object", Version: "schema-1",
		Columns:    []model.AnalysisColumn{{Key: "region", Type: "text"}, {Key: "amount", Type: "decimal", Unit: "CNY"}},
		References: []model.AnalysisReference{{Kind: "business_object", ID: "sales", Label: "Sales", Version: "schema-1"}},
	}
	plan, err := Compile(model.AnalysisRequest{DatasetKey: "sales", GroupBy: []string{"region"}, Measures: []model.AnalysisMeasure{{Key: "total", Function: "sum", Field: "amount"}}, MaxRows: 10}, dataset)
	if err != nil {
		t.Fatal(err)
	}
	evaluated := Evaluation{Rows: []model.AnalysisRow{
		{Values: map[string]*string{"region": resultText("east"), "total": resultText("12.50")}, Issues: []model.AnalysisCellIssue{}},
		{Values: map[string]*string{"region": resultText("west"), "total": nil}, InputCounts: map[string]string{"dataset": "9007199254740993"}, NonNullCounts: map[string]string{"total": "9007199254740990"}, Issues: []model.AnalysisCellIssue{{Column: "total", Code: "missing_comparison_value"}}},
	}}
	visualization, coverage, references := DescribeResult(plan, evaluated)
	if visualization.Chart == nil || visualization.Chart.Type != "bar" || visualization.Chart.XColumn != "region" || !reflect.DeepEqual(visualization.Chart.YColumns, []string{"total"}) || visualization.OmittedReason != "" {
		t.Fatalf("visualization=%+v", visualization)
	}
	wantMissing := []model.AnalysisMissing{{Column: "total", Code: "missing_comparison_value", Count: "1"}, {Column: "total", Code: "null_value", Count: "1"}, {Column: "total", Code: "source_null_inputs", Count: "3"}}
	if !coverage.Complete || coverage.Truncated || coverage.ReturnedRows != 2 || coverage.RequestedMaxRows != 10 || !reflect.DeepEqual(coverage.Missing, wantMissing) {
		t.Fatalf("coverage=%+v", coverage)
	}
	if !reflect.DeepEqual(references, dataset.References) {
		t.Fatalf("references=%+v", references)
	}
}

func TestDescribeResultExplainsChartOmissionAndFallbackReference(t *testing.T) {
	dataset := model.AnalysisDataset{Key: "sales", Name: "Sales", Kind: "business_object", Version: "schema-1", Columns: []model.AnalysisColumn{{Key: "amount", Type: "decimal", Unit: "CNY"}}}
	plan, err := Compile(model.AnalysisRequest{DatasetKey: "sales", Measures: []model.AnalysisMeasure{{Key: "total", Function: "sum", Field: "amount"}}}, dataset)
	if err != nil {
		t.Fatal(err)
	}
	visualization, coverage, references := DescribeResult(plan, Evaluation{Rows: []model.AnalysisRow{{Values: map[string]*string{"total": resultText("1")}}}})
	if visualization.Chart != nil || visualization.OmittedReason != "single_value_or_multiple_dimensions" || coverage.RequestedMaxRows != 100 || len(coverage.Missing) != 0 {
		t.Fatalf("visualization=%+v coverage=%+v", visualization, coverage)
	}
	if len(references) != 1 || references[0].Kind != "analysis_dataset" || references[0].ID != "sales" || references[0].Version != "schema-1" {
		t.Fatalf("references=%+v", references)
	}
}

func TestCompileRejectsInvalidOrDuplicateReferences(t *testing.T) {
	base := model.AnalysisDataset{Key: "sales", Kind: "business_object", Version: "schema-1", Columns: []model.AnalysisColumn{{Key: "amount", Type: "decimal", Unit: "CNY"}}}
	request := model.AnalysisRequest{DatasetKey: "sales", Measures: []model.AnalysisMeasure{{Key: "total", Function: "sum", Field: "amount"}}}
	for _, references := range [][]model.AnalysisReference{
		{{Kind: "url", ID: "https://example.invalid"}},
		{{Kind: "business_object", ID: ""}},
		{{Kind: "business_object", ID: "sales"}, {Kind: "business_object", ID: "sales"}},
	} {
		dataset := base
		dataset.References = references
		if _, err := Compile(request, dataset); err == nil {
			t.Fatalf("accepted references=%+v", references)
		}
	}
}
