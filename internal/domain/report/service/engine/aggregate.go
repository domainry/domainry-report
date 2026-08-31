package engine

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

const reportDatasetPageSize = 500

func Aggregate(rows []Row, dataset reportmodel.ReportDatasetSchema, objects map[string]Object) (AggregateResult, error) {
	groups, err := reportAggregateDatasetRows(rows, dataset, objects)
	if err != nil {
		return AggregateResult{}, &CalculationError{Stage: "aggregate", Err: err}
	}
	resultRows := reportFinalizeGroups(groups, dataset)
	if err := reportApplyDatasetComparisons(resultRows, dataset); err != nil {
		return AggregateResult{}, &CalculationError{Stage: "comparison", Err: err}
	}
	reportSortResultRows(resultRows, dataset.Sort)
	if dataset.Limit > 0 && len(resultRows) > dataset.Limit {
		resultRows = resultRows[:dataset.Limit]
	}
	visible := len(rows)
	if dataset.Privacy != nil {
		visible = 0
		for _, group := range groups {
			if len(group.privacyEntities) >= dataset.Privacy.MinimumGroupSize {
				visible += group.sourceRows
			}
		}
	}
	return AggregateResult{Rows: resultRows, VisibleSourceRows: visible}, nil
}

type reportMeasureState struct {
	count       int
	sum         decimal.Decimal
	values      []decimal.Decimal
	distinct    map[string]struct{}
	minimum     string
	maximum     string
	hasExtremum bool
	scale       int32
}

type reportGroupState struct {
	dimensions      map[string]string
	measures        map[string]*reportMeasureState
	sourceRows      int
	privacyEntities map[string]struct{}
}

func reportAggregateDatasetRows(rows []Row, dataset reportmodel.ReportDatasetSchema, objects map[string]Object) (map[string]*reportGroupState, error) {
	groups := map[string]*reportGroupState{}
	location := time.UTC
	if strings.TrimSpace(dataset.TimeZone) != "" {
		loaded, err := time.LoadLocation(strings.TrimSpace(dataset.TimeZone))
		if err != nil {
			return nil, fmt.Errorf("invalid report time zone %q", dataset.TimeZone)
		}
		location = loaded
	}
	for _, row := range rows {
		dimensions := map[string]string{}
		parts := make([]string, 0, len(dataset.Dimensions))
		for _, dimension := range dataset.Dimensions {
			value, _ := FieldValue(row, dimension.Field)
			text := dimensionValue(value, dimension.TimeGrain, dataset.DefaultTimeGrain, location)
			dimensions[dimension.Key] = text
			parts = append(parts, strconv.Itoa(len(text))+":"+text)
		}
		groupKey := strings.Join(parts, "|")
		group := groups[groupKey]
		if group == nil {
			group = &reportGroupState{dimensions: dimensions, measures: map[string]*reportMeasureState{}, privacyEntities: map[string]struct{}{}}
			groups[groupKey] = group
		}
		group.sourceRows++
		if dataset.Privacy != nil {
			if entity, ok := FieldValue(row, dataset.Privacy.EntityField); ok && stableValue(entity) != "" {
				group.privacyEntities[stableValue(entity)] = struct{}{}
			}
		}
		for _, measure := range dataset.Measures {
			if measure.Operation == "ratio" {
				continue
			}
			state := group.measures[measure.Key]
			if state == nil {
				state = &reportMeasureState{distinct: map[string]struct{}{}, scale: -1}
				group.measures[measure.Key] = state
			}
			if err := reportAccumulateMeasure(state, measure, row, objects); err != nil {
				return nil, fmt.Errorf("measure %s: %w", measure.Key, err)
			}
		}
	}
	if len(rows) == 0 && len(dataset.Dimensions) == 0 {
		groups[""] = &reportGroupState{dimensions: map[string]string{}, measures: map[string]*reportMeasureState{}, privacyEntities: map[string]struct{}{}}
	}
	return groups, nil
}

func reportAccumulateMeasure(state *reportMeasureState, measure reportmodel.ReportDatasetMeasure, row Row, objects map[string]Object) error {
	operation := strings.TrimSpace(measure.Operation)
	if operation == "count" {
		if row[strings.TrimSpace(measure.SourceAlias)] != nil {
			state.count++
		}
		return nil
	}
	if operation == "duration" {
		startValue, startOK := FieldValue(row, *measure.StartField)
		endValue, endOK := FieldValue(row, *measure.EndField)
		if !startOK || !endOK {
			return nil
		}
		start, err := parseTime(startValue)
		if err != nil {
			return err
		}
		end, err := parseTime(endValue)
		if err != nil {
			return err
		}
		state.sum = state.sum.Add(decimal.NewFromInt(int64(end.Sub(start) / time.Second)))
		state.count++
		return nil
	}
	if measure.Field == nil {
		return fmt.Errorf("field is required")
	}
	value, ok := FieldValue(row, *measure.Field)
	if !ok || value == nil || strings.TrimSpace(fmt.Sprint(value)) == "" {
		return nil
	}
	if operation == "distinct_count" {
		state.distinct[stableValue(value)] = struct{}{}
		return nil
	}
	if operation == "min" || operation == "max" {
		text := stableValue(value)
		if !state.hasExtremum || operation == "min" && compareValues(text, state.minimum) < 0 || operation == "max" && compareValues(text, state.maximum) > 0 {
			if operation == "min" {
				state.minimum = text
			} else {
				state.maximum = text
			}
			state.hasExtremum = true
		}
		return nil
	}
	field := reportDatasetFieldSchema(objects[measure.Field.SourceAlias], measure.Field.FieldKey)
	amount, scale, err := reportNumericValue(field, value)
	if err != nil {
		return err
	}
	state.scale = scale
	state.sum = state.sum.Add(amount)
	state.count++
	if operation == "percentile" {
		state.values = append(state.values, amount)
	}
	return nil
}

func reportFinalizeGroups(groups map[string]*reportGroupState, dataset reportmodel.ReportDatasetSchema) []reportmodel.ReportResultRow {
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rows := make([]reportmodel.ReportResultRow, 0, len(keys))
	for _, key := range keys {
		group := groups[key]
		if dataset.Privacy != nil && len(group.privacyEntities) < dataset.Privacy.MinimumGroupSize {
			continue
		}
		values := map[string]string{}
		for _, measure := range dataset.Measures {
			if measure.Operation == "ratio" {
				continue
			}
			values[measure.Key] = reportFinalizeMeasure(group.measures[measure.Key], measure)
		}
		for _, measure := range dataset.Measures {
			if measure.Operation != "ratio" {
				continue
			}
			numerator, numeratorErr := decimal.NewFromString(values[measure.NumeratorKey])
			denominator, denominatorErr := decimal.NewFromString(values[measure.DenominatorKey])
			if numeratorErr == nil && denominatorErr == nil && !denominator.IsZero() {
				values[measure.Key] = numerator.Div(denominator).RoundBank(6).StringFixed(6)
			} else {
				values[measure.Key] = ""
			}
		}
		rows = append(rows, reportmodel.ReportResultRow{Dimensions: group.dimensions, Measures: values})
	}
	return rows
}

func reportFinalizeMeasure(state *reportMeasureState, measure reportmodel.ReportDatasetMeasure) string {
	if state == nil {
		if measure.Operation == "count" || measure.Operation == "distinct_count" || measure.Operation == "sum" || measure.Operation == "duration" {
			return "0"
		}
		return ""
	}
	switch measure.Operation {
	case "count":
		return strconv.Itoa(state.count)
	case "distinct_count":
		return strconv.Itoa(len(state.distinct))
	case "sum":
		return reportFormatDecimal(state.sum, state.scale)
	case "avg":
		if state.count == 0 {
			return ""
		}
		return state.sum.Div(decimal.NewFromInt(int64(state.count))).RoundBank(6).StringFixed(6)
	case "min":
		return state.minimum
	case "max":
		return state.maximum
	case "duration":
		return state.sum.String()
	case "percentile":
		if len(state.values) == 0 {
			return ""
		}
		sort.Slice(state.values, func(i, j int) bool { return state.values[i].LessThan(state.values[j]) })
		percentile, _ := strconv.ParseFloat(strings.TrimSpace(measure.Percentile), 64)
		index := int(math.Ceil(percentile/100*float64(len(state.values)))) - 1
		if index < 0 {
			index = 0
		}
		return reportFormatDecimal(state.values[index], state.scale)
	default:
		return ""
	}
}

func reportSortResultRows(rows []reportmodel.ReportResultRow, rules []reportmodel.ReportDatasetSort) {
	if len(rules) == 0 {
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		for _, rule := range rules {
			left := rows[i].Dimensions[rule.Key]
			if value, ok := rows[i].Measures[rule.Key]; ok {
				left = value
			}
			right := rows[j].Dimensions[rule.Key]
			if value, ok := rows[j].Measures[rule.Key]; ok {
				right = value
			}
			comparison := compareValues(left, right)
			if comparison == 0 {
				continue
			}
			if rule.Direction == "desc" {
				return comparison > 0
			}
			return comparison < 0
		}
		return false
	})
}

func reportApplyDatasetComparisons(rows []reportmodel.ReportResultRow, dataset reportmodel.ReportDatasetSchema) error {
	if len(dataset.Comparisons) == 0 {
		return nil
	}
	dimensions := map[string]reportmodel.ReportDatasetDimension{}
	for _, dimension := range dataset.Dimensions {
		dimensions[strings.TrimSpace(dimension.Key)] = dimension
	}
	for _, comparison := range dataset.Comparisons {
		dimension := dimensions[strings.TrimSpace(comparison.TimeDimensionKey)]
		grain := strings.TrimSpace(dimension.TimeGrain)
		if grain == "" {
			grain = strings.TrimSpace(dataset.DefaultTimeGrain)
		}
		offset := comparison.OffsetPeriods
		if offset == 0 {
			offset = 1
		}
		rowIndex := make(map[string]int, len(rows))
		for index, row := range rows {
			rowIndex[reportComparisonRowKey(row, dataset.Dimensions)] = index
		}
		for index := range rows {
			currentBucket := rows[index].Dimensions[comparison.TimeDimensionKey]
			previousBucket, err := reportShiftTimeBucket(currentBucket, grain, -offset)
			if err != nil {
				return fmt.Errorf("comparison %s: %w", comparison.Key, err)
			}
			previousDimensions := make(map[string]string, len(rows[index].Dimensions))
			for key, value := range rows[index].Dimensions {
				previousDimensions[key] = value
			}
			previousDimensions[comparison.TimeDimensionKey] = previousBucket
			previousIndex, ok := rowIndex[reportComparisonDimensionsKey(previousDimensions, dataset.Dimensions)]
			if !ok {
				rows[index].Measures[comparison.Key] = ""
				continue
			}
			current, currentErr := decimal.NewFromString(rows[index].Measures[comparison.MeasureKey])
			previous, previousErr := decimal.NewFromString(rows[previousIndex].Measures[comparison.MeasureKey])
			if currentErr != nil || previousErr != nil {
				rows[index].Measures[comparison.Key] = ""
				continue
			}
			switch comparison.Operation {
			case "difference":
				scale := reportMaximumDecimalScale(rows[index].Measures[comparison.MeasureKey], rows[previousIndex].Measures[comparison.MeasureKey])
				rows[index].Measures[comparison.Key] = current.Sub(previous).StringFixed(scale)
			case "ratio":
				if previous.IsZero() {
					rows[index].Measures[comparison.Key] = ""
				} else {
					rows[index].Measures[comparison.Key] = current.Div(previous).RoundBank(6).StringFixed(6)
				}
			case "percent_change":
				if previous.IsZero() {
					rows[index].Measures[comparison.Key] = ""
				} else {
					rows[index].Measures[comparison.Key] = current.Sub(previous).Div(previous).Mul(decimal.NewFromInt(100)).RoundBank(6).StringFixed(6)
				}
			default:
				return fmt.Errorf("unsupported operation %q", comparison.Operation)
			}
		}
	}
	return nil
}

func reportComparisonRowKey(row reportmodel.ReportResultRow, dimensions []reportmodel.ReportDatasetDimension) string {
	return reportComparisonDimensionsKey(row.Dimensions, dimensions)
}

func reportComparisonDimensionsKey(values map[string]string, dimensions []reportmodel.ReportDatasetDimension) string {
	parts := make([]string, 0, len(dimensions))
	for _, dimension := range dimensions {
		value := values[dimension.Key]
		parts = append(parts, strconv.Itoa(len(value))+":"+value)
	}
	return strings.Join(parts, "|")
}

func reportShiftTimeBucket(value, grain string, periods int) (string, error) {
	switch grain {
	case "minute":
		parsed, err := time.Parse("2006-01-02T15:04", value)
		if err != nil {
			return "", err
		}
		return parsed.Add(time.Duration(periods) * time.Minute).Format("2006-01-02T15:04"), nil
	case "hour":
		parsed, err := time.Parse("2006-01-02T15:00", value)
		if err != nil {
			return "", err
		}
		return parsed.Add(time.Duration(periods) * time.Hour).Format("2006-01-02T15:00"), nil
	case "day", "week":
		parsed, err := time.Parse("2006-01-02", value)
		if err != nil {
			return "", err
		}
		days := periods
		if grain == "week" {
			days *= 7
		}
		return parsed.AddDate(0, 0, days).Format("2006-01-02"), nil
	case "month":
		parsed, err := time.Parse("2006-01", value)
		if err != nil {
			return "", err
		}
		return parsed.AddDate(0, periods, 0).Format("2006-01"), nil
	case "quarter":
		var year, quarter int
		if _, err := fmt.Sscanf(value, "%d-Q%d", &year, &quarter); err != nil || quarter < 1 || quarter > 4 {
			return "", fmt.Errorf("invalid quarter %q", value)
		}
		shifted := time.Date(year, time.Month((quarter-1)*3+1), 1, 0, 0, 0, 0, time.UTC).AddDate(0, periods*3, 0)
		return fmt.Sprintf("%04d-Q%d", shifted.Year(), (int(shifted.Month())-1)/3+1), nil
	case "year":
		year, err := strconv.Atoi(value)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%04d", year+periods), nil
	default:
		return "", fmt.Errorf("unsupported time grain %q", grain)
	}
}

func reportMaximumDecimalScale(values ...string) int32 {
	maximum := 0
	for _, value := range values {
		if dot := strings.IndexByte(value, '.'); dot >= 0 && len(value)-dot-1 > maximum {
			maximum = len(value) - dot - 1
		}
	}
	return int32(maximum)
}

func reportDatasetFieldSchema(object Object, fieldKey string) Field {
	for _, field := range object.Fields {
		if strings.TrimSpace(field.Key) == strings.TrimSpace(fieldKey) {
			return field
		}
	}
	return Field{Key: fieldKey, Type: "text"}
}

func reportNumericValue(field Field, value any) (decimal.Decimal, int32, error) {
	if field.Type == "currency" || field.Type == "decimal" {
		amount, err := decimal.NewFromString(strings.TrimSpace(fmt.Sprint(value)))
		if err != nil {
			return decimal.Zero, -1, err
		}
		return amount.RoundBank(field.Scale), field.Scale, nil
	}
	amount, err := decimal.NewFromString(strings.TrimSpace(fmt.Sprint(value)))
	return amount, -1, err
}

func reportFormatDecimal(value decimal.Decimal, scale int32) string {
	if scale >= 0 {
		return value.StringFixed(scale)
	}
	return value.String()
}
