package export

import (
	"sort"
	"strings"

	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

func reportExportControlOwnsSource(control reportmodel.ReportExportControlSchema, objectKey string) bool {
	objectKey = strings.TrimSpace(objectKey)
	for _, candidate := range control.SourceObjects {
		if strings.TrimSpace(candidate) == objectKey && objectKey != "" {
			return true
		}
	}
	return false
}

func applyReportOwnedExportScope(scope reportmodel.ReportExportScopeRequest, report reportmodel.ReportSchema) (reportmodel.ReportExportScopeRequest, reportmodel.ReportSchema, error) {
	scope.QueryKey = strings.ToLower(strings.Join(strings.Fields(scope.QueryKey), " "))
	if len(scope.QueryKey) > 128 {
		return scope, report, exportScopeError("backend.report.export_scope_invalid")
	}
	if scope.QueryKey != "" {
		definition := report.ExportScope
		if definition == nil || definition.Query == nil {
			return scope, report, exportScopeError("backend.report.export_query_not_allowed")
		}
		mode := strings.TrimSpace(definition.Query.Mode)
		if mode == "" {
			mode = "any"
		}
		if mode != "any" && mode != "all" || len(definition.Query.Predicates) == 0 || len(definition.Query.Predicates) > 16 {
			return scope, report, exportScopeError("backend.report.export_query_definition_invalid")
		}
		predicates := make([]reportmodel.ReportExportQueryPredicate, 0, len(definition.Query.Predicates))
		for _, predicate := range definition.Query.Predicates {
			predicate.Field.SourceAlias = strings.TrimSpace(predicate.Field.SourceAlias)
			predicate.Field.FieldKey = strings.TrimSpace(predicate.Field.FieldKey)
			predicate.Operator = strings.TrimSpace(predicate.Operator)
			if predicate.Field.SourceAlias == "" || predicate.Field.FieldKey == "" || !reportExportQueryOperator(predicate.Operator) {
				return scope, report, exportScopeError("backend.report.export_query_definition_invalid")
			}
			predicates = append(predicates, predicate)
		}
		report.Dataset.RuntimeQuery = &reportmodel.ReportDatasetRuntimeQuery{Mode: mode, Value: scope.QueryKey, Predicates: predicates}
	}
	if len(scope.Tags) == 0 {
		if strings.TrimSpace(scope.TagMatch) != "" {
			return scope, report, exportScopeError("backend.report.export_scope_invalid")
		}
		scope.Tags = nil
		scope.TagMatch = ""
		return scope, report, nil
	}
	if len(scope.Tags) > 32 {
		return scope, report, exportScopeError("backend.report.export_scope_invalid")
	}
	seenTags := map[string]bool{}
	normalizedTags := make([]string, 0, len(scope.Tags))
	for _, raw := range scope.Tags {
		tag := strings.TrimSpace(raw)
		if tag == "" || len(tag) > 128 {
			return scope, report, exportScopeError("backend.report.export_scope_invalid")
		}
		if !seenTags[tag] {
			seenTags[tag] = true
			normalizedTags = append(normalizedTags, tag)
		}
	}
	sort.Strings(normalizedTags)
	scope.Tags = normalizedTags
	definition := report.ExportScope
	if definition == nil || definition.Tags == nil {
		return scope, report, exportScopeError("backend.report.export_tags_not_allowed")
	}
	tags := *definition.Tags
	tags.Join.Alias = strings.TrimSpace(tags.Join.Alias)
	tags.Join.ObjectKey = strings.TrimSpace(tags.Join.ObjectKey)
	tags.Join.Type = strings.TrimSpace(tags.Join.Type)
	tags.Join.LeftAlias = strings.TrimSpace(tags.Join.LeftAlias)
	tags.Join.LeftField = strings.TrimSpace(tags.Join.LeftField)
	tags.Join.RightField = strings.TrimSpace(tags.Join.RightField)
	tags.Join.Cardinality = strings.TrimSpace(tags.Join.Cardinality)
	tags.TargetField.SourceAlias = strings.TrimSpace(tags.TargetField.SourceAlias)
	tags.TargetField.FieldKey = strings.TrimSpace(tags.TargetField.FieldKey)
	tags.TagField.SourceAlias = strings.TrimSpace(tags.TagField.SourceAlias)
	tags.TagField.FieldKey = strings.TrimSpace(tags.TagField.FieldKey)
	tags.Match = strings.ToLower(strings.TrimSpace(tags.Match))
	tags.DefaultMatchMode = strings.ToLower(strings.TrimSpace(tags.DefaultMatchMode))
	match, ok := reportExportTagMatch(tags, scope.TagMatch)
	if !ok {
		return scope, report, exportScopeError("backend.report.export_tag_match_not_allowed")
	}
	scope.TagMatch = match
	aliases := reportmodel.ReportDatasetAliasObjects(report.Dataset)
	if tags.Join.Alias == "" || aliases[tags.Join.Alias] != "" || tags.Join.ObjectKey == "" || tags.Join.Type != "inner" || tags.Join.Cardinality != "one_to_many" ||
		aliases[tags.Join.LeftAlias] == "" || tags.TargetField.SourceAlias != tags.Join.LeftAlias || tags.TargetField.FieldKey != tags.Join.LeftField ||
		tags.TagField.FieldKey == "" || tags.Join.RightField == "" {
		return scope, report, exportScopeError("backend.report.export_tag_definition_invalid")
	}
	scopeAliases := []string{tags.Join.Alias}
	allowedFilterAliases := map[string]bool{tags.Join.Alias: true}
	if tags.FamilyJoin == nil {
		if tags.TagField.SourceAlias != tags.Join.Alias {
			return scope, report, exportScopeError("backend.report.export_tag_definition_invalid")
		}
	} else {
		family := *tags.FamilyJoin
		family.Alias = strings.TrimSpace(family.Alias)
		family.ObjectKey = strings.TrimSpace(family.ObjectKey)
		family.Type = strings.TrimSpace(family.Type)
		family.LeftAlias = strings.TrimSpace(family.LeftAlias)
		family.LeftField = strings.TrimSpace(family.LeftField)
		family.RightField = strings.TrimSpace(family.RightField)
		family.Cardinality = strings.TrimSpace(family.Cardinality)
		if family.Alias == "" || aliases[family.Alias] != "" || family.Alias == tags.Join.Alias || family.ObjectKey == "" || family.Type != "inner" || family.Cardinality != "many_to_one" ||
			family.LeftAlias != tags.Join.Alias || family.LeftField == "" || family.RightField == "" || tags.TagField.SourceAlias != family.Alias {
			return scope, report, exportScopeError("backend.report.export_tag_definition_invalid")
		}
		tags.FamilyJoin = &family
		scopeAliases = append(scopeAliases, family.Alias)
		allowedFilterAliases[family.Alias] = true
	}
	for index := range tags.FixedFilters {
		filter := &tags.FixedFilters[index]
		filter.Field.SourceAlias = strings.TrimSpace(filter.Field.SourceAlias)
		filter.Field.FieldKey = strings.TrimSpace(filter.Field.FieldKey)
		filter.Operator = strings.TrimSpace(filter.Operator)
		if !allowedFilterAliases[filter.Field.SourceAlias] || filter.Field.FieldKey == "" || !reportExportFixedFilter(filter) {
			return scope, report, exportScopeError("backend.report.export_tag_definition_invalid")
		}
	}
	tags.Join.RuntimeScopeOnly = true
	report.Dataset.Joins = append(report.Dataset.Joins, tags.Join)
	if tags.FamilyJoin != nil {
		tags.FamilyJoin.RuntimeScopeOnly = true
		report.Dataset.Joins = append(report.Dataset.Joins, *tags.FamilyJoin)
	}
	report.Dataset.Filters = append(report.Dataset.Filters, tags.FixedFilters...)
	report.Dataset.RuntimeTags = &reportmodel.ReportDatasetRuntimeTags{ScopeJoinAliases: scopeAliases, TargetField: tags.TargetField, TagField: tags.TagField, Values: append([]string(nil), scope.Tags...), Match: match}
	return scope, report, nil
}

func reportExportTagMatch(tags reportmodel.ReportExportTagScopeSchema, requested string) (string, bool) {
	requested = strings.ToLower(strings.TrimSpace(requested))
	allowed := map[string]bool{}
	for _, raw := range tags.AllowedMatchModes {
		mode := strings.ToLower(strings.TrimSpace(raw))
		if !reportExportTagMatchMode(mode) || allowed[mode] {
			return "", false
		}
		allowed[mode] = true
	}
	legacy := strings.ToLower(strings.TrimSpace(tags.Match))
	defaultMode := strings.ToLower(strings.TrimSpace(tags.DefaultMatchMode))
	if legacy != "" {
		if len(allowed) != 0 || defaultMode != "" || !reportExportTagMatchMode(legacy) {
			return "", false
		}
		allowed[legacy], defaultMode = true, legacy
	} else if len(allowed) == 0 {
		allowed["all"], defaultMode = true, "all"
	} else if defaultMode == "" {
		if len(allowed) != 1 {
			return "", false
		}
		for mode := range allowed {
			defaultMode = mode
		}
	}
	if !allowed[defaultMode] {
		return "", false
	}
	if requested == "" {
		requested = defaultMode
	}
	return requested, allowed[requested]
}

func reportExportTagMatchMode(mode string) bool { return mode == "any" || mode == "all" }

func reportExportQueryOperator(operator string) bool {
	switch operator {
	case "contains", "starts_with", "eq":
		return true
	default:
		return false
	}
}

func reportExportFixedFilter(filter *reportmodel.ReportDatasetFilter) bool {
	switch filter.Operator {
	case "eq", "ne", "gt", "gte", "lt", "lte":
		return len(filter.Values) == 0
	case "in", "not_in":
		return filter.Value == nil && len(filter.Values) > 0 && len(filter.Values) <= 32
	case "is_null", "not_null":
		return filter.Value == nil && len(filter.Values) == 0
	default:
		return false
	}
}
