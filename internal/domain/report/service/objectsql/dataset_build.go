package objectsql

import (
	"fmt"
	"strings"

	reportmodel "github.com/domainry/domainry-report-sdk/model"
	reportengine "github.com/domainry/domainry-report/internal/domain/report/service/engine"
)

type DatasetPlan struct {
	Plan       reportmodel.ReportObjectSQLPlan
	Parameters map[string]any
}

func BuildDatasetPlan(dataset reportmodel.ReportDatasetSchema, aliasObjects map[string]string, aliases []string, selectFields map[string][]string, objects map[string]reportengine.Object) (DatasetPlan, error) {
	sqlPlan := reportmodel.ReportObjectSQLPlan{
		Sources: make([]reportmodel.ReportObjectSQLSource, 0, len(aliases)), Parameters: map[string]reportmodel.ReportObjectSQLParameter{},
		ResultSchema: []reportmodel.ReportResultColumnSchema{}, Limit: dataset.Limit,
	}
	if sqlPlan.Limit <= 0 {
		sqlPlan.Limit = 10000
	}
	for index, alias := range aliases {
		source := reportmodel.ReportObjectSQLSource{ObjectKey: aliasObjects[alias], Alias: alias, Fields: append([]string(nil), selectFields[alias]...)}
		if index > 0 {
			join := dataset.Joins[index-1]
			source.JoinType, source.Cardinality = strings.TrimSpace(join.Type), strings.TrimSpace(join.Cardinality)
			conditions := make([]reportmodel.ReportObjectSQLExpression, 0, len(join.Equalities()))
			for _, equality := range join.Equalities() {
				left := DatasetFieldExpression(objects, reportmodel.ReportDatasetField{SourceAlias: join.LeftAlias, FieldKey: equality.LeftField})
				right := DatasetFieldExpression(objects, reportmodel.ReportDatasetField{SourceAlias: join.Alias, FieldKey: equality.RightField})
				conditions = append(conditions, Binary("comparison", "=", left, right))
			}
			condition := Conjunction(conditions, "and")
			source.On = &condition
		}
		sqlPlan.Sources = append(sqlPlan.Sources, source)
	}

	for _, dimension := range dataset.Dimensions {
		expression := DatasetFieldExpression(objects, dimension.Field)
		grain := strings.TrimSpace(dimension.TimeGrain)
		if grain == "" {
			grain = strings.TrimSpace(dataset.DefaultTimeGrain)
		}
		if grain != "" {
			expression = reportmodel.ReportObjectSQLExpression{Kind: "function", Name: "date_bucket", Value: grain, Type: expression.Type, Arguments: []reportmodel.ReportObjectSQLExpression{expression}}
		}
		sqlPlan.Projections = append(sqlPlan.Projections, reportmodel.ReportObjectSQLProjection{Alias: dimension.Key, Expression: expression})
		sqlPlan.GroupBy = append(sqlPlan.GroupBy, expression)
		sqlPlan.ResultSchema = append(sqlPlan.ResultSchema, reportmodel.ReportResultColumnSchema{Key: dimension.Key, Type: ResultType(expression.Type), Kind: "dimension", Precision: expression.Precision, Scale: expression.Scale})
	}
	measureExpressions := map[string]reportmodel.ReportObjectSQLExpression{}
	measureByKey := map[string]reportmodel.ReportDatasetMeasure{}
	for _, measure := range dataset.Measures {
		measureByKey[measure.Key] = measure
	}
	var buildMeasure func(string, map[string]bool) (reportmodel.ReportObjectSQLExpression, error)
	buildMeasure = func(key string, visiting map[string]bool) (reportmodel.ReportObjectSQLExpression, error) {
		if expression := measureExpressions[key]; expression.Kind != "" {
			return expression, nil
		}
		if visiting[key] {
			return reportmodel.ReportObjectSQLExpression{}, fmt.Errorf("cyclic ratio")
		}
		visiting[key] = true
		defer delete(visiting, key)
		measure, ok := measureByKey[key]
		if !ok {
			return reportmodel.ReportObjectSQLExpression{}, fmt.Errorf("unknown measure %s", key)
		}
		operation := strings.TrimSpace(measure.Operation)
		var expression reportmodel.ReportObjectSQLExpression
		switch operation {
		case "count":
			alias := strings.TrimSpace(measure.SourceAlias)
			if alias == "" {
				alias = strings.TrimSpace(dataset.Source.Alias)
			}
			expression = reportmodel.ReportObjectSQLExpression{Kind: "aggregate", Name: "count", Type: "integer", Arguments: []reportmodel.ReportObjectSQLExpression{{Kind: "field", Alias: alias, FieldKey: "id", Type: "text"}}}
		case "distinct_count", "sum", "avg", "min", "max":
			if measure.Field == nil {
				return reportmodel.ReportObjectSQLExpression{}, fmt.Errorf("measure field missing")
			}
			field := DatasetFieldExpression(objects, *measure.Field)
			name := operation
			if operation == "distinct_count" {
				name = "count"
			}
			expression = reportmodel.ReportObjectSQLExpression{Kind: "aggregate", Name: name, Type: field.Type, Precision: field.Precision, Scale: field.Scale, Distinct: operation == "distinct_count", Arguments: []reportmodel.ReportObjectSQLExpression{field}}
			if operation == "distinct_count" {
				expression.Type, expression.Precision, expression.Scale = "integer", 0, 0
			}
		case "ratio":
			numerator, err := buildMeasure(measure.NumeratorKey, visiting)
			if err != nil {
				return reportmodel.ReportObjectSQLExpression{}, err
			}
			denominator, err := buildMeasure(measure.DenominatorKey, visiting)
			if err != nil {
				return reportmodel.ReportObjectSQLExpression{}, err
			}
			expression = reportmodel.ReportObjectSQLExpression{Kind: "binary", Operator: "/", Type: "decimal", Precision: 38, Scale: 6, Arguments: []reportmodel.ReportObjectSQLExpression{numerator, {Kind: "function", Name: "nullif", Type: denominator.Type, Arguments: []reportmodel.ReportObjectSQLExpression{denominator, {Kind: "literal", Type: "integer", ValueType: "integer", Value: "0"}}}}}
		}
		measureExpressions[key] = expression
		return expression, nil
	}
	for _, measure := range dataset.Measures {
		expression, err := buildMeasure(measure.Key, map[string]bool{})
		if err != nil {
			return DatasetPlan{}, err
		}
		sqlPlan.Projections = append(sqlPlan.Projections, reportmodel.ReportObjectSQLProjection{Alias: measure.Key, Expression: expression})
		sqlPlan.ResultSchema = append(sqlPlan.ResultSchema, reportmodel.ReportResultColumnSchema{Key: measure.Key, Type: ResultType(expression.Type), Kind: "measure", Precision: expression.Precision, Scale: expression.Scale})
	}

	filterExpressions := make([]reportmodel.ReportObjectSQLExpression, 0, len(dataset.Filters))
	parameters := map[string]any{}
	for index, filter := range dataset.Filters {
		expression, err := DatasetFilter(objects, filter, fmt.Sprintf("dataset_filter_%d", index), sqlPlan.Parameters, parameters)
		if err != nil {
			return DatasetPlan{}, err
		}
		filterExpressions = append(filterExpressions, expression)
	}
	if len(filterExpressions) > 0 {
		where := Conjunction(filterExpressions, "and")
		sqlPlan.Where = &where
	}
	if dataset.Privacy != nil {
		entity := DatasetFieldExpression(objects, dataset.Privacy.EntityField)
		count := reportmodel.ReportObjectSQLExpression{Kind: "aggregate", Name: "count", Type: "integer", Distinct: true, Arguments: []reportmodel.ReportObjectSQLExpression{entity}}
		minimum := reportmodel.ReportObjectSQLExpression{Kind: "literal", Type: "integer", ValueType: "integer", Value: fmt.Sprint(dataset.Privacy.MinimumGroupSize)}
		having := Binary("comparison", ">=", count, minimum)
		sqlPlan.Having = &having
	}
	for _, sortRule := range dataset.Sort {
		sqlPlan.OrderBy = append(sqlPlan.OrderBy, reportmodel.ReportObjectSQLOrder{Expression: reportmodel.ReportObjectSQLExpression{Kind: "result", Alias: sortRule.Key}, Direction: sortRule.Direction})
	}
	for _, dimension := range dataset.Dimensions {
		candidate := reportmodel.ReportObjectSQLOrder{Expression: reportmodel.ReportObjectSQLExpression{Kind: "result", Alias: dimension.Key}, Direction: "asc"}
		if !OrderContains(sqlPlan.OrderBy, dimension.Key) {
			sqlPlan.OrderBy = append(sqlPlan.OrderBy, candidate)
		}
	}
	EnsureDatasetStableOrder(&sqlPlan)
	return DatasetPlan{Plan: sqlPlan, Parameters: parameters}, nil
}
