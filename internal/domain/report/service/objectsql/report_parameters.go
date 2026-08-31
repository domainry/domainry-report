package objectsql

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/domainry/domainry-foundation/apperror"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
	"github.com/shopspring/decimal"
)

// NormalizeParameters applies the Report-authored type/default contract before a
// query or governed export is executed and fingerprinted.
func NormalizeParameters(declarations []reportmodel.ReportObjectSQLParameter, raw map[string]any) (map[string]any, error) {
	declared := make(map[string]reportmodel.ReportObjectSQLParameter, len(declarations))
	for _, parameter := range declarations {
		parameter.Key = strings.TrimSpace(parameter.Key)
		parameter.Type = strings.TrimSpace(parameter.Type)
		declared[parameter.Key] = parameter
	}
	return NormalizeDeclaredParameters(declared, raw)
}

func NormalizeDeclaredParameters(declared map[string]reportmodel.ReportObjectSQLParameter, raw map[string]any) (map[string]any, error) {
	if raw == nil {
		raw = map[string]any{}
	}
	for key := range raw {
		if declared[key].Key == "" {
			return nil, &apperror.AppError{Kind: apperror.KindBadRequest, Code: "backend.report.object_sql_parameter_unknown", Params: map[string]string{"parameter": key}}
		}
	}
	result := make(map[string]any, len(declared))
	for key, parameter := range declared {
		value, exists := raw[key]
		if !exists && parameter.Default != nil {
			value, exists = parameter.Default, true
		}
		if !exists {
			if parameter.Required {
				return nil, &apperror.AppError{Kind: apperror.KindBadRequest, Code: "backend.report.object_sql_parameter_required", Params: map[string]string{"parameter": key}}
			}
			result[key] = nil
			continue
		}
		normalized, err := parameterValue(parameter.Type, value)
		if err != nil {
			return nil, &apperror.AppError{Kind: apperror.KindBadRequest, Code: "backend.report.object_sql_parameter_type_invalid", Params: map[string]string{"parameter": key, "type": parameter.Type}, Err: err}
		}
		result[key] = normalized
	}
	return result, nil
}

func parameterValue(parameterType string, value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch parameterType {
	case "text":
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("text parameter must be a string")
		}
		return text, nil
	case "integer":
		switch typed := value.(type) {
		case json.Number:
			return typed.Int64()
		case string:
			return strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		case int:
			return int64(typed), nil
		case int64:
			return typed, nil
		}
		return nil, fmt.Errorf("integer parameter must be an integer")
	case "decimal":
		parsed, err := decimal.NewFromString(strings.TrimSpace(fmt.Sprint(value)))
		if err != nil {
			return nil, err
		}
		return parsed.String(), nil
	case "boolean":
		boolean, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("boolean parameter must be a boolean")
		}
		return boolean, nil
	case "date":
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("date parameter must be a string")
		}
		if _, err := time.Parse("2006-01-02", text); err != nil {
			return nil, err
		}
		return text, nil
	case "datetime":
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("datetime parameter must be a string")
		}
		parsed, err := time.Parse(time.RFC3339, text)
		if err != nil {
			return nil, err
		}
		return parsed.UTC().Format(time.RFC3339Nano), nil
	default:
		return nil, fmt.Errorf("unsupported parameter type %q", parameterType)
	}
}
