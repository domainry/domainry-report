package export

import (
	"strings"

	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

// ApplyDeclaredPredicates resolves public selector keys exclusively through
// Report-owned closed predicate declarations. When enforceControl is true, the
// Export Control allowlists are an additional intersection and an empty list
// grants nothing.
func ApplyDeclaredPredicates(report reportmodel.ReportSchema, queryKey string, tags, allowedQueryKeys, allowedTags []string, enforceControl bool) (reportmodel.ReportSchema, string, []string, error) {
	report.Dataset.Filters = append([]reportmodel.ReportDatasetFilter(nil), report.Dataset.Filters...)
	queryKey = strings.TrimSpace(queryKey)
	if len(queryKey) > 128 {
		return report, queryKey, nil, exportScopeError("backend.report.export_scope_invalid")
	}
	normalizedTags := normalizeScopeStrings(tags, 32, 128)
	if len(tags) > 0 && normalizedTags == nil {
		return report, queryKey, nil, exportScopeError("backend.report.export_scope_invalid")
	}
	queries, err := declaredPredicateMap(report.Dataset.QueryPredicates)
	if err != nil {
		return report, queryKey, normalizedTags, err
	}
	tagPredicates, err := declaredPredicateMap(report.Dataset.TagPredicates)
	if err != nil {
		return report, queryKey, normalizedTags, err
	}
	if queryKey != "" {
		predicate, ok := queries[queryKey]
		if !ok || enforceControl && !stringAllowlistContains(allowedQueryKeys, queryKey) {
			return report, queryKey, normalizedTags, exportScopeError("backend.report.query_not_allowed")
		}
		report.Dataset.Filters = append(report.Dataset.Filters, predicate.Filters...)
	}
	for _, tag := range normalizedTags {
		predicate, ok := tagPredicates[tag]
		if !ok || enforceControl && !stringAllowlistContains(allowedTags, tag) {
			return report, queryKey, normalizedTags, exportScopeError("backend.report.tag_not_allowed")
		}
		report.Dataset.Filters = append(report.Dataset.Filters, predicate.Filters...)
	}
	return report, queryKey, normalizedTags, nil
}

func declaredPredicateMap(values []reportmodel.ReportDatasetPredicate) (map[string]reportmodel.ReportDatasetPredicate, error) {
	result := make(map[string]reportmodel.ReportDatasetPredicate, len(values))
	for _, predicate := range values {
		key := strings.TrimSpace(predicate.Key)
		if key == "" || result[key].Key != "" || len(predicate.Filters) == 0 || len(predicate.Filters) > 16 {
			return nil, exportScopeError("backend.report.predicate_invalid")
		}
		for _, filter := range predicate.Filters {
			if !validDeclaredPredicateFilter(filter) {
				return nil, exportScopeError("backend.report.predicate_invalid")
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

func stringAllowlistContains(values []string, expected string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == expected {
			return true
		}
	}
	return false
}
