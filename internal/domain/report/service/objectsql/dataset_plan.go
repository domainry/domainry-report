package objectsql

import (
	"fmt"
	"strings"

	reportmodel "github.com/domainry/domainry-report-sdk/model"
	reportengine "github.com/domainry/domainry-report/internal/domain/report/service/engine"
)

func EnsureDatasetStableOrder(plan *reportmodel.ReportObjectSQLPlan) {
	if plan == nil || len(plan.OrderBy) != 0 || len(plan.GroupBy) != 0 || len(plan.Projections) == 0 {
		return
	}
	plan.OrderBy = append(plan.OrderBy, reportmodel.ReportObjectSQLOrder{Expression: reportmodel.ReportObjectSQLExpression{Kind: "result", Alias: plan.Projections[0].Alias}, Direction: "asc"})
}

func DatasetFieldExpression(objects map[string]reportengine.Object, reference reportmodel.ReportDatasetField) reportmodel.ReportObjectSQLExpression {
	object := objects[strings.TrimSpace(reference.SourceAlias)]
	field := reportengine.Field{Key: reference.FieldKey, Type: "text", Scale: -1}
	for _, candidate := range object.Fields {
		if strings.TrimSpace(candidate.Key) == strings.TrimSpace(reference.FieldKey) {
			field = candidate
			break
		}
	}
	return reportmodel.ReportObjectSQLExpression{Kind: "field", Alias: strings.TrimSpace(reference.SourceAlias), FieldKey: strings.TrimSpace(reference.FieldKey), Type: field.Type, Precision: field.Precision, Scale: int(field.Scale)}
}

func ResultType(value string) string {
	switch value {
	case "text", "integer", "number", "decimal", "boolean", "date", "datetime", "currency":
		return value
	default:
		return "text"
	}
}

func DatasetFilter(objects map[string]reportengine.Object, filter reportmodel.ReportDatasetFilter, prefix string, declared map[string]reportmodel.ReportObjectSQLParameter, parameters map[string]any) (reportmodel.ReportObjectSQLExpression, error) {
	field := DatasetFieldExpression(objects, filter.Field)
	parameter := func(suffix string, value any) reportmodel.ReportObjectSQLExpression {
		key := prefix + suffix
		parameterType := ResultType(field.Type)
		if parameterType == "currency" {
			parameterType = "decimal"
		}
		declared[key] = reportmodel.ReportObjectSQLParameter{Key: key, Type: parameterType, Required: true}
		parameters[key] = value
		return reportmodel.ReportObjectSQLExpression{Kind: "parameter", Name: key, Type: field.Type, Precision: field.Precision, Scale: field.Scale}
	}
	operator := strings.TrimSpace(filter.Operator)
	switch operator {
	case "eq", "ne", "gt", "gte", "lt", "lte":
		sqlOperator := map[string]string{"eq": "=", "ne": "!=", "gt": ">", "gte": ">=", "lt": "<", "lte": "<="}[operator]
		return Binary("comparison", sqlOperator, field, parameter("_value", filter.Value)), nil
	case "between":
		if len(filter.Values) != 2 {
			return reportmodel.ReportObjectSQLExpression{}, fmt.Errorf("invalid between filter")
		}
		return reportmodel.ReportObjectSQLExpression{Kind: "between", Operator: "between", Arguments: []reportmodel.ReportObjectSQLExpression{field, parameter("_from", filter.Values[0]), parameter("_to", filter.Values[1])}}, nil
	case "in", "not_in":
		if len(filter.Values) == 0 {
			return reportmodel.ReportObjectSQLExpression{}, fmt.Errorf("empty set filter")
		}
		items := make([]reportmodel.ReportObjectSQLExpression, 0, len(filter.Values))
		for index, value := range filter.Values {
			comparison := "="
			if operator == "not_in" {
				comparison = "!="
			}
			items = append(items, Binary("comparison", comparison, field, parameter(fmt.Sprintf("_%d", index), value)))
		}
		join := "or"
		if operator == "not_in" {
			join = "and"
		}
		return Conjunction(items, join), nil
	case "is_null", "not_null":
		sqlOperator := "IS NULL"
		if operator == "not_null" {
			sqlOperator = "IS NOT NULL"
		}
		return reportmodel.ReportObjectSQLExpression{Kind: "is", Operator: sqlOperator, Arguments: []reportmodel.ReportObjectSQLExpression{field}}, nil
	default:
		return reportmodel.ReportObjectSQLExpression{}, fmt.Errorf("unsupported filter %s", operator)
	}
}

func Binary(kind, operator string, left, right reportmodel.ReportObjectSQLExpression) reportmodel.ReportObjectSQLExpression {
	return reportmodel.ReportObjectSQLExpression{Kind: kind, Operator: operator, Type: left.Type, Precision: left.Precision, Scale: left.Scale, Arguments: []reportmodel.ReportObjectSQLExpression{left, right}}
}

func Conjunction(values []reportmodel.ReportObjectSQLExpression, operator string) reportmodel.ReportObjectSQLExpression {
	if len(values) == 1 {
		return values[0]
	}
	result := Binary("logical", operator, values[0], values[1])
	for _, value := range values[2:] {
		result = Binary("logical", operator, result, value)
	}
	return result
}

func OrderContains(orders []reportmodel.ReportObjectSQLOrder, alias string) bool {
	for _, order := range orders {
		if order.Expression.Kind == "result" && order.Expression.Alias == alias {
			return true
		}
	}
	return false
}
