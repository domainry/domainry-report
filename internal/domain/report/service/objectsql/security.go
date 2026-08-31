package objectsql

import reportmodel "github.com/domainry/domainry-report-sdk/model"

func SafeCrossWorkspaceAggregatePlan(plan reportmodel.ReportObjectSQLPlan) bool {
	for _, projection := range plan.Projections {
		if ExpressionHasAggregate(projection.Expression) || projection.Expression.Kind == "field" && projection.Expression.FieldKey == "workspace_id" || projection.Expression.Kind == "function" && projection.Expression.Name == "date_bucket" {
			continue
		}
		return false
	}
	for _, group := range plan.GroupBy {
		if group.Kind == "field" && group.FieldKey == "workspace_id" || group.Kind == "function" && group.Name == "date_bucket" {
			continue
		}
		return false
	}
	if len(plan.GroupBy) == 0 {
		for _, projection := range plan.Projections {
			if ExpressionHasAggregate(projection.Expression) {
				return true
			}
		}
		return false
	}
	return true
}

func ExpressionHasAggregate(expression reportmodel.ReportObjectSQLExpression) bool {
	if expression.Kind == "aggregate" {
		return true
	}
	for _, argument := range expression.Arguments {
		if ExpressionHasAggregate(argument) {
			return true
		}
	}
	for _, when := range expression.Whens {
		if ExpressionHasAggregate(when.Condition) || ExpressionHasAggregate(when.Value) {
			return true
		}
	}
	return expression.Else != nil && ExpressionHasAggregate(*expression.Else)
}
