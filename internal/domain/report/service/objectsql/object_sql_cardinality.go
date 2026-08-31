package objectsql

import (
	"fmt"
	"sort"

	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

func (c *objectSQLCompiler) validateAmplification(plan reportmodel.ReportObjectSQLPlan) error {
	for projectionIndex, projection := range plan.Projections {
		var validationErr error
		walkObjectSQLExpression(projection.Expression, func(expression reportmodel.ReportObjectSQLExpression) {
			if validationErr != nil || expression.Kind != "aggregate" || expression.Distinct || expression.Name == "min" || expression.Name == "max" {
				return
			}
			sources := map[string]bool{}
			for _, argument := range expression.Arguments {
				collectObjectSQLFieldAliases(argument, sources)
			}
			for joinIndex, source := range plan.Sources {
				if source.Cardinality != "one_to_many" {
					continue
				}
				if len(sources) == 0 && expression.Name == "count" {
					validationErr = objectSQLPlanError("backend.report.join_measure_amplification", fmt.Sprintf("object_sql_v1.result_schema[%d]", projectionIndex), map[string]string{"measure": projection.Alias, "join_alias": source.Alias})
					return
				}
				for alias := range sources {
					if c.aliasOrder[alias] < joinIndex {
						validationErr = objectSQLPlanError("backend.report.join_measure_amplification", fmt.Sprintf("object_sql_v1.result_schema[%d]", projectionIndex), map[string]string{"measure": projection.Alias, "source_alias": alias, "join_alias": source.Alias})
						return
					}
				}
			}
		})
		if validationErr != nil {
			return validationErr
		}
	}
	return nil
}

func (c *objectSQLCompiler) populateSourceFields(plan *reportmodel.ReportObjectSQLPlan) {
	fields := map[string]map[string]bool{}
	for _, source := range plan.Sources {
		fields[source.Alias] = map[string]bool{}
		if source.On != nil {
			collectObjectSQLFields(*source.On, fields)
		}
	}
	for _, projection := range plan.Projections {
		collectObjectSQLFields(projection.Expression, fields)
	}
	if plan.Where != nil {
		collectObjectSQLFields(*plan.Where, fields)
	}
	if plan.Having != nil {
		collectObjectSQLFields(*plan.Having, fields)
	}
	for _, expression := range plan.GroupBy {
		collectObjectSQLFields(expression, fields)
	}
	for _, order := range plan.OrderBy {
		collectObjectSQLFields(order.Expression, fields)
	}
	for index := range plan.Sources {
		for field := range fields[plan.Sources[index].Alias] {
			plan.Sources[index].Fields = append(plan.Sources[index].Fields, field)
		}
		sort.Strings(plan.Sources[index].Fields)
	}
}

func collectObjectSQLFields(expression reportmodel.ReportObjectSQLExpression, fields map[string]map[string]bool) {
	if expression.Kind == "field" {
		fields[expression.Alias][expression.FieldKey] = true
	}
	for _, argument := range expression.Arguments {
		collectObjectSQLFields(argument, fields)
	}
	for _, when := range expression.Whens {
		collectObjectSQLFields(when.Condition, fields)
		collectObjectSQLFields(when.Value, fields)
	}
	if expression.Else != nil {
		collectObjectSQLFields(*expression.Else, fields)
	}
}

func collectObjectSQLFieldAliases(expression reportmodel.ReportObjectSQLExpression, aliases map[string]bool) {
	if expression.Kind == "field" {
		aliases[expression.Alias] = true
	}
	for _, argument := range expression.Arguments {
		collectObjectSQLFieldAliases(argument, aliases)
	}
	for _, when := range expression.Whens {
		collectObjectSQLFieldAliases(when.Condition, aliases)
		collectObjectSQLFieldAliases(when.Value, aliases)
	}
	if expression.Else != nil {
		collectObjectSQLFieldAliases(*expression.Else, aliases)
	}
}

func walkObjectSQLExpression(expression reportmodel.ReportObjectSQLExpression, visit func(reportmodel.ReportObjectSQLExpression)) {
	visit(expression)
	for _, argument := range expression.Arguments {
		walkObjectSQLExpression(argument, visit)
	}
	for _, when := range expression.Whens {
		walkObjectSQLExpression(when.Condition, visit)
		walkObjectSQLExpression(when.Value, visit)
	}
	if expression.Else != nil {
		walkObjectSQLExpression(*expression.Else, visit)
	}
}

func objectSQLResultCompatible(result reportmodel.ReportResultColumnSchema, expression reportmodel.ReportObjectSQLExpression) bool {
	if result.Type == expression.Type {
		return result.Type != "currency" && result.Type != "decimal" || result.Scale == 0 || result.Scale == expression.Scale
	}
	return result.Type == "decimal" && (expression.Type == "decimal" || expression.Type == "integer")
}
