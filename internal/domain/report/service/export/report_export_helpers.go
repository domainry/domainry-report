package export

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/domainry/domainry-foundation/apperror"
	reportcontract "github.com/domainry/domainry-report-sdk/contract"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

func validReportExportFilter(operator string, values int) bool {
	switch operator {
	case "is_null", "not_null":
		return values == 0
	case "eq", "ne", "gt", "gte", "lt", "lte":
		return values == 1
	case "between":
		return values == 2
	case "in", "not_in":
		return values >= 1 && values <= 64
	default:
		return false
	}
}

func reportMeasureFields(measure reportmodel.ReportDatasetMeasure) []reportmodel.ReportDatasetField {
	fields := []reportmodel.ReportDatasetField{}
	for _, field := range []*reportmodel.ReportDatasetField{measure.Field, measure.StartField, measure.EndField} {
		if field != nil {
			fields = append(fields, *field)
		}
	}
	return fields
}

func normalizeScopeStrings(values []string, maximum, maximumLength int) []string {
	if values == nil {
		return nil
	}
	if len(values) > maximum {
		return nil
	}
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > maximumLength {
			return nil
		}
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func normalizeScopeProjection(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func stringValuesAsAny(values []string) []any {
	result := make([]any, len(values))
	for index := range values {
		result[index] = values[index]
	}
	return result
}

func reportExportDate(value string) (time.Time, error) {
	return reportExportDateBoundary(value, time.UTC, false)
}

func reportExportDateBoundary(value string, location *time.Location, endOfDay bool) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed, nil
	}
	if location == nil {
		location = time.UTC
	}
	parsed, err := time.ParseInLocation("2006-01-02", value, location)
	if err != nil {
		return time.Time{}, err
	}
	if endOfDay {
		parsed = parsed.AddDate(0, 0, 1).Add(-time.Nanosecond)
	}
	return parsed, nil
}

func defaultReportTimeZone(report reportmodel.ReportSchema) string {
	value := strings.TrimSpace(report.Dataset.TimeZone)
	if value == "" {
		return "UTC"
	}
	return value
}

func CanonicalJSONSHA256(value any) (string, error) {
	return reportcontract.CanonicalJSONSHA256(value)
}

func canonicalJSONEqual(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func reportMetricDefinitionsEqual(requested, expected []reportmodel.ReportMetricDefinitionRef) bool {
	if len(requested) != len(expected) {
		return false
	}
	expectedByKey := make(map[string]string, len(expected))
	for _, definition := range expected {
		expectedByKey[definition.Key] = definition.Version
	}
	seen := make(map[string]struct{}, len(requested))
	for _, definition := range requested {
		key, version := strings.TrimSpace(definition.Key), strings.TrimSpace(definition.Version)
		if key == "" || version == "" || expectedByKey[key] != version {
			return false
		}
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}

func SHA256Hex(content []byte) string {
	return reportcontract.SHA256Hex(content)
}

func SafeFilename(reportKey, objectKey string) string {
	return reportcontract.SafeExportFilename(reportKey, objectKey)
}

func exportScopeError(code string) error {
	return &apperror.AppError{Kind: apperror.KindBadRequest, Code: code}
}
