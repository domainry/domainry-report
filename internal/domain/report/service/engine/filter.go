package engine

import (
	"strings"

	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

func RowMatchesFilters(row Row, filters []reportmodel.ReportDatasetFilter) bool {
	for _, filter := range filters {
		actual, exists := FieldValue(row, filter.Field)
		if !FilterMatches(actual, exists, filter) {
			return false
		}
	}
	return true
}

func FilterMatches(actual any, exists bool, filter reportmodel.ReportDatasetFilter) bool {
	operator := strings.TrimSpace(filter.Operator)
	if operator == "is_null" {
		return !exists || actual == nil
	}
	if operator == "not_null" {
		return exists && actual != nil
	}
	if !exists || actual == nil {
		return false
	}
	compare := func(expected any) int { return compareValues(actual, expected) }
	switch operator {
	case "eq":
		return compare(filter.Value) == 0
	case "ne":
		return compare(filter.Value) != 0
	case "gt":
		return compare(filter.Value) > 0
	case "gte":
		return compare(filter.Value) >= 0
	case "lt":
		return compare(filter.Value) < 0
	case "lte":
		return compare(filter.Value) <= 0
	case "in", "not_in":
		matched := false
		for _, expected := range filter.Values {
			matched = matched || compare(expected) == 0
		}
		return matched == (operator == "in")
	case "between":
		return len(filter.Values) == 2 && compare(filter.Values[0]) >= 0 && compare(filter.Values[1]) <= 0
	case "contains":
		return strings.Contains(stableValue(actual), stableValue(filter.Value))
	case "starts_with":
		return strings.HasPrefix(stableValue(actual), stableValue(filter.Value))
	case "ends_with":
		return strings.HasSuffix(stableValue(actual), stableValue(filter.Value))
	default:
		return false
	}
}

func StableValue(value any) string { return stableValue(value) }
