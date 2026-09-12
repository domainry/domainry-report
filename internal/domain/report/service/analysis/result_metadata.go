package analysis

import (
	"math/big"
	"sort"

	model "github.com/domainry/domainry-report-sdk/model"
)

// DescribeResult derives bounded presentation metadata from the compiled plan
// and evaluated rows. It performs no source access and returns only declarative
// column references; consumers cannot inject expressions through a chart.
func DescribeResult(plan Plan, evaluated Evaluation) (model.AnalysisVisualization, model.AnalysisCoverage, []model.AnalysisReference) {
	coverage := model.AnalysisCoverage{
		Complete:         true,
		Truncated:        false,
		ReturnedRows:     len(evaluated.Rows),
		RequestedMaxRows: plan.Spec.MaxRows,
		Missing:          []model.AnalysisMissing{},
	}
	type missingKey struct{ column, code string }
	counts := map[missingKey]*big.Int{}
	add := func(key missingKey, count *big.Int) {
		if count == nil || count.Sign() <= 0 {
			return
		}
		if counts[key] == nil {
			counts[key] = new(big.Int)
		}
		counts[key].Add(counts[key], count)
	}
	one := big.NewInt(1)
	for _, row := range evaluated.Rows {
		for _, column := range plan.Columns {
			if value, found := row.Values[column.Key]; !found || value == nil {
				add(missingKey{column.Key, "null_value"}, one)
			}
		}
		for column, rawNonNull := range row.NonNullCounts {
			segment := "dataset"
			if len(column) > len("baseline_") && column[:len("baseline_")] == "baseline_" {
				segment = "baseline"
			} else if len(column) > len("current_") && column[:len("current_")] == "current_" {
				segment = "current"
			}
			rawInput, found := row.InputCounts[segment]
			input, inputOK := new(big.Int).SetString(rawInput, 10)
			nonNull, nonNullOK := new(big.Int).SetString(rawNonNull, 10)
			if found && inputOK && nonNullOK && input.Cmp(nonNull) > 0 {
				add(missingKey{column, "source_null_inputs"}, new(big.Int).Sub(input, nonNull))
			}
		}
		for _, issue := range row.Issues {
			add(missingKey{issue.Column, issue.Code}, one)
		}
	}
	keys := make([]missingKey, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].column != keys[j].column {
			return keys[i].column < keys[j].column
		}
		return keys[i].code < keys[j].code
	})
	for _, key := range keys {
		coverage.Missing = append(coverage.Missing, model.AnalysisMissing{Column: key.column, Code: key.code, Count: counts[key].String()})
	}

	references := append([]model.AnalysisReference{}, plan.Dataset.References...)
	if len(references) == 0 {
		references = []model.AnalysisReference{{Kind: "analysis_dataset", ID: plan.Dataset.Key, Label: plan.Dataset.Name, Version: plan.Dataset.Version}}
	}
	return chartFor(plan, evaluated), coverage, references
}

func chartFor(plan Plan, evaluated Evaluation) model.AnalysisVisualization {
	omit := func(reason string) model.AnalysisVisualization {
		return model.AnalysisVisualization{Chart: nil, OmittedReason: reason}
	}
	if len(evaluated.Rows) == 0 {
		return omit("no_rows")
	}
	chart := &model.AnalysisChartSpec{}
	switch plan.Spec.Mode {
	case "trend":
		if len(plan.GroupKeys) != 1 { // period is the only supported x dimension
			return omit("multiple_dimensions_require_explicit_chart_design")
		}
		chart.Type, chart.XColumn = "line", "period"
		chart.YColumns = numericKeys(plan, plan.MeasureKeys)
	case "aggregate":
		if len(plan.GroupKeys) != 1 {
			return omit("single_value_or_multiple_dimensions")
		}
		chart.Type, chart.XColumn = "bar", plan.GroupKeys[0]
		chart.YColumns = numericKeys(plan, plan.MeasureKeys)
	case "compare":
		if len(plan.GroupKeys) != 1 {
			return omit("single_value_or_multiple_dimensions")
		}
		chart.Type, chart.XColumn = "bar", plan.GroupKeys[0]
		keys := make([]string, 0, len(plan.MeasureKeys)*2)
		for _, key := range plan.MeasureKeys {
			keys = append(keys, "baseline_"+key, "current_"+key)
		}
		chart.YColumns = numericKeys(plan, keys)
	case "table":
		for _, column := range plan.Columns {
			if !numeric(column.Type) {
				chart.XColumn = column.Key
				break
			}
		}
		keys := make([]string, 0, len(plan.Columns))
		for _, column := range plan.Columns {
			keys = append(keys, column.Key)
		}
		chart.Type, chart.YColumns = "bar", numericKeys(plan, keys)
	default:
		return omit("unsupported_mode")
	}
	if chart.XColumn == "" || len(chart.YColumns) == 0 {
		return omit("no_supported_axes")
	}
	return model.AnalysisVisualization{Chart: chart}
}

func numericKeys(plan Plan, candidates []string) []string {
	result := []string{}
	for _, key := range candidates {
		for _, column := range plan.Columns {
			if column.Key == key && numeric(column.Type) {
				result = append(result, key)
				break
			}
		}
		if len(result) == 8 {
			break
		}
	}
	return result
}
