// Package objectsql is the compatibility facade for Report's internal ObjectSQL planner.
package objectsql

import (
	reportmodel "github.com/domainry/domainry-report-sdk/model"
	reportquery "github.com/domainry/domainry-report-sdk/query"
	domainobjectsql "github.com/domainry/domainry-report-sdk/query/objectsql"
)

var NormalizeParameters = domainobjectsql.NormalizeParameters
var NormalizeDeclaredParameters = domainobjectsql.NormalizeDeclaredParameters
var SafeCrossWorkspaceAggregatePlan = domainobjectsql.SafeCrossWorkspaceAggregatePlan
var ExpressionHasAggregate = domainobjectsql.ExpressionHasAggregate

const (
	ReportObjectSQLDefaultLimitRows = domainobjectsql.ReportObjectSQLDefaultLimitRows
	ReportObjectSQLMaximumLimitRows = domainobjectsql.ReportObjectSQLMaximumLimitRows
)

func CompileReportObjectSQL(schema reportmodel.ReportObjectSQLSchema, objects map[string]reportquery.Object) (reportmodel.ReportObjectSQLPlan, error) {
	return domainobjectsql.CompileReportObjectSQL(schema, objects)
}

func ReportObjectSQLPlanSingleRow(plan reportmodel.ReportObjectSQLPlan) bool {
	return domainobjectsql.ReportObjectSQLPlanSingleRow(plan)
}
