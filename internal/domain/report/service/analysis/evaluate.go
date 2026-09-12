package analysis

import (
	"encoding/json"
	"math/big"
	"regexp"
	"sort"

	model "github.com/domainry/domainry-report-sdk/model"
)

type Evaluation struct {
	Rows        []model.AnalysisRow
	InputCounts map[string]string
}

var nonnegativeInteger = regexp.MustCompile(`^(0|[1-9][0-9]{0,63})$`)

func resultInvalid(path string) error { return &Error{Code: "result_invalid", Path: path} }
func freshRow() model.AnalysisRow {
	return model.AnalysisRow{Values: map[string]*string{}, InputCounts: map[string]string{}, NonNullCounts: map[string]string{}, Anomalies: []string{}, Issues: []model.AnalysisCellIssue{}}
}

// Evaluate consumes complete host aggregate results, not raw input pages. An
// output overflow or a continuation marker aborts the entire analysis. It is
// never legal to derive comparisons from a partial grouped result.
func Evaluate(plan Plan, results []model.ReportObjectSQLExecutionResult) (Evaluation, error) {
	if len(results) != len(plan.Queries) || len(results) == 0 {
		return Evaluation{}, resultInvalid("segments")
	}
	out := Evaluation{Rows: []model.AnalysisRow{}, InputCounts: map[string]string{}}
	bytesRead := 0
	segments := map[string][]model.AnalysisRow{}
	for index, result := range results {
		if result.HasMore || result.NextCursor != "" || len(result.Rows) > plan.Spec.MaxRows || result.TotalKnown && result.Total != len(result.Rows) {
			return Evaluation{}, &Error{Code: "result_limit_exceeded", Path: "rows"}
		}
		query := plan.Queries[index]
		if query.CountAlias != "" && len(plan.GroupKeys) == 0 && len(result.Rows) != 1 {
			return Evaluation{}, resultInvalid("aggregate_count")
		}
		rows := []model.AnalysisRow{}
		count := new(big.Int)
		seen := map[string]bool{}
		for _, raw := range result.Rows {
			row := freshRow()
			inputCount := "1"
			if query.CountAlias != "" {
				inputCount = raw[query.CountAlias]
			}
			if !nonnegativeInteger.MatchString(inputCount) || len(plan.GroupKeys) > 0 && inputCount == "0" {
				return Evaluation{}, resultInvalid("input_count")
			}
			n, _ := new(big.Int).SetString(inputCount, 10)
			count.Add(count, n)
			row.InputCounts[query.Segment] = inputCount
			for _, binding := range query.Values {
				value, ok := raw[binding.Alias]
				bytesRead += len(value)
				if bytesRead > 2<<20 {
					return Evaluation{}, &Error{Code: "result_limit_exceeded", Path: "bytes"}
				}
				if len(value) > 16384 {
					return Evaluation{}, resultInvalid("column")
				}
				isNull := false
				if binding.NullAlias != "" {
					flag := raw[binding.NullAlias]
					if flag != "0" && flag != "1" {
						return Evaluation{}, resultInvalid("null_flag")
					}
					isNull = flag == "1"
				}
				if binding.CountAlias != "" {
					nonNull := raw[binding.CountAlias]
					if !nonnegativeInteger.MatchString(nonNull) {
						return Evaluation{}, resultInvalid("non_null_count")
					}
					rowsCount, _ := new(big.Int).SetString(inputCount, 10)
					validCount, _ := new(big.Int).SetString(nonNull, 10)
					if validCount.Cmp(rowsCount) > 0 {
						return Evaluation{}, resultInvalid("non_null_count")
					}
					if binding.Function == "count" {
						if !nonnegativeInteger.MatchString(value) {
							return Evaluation{}, resultInvalid("count")
						}
						actual, _ := new(big.Int).SetString(value, 10)
						if !binding.Distinct && actual.Cmp(validCount) != 0 || binding.Distinct && (actual.Cmp(validCount) > 0 || (actual.Sign() == 0) != (validCount.Sign() == 0)) {
							return Evaluation{}, resultInvalid("count")
						}
					}
					row.NonNullCounts[binding.Key] = nonNull
					if binding.Function != "count" && nonNull == "0" {
						isNull = true
					}
					if binding.Function == "avg" && !isNull {
						sum, err := readNumber(value)
						if err != nil {
							return Evaluation{}, err
						}
						sum.Quo(sum, new(big.Rat).SetInt(validCount))
						value = roundNumber(sum, 6)
					}
				}
				row.Values[binding.Key] = nil
				if !isNull {
					if !ok {
						return Evaluation{}, resultInvalid("column")
					}
					kind := bindingType(plan, binding)
					if kind == "boolean" {
						switch value {
						case "0", "false":
							value = "false"
						case "1", "true":
							value = "true"
						default:
							return Evaluation{}, resultInvalid("boolean")
						}
					}
					if numeric(kind) {
						number, err := readNumber(value)
						if err != nil || kind == "integer" && !number.IsInt() {
							return Evaluation{}, resultInvalid("number")
						}
					}
					row.Values[binding.Key] = textPointer(value)
				}
			}
			if plan.Spec.Mode != "table" {
				key := groupKey(row, plan.GroupKeys)
				if seen[key] {
					return Evaluation{}, resultInvalid("duplicate_group")
				}
				seen[key] = true
			}
			rows = append(rows, row)
		}
		out.InputCounts[query.Segment] = count.String()
		segments[query.Segment] = rows
	}
	if plan.Spec.Mode == "compare" {
		rows, err := compareRows(plan, segments)
		if err != nil {
			return Evaluation{}, err
		}
		out.Rows = rows
	} else {
		out.Rows = segments["dataset"]
	}
	if plan.Spec.Mode == "trend" {
		if err := trendRows(plan, out.Rows); err != nil {
			return Evaluation{}, err
		}
	}
	for index := range out.Rows {
		if err := applyCalculationsAndRules(plan, &out.Rows[index]); err != nil {
			return Evaluation{}, err
		}
	}
	return out, nil
}

func bindingType(plan Plan, binding ValueBinding) string {
	key := binding.Key
	if plan.Spec.Mode == "compare" && binding.Function != "" {
		key = "current_" + key
	}
	for _, column := range plan.Columns {
		if column.Key == key {
			return column.Type
		}
	}
	return ""
}

func groupKey(row model.AnalysisRow, keys []string) string {
	values := make([]*string, 0, len(keys))
	for _, key := range keys {
		values = append(values, row.Values[key])
	}
	raw, _ := json.Marshal(values)
	return string(raw)
}

func compareRows(plan Plan, segments map[string][]model.AnalysisRow) ([]model.AnalysisRow, error) {
	rows := map[string]model.AnalysisRow{}
	for _, segment := range []string{"baseline", "current"} {
		for _, source := range segments[segment] {
			key := groupKey(source, plan.GroupKeys)
			row, ok := rows[key]
			if !ok {
				row = freshRow()
				for _, group := range plan.GroupKeys {
					row.Values[group] = source.Values[group]
				}
			}
			row.InputCounts[segment] = source.InputCounts[segment]
			for _, measure := range plan.MeasureKeys {
				row.Values[segment+"_"+measure] = source.Values[measure]
				row.NonNullCounts[segment+"_"+measure] = source.NonNullCounts[measure]
			}
			rows[key] = row
		}
	}
	if len(rows) > plan.Spec.MaxRows {
		return nil, &Error{Code: "result_limit_exceeded", Path: "comparison_groups"}
	}
	keys := []string{}
	for key := range rows {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]model.AnalysisRow, 0, len(keys))
	for _, key := range keys {
		row := rows[key]
		for _, segment := range []string{"baseline", "current"} {
			if _, found := row.InputCounts[segment]; !found {
				row.InputCounts[segment] = "0"
				for _, measure := range plan.Spec.Measures {
					row.Values[segment+"_"+measure.Key] = nil
					if measure.Function == "count" {
						row.Values[segment+"_"+measure.Key] = textPointer("0")
					}
					row.NonNullCounts[segment+"_"+measure.Key] = "0"
				}
			}
		}
		for _, measure := range plan.MeasureKeys {
			if err := changes(&row, measure, row.Values["baseline_"+measure], row.Values["current_"+measure]); err != nil {
				return nil, err
			}
		}
		result = append(result, row)
	}
	return result, nil
}

func changes(row *model.AnalysisRow, key string, baseline, current *string) error {
	delta, pct := "delta_"+key, "change_pct_"+key
	row.Values[delta], row.Values[pct] = nil, nil
	if baseline == nil || current == nil {
		row.Issues = append(row.Issues, model.AnalysisCellIssue{Column: delta, Code: "missing_comparison_value"}, model.AnalysisCellIssue{Column: pct, Code: "missing_comparison_value"})
		return nil
	}
	a, err := readNumber(*baseline)
	if err != nil {
		return err
	}
	b, err := readNumber(*current)
	if err != nil {
		return err
	}
	difference := new(big.Rat).Sub(b, a)
	row.Values[delta] = textPointer(roundNumber(difference, 6))
	if a.Sign() == 0 {
		row.Issues = append(row.Issues, model.AnalysisCellIssue{Column: pct, Code: "zero_baseline"})
		return nil
	}
	percent := new(big.Rat).Quo(difference, new(big.Rat).Abs(a))
	percent.Mul(percent, big.NewRat(100, 1))
	row.Values[pct] = textPointer(roundNumber(percent, 6))
	return nil
}
