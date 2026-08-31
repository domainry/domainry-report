package engine

import (
	"fmt"
	"strings"

	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

type PredicateError struct{ Code string }

func (e *PredicateError) Error() string {
	return fmt.Sprintf("Report predicate is invalid: %s", e.Code)
}

func predicateError(code string) error { return &PredicateError{Code: code} }

// ApplyDeclaredPredicates narrows a dataset only through authored predicate
// keys. Request values can never add fields, operators, identifiers or SQL.
func ApplyDeclaredPredicates(report reportmodel.ReportSchema, queryKey string, tags []string) (reportmodel.ReportSchema, error) {
	// ReportSchema is a value at the owner boundary, but its slices still share
	// backing arrays. Never let request scoping mutate the published definition
	// or change later pagination fingerprints.
	report.Dataset.Filters = append([]reportmodel.ReportDatasetFilter(nil), report.Dataset.Filters...)
	queryKey = strings.TrimSpace(queryKey)
	if len(queryKey) > 128 {
		return report, predicateError("backend.report.export_scope_invalid")
	}
	normalizedTags, ok := normalizePredicateStrings(tags, 32, 128)
	if !ok {
		return report, predicateError("backend.report.export_scope_invalid")
	}
	queries, err := declaredPredicateMap(report.Dataset.QueryPredicates)
	if err != nil {
		return report, err
	}
	tagPredicates, err := declaredPredicateMap(report.Dataset.TagPredicates)
	if err != nil {
		return report, err
	}
	if queryKey != "" {
		predicate, exists := queries[queryKey]
		if !exists {
			return report, predicateError("backend.report.query_not_allowed")
		}
		report.Dataset.Filters = append(report.Dataset.Filters, predicate.Filters...)
	}
	for _, tag := range normalizedTags {
		predicate, exists := tagPredicates[tag]
		if !exists {
			return report, predicateError("backend.report.tag_not_allowed")
		}
		report.Dataset.Filters = append(report.Dataset.Filters, predicate.Filters...)
	}
	return report, nil
}

func declaredPredicateMap(values []reportmodel.ReportDatasetPredicate) (map[string]reportmodel.ReportDatasetPredicate, error) {
	result := make(map[string]reportmodel.ReportDatasetPredicate, len(values))
	for _, predicate := range values {
		key := strings.TrimSpace(predicate.Key)
		if key == "" || result[key].Key != "" || len(predicate.Filters) == 0 || len(predicate.Filters) > 16 {
			return nil, predicateError("backend.report.predicate_invalid")
		}
		for _, filter := range predicate.Filters {
			if !validDeclaredPredicateFilter(filter) {
				return nil, predicateError("backend.report.predicate_invalid")
			}
		}
		predicate.Key = key
		result[key] = predicate
	}
	return result, nil
}

func validDeclaredPredicateFilter(filter reportmodel.ReportDatasetFilter) bool {
	if strings.TrimSpace(filter.Field.SourceAlias) == "" || strings.TrimSpace(filter.Field.FieldKey) == "" {
		return false
	}
	switch strings.TrimSpace(filter.Operator) {
	case "is_null", "not_null":
		return filter.Value == nil && len(filter.Values) == 0
	case "eq", "ne", "gt", "gte", "lt", "lte", "contains", "starts_with", "ends_with":
		return filter.Value != nil && len(filter.Values) == 0
	case "between":
		return filter.Value == nil && len(filter.Values) == 2
	case "in", "not_in":
		return filter.Value == nil && len(filter.Values) >= 1 && len(filter.Values) <= 64
	default:
		return false
	}
}

func normalizePredicateStrings(values []string, maximum, maximumLength int) ([]string, bool) {
	if len(values) > maximum {
		return nil, false
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" || len(value) > maximumLength {
			return nil, false
		}
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result, true
}
