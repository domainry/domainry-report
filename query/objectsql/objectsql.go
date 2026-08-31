// Package objectsql is the compatibility facade for Report's internal ObjectSQL planner.
package objectsql

import (
	reportmodel "github.com/domainry/domainry-report-sdk/model"
	domainobjectsql "github.com/domainry/domainry-report/internal/domain/report/service/objectsql"
	reportengine "github.com/domainry/domainry-report/query/engine"
)

type DatasetPlan = domainobjectsql.DatasetPlan

var NormalizeParameters = domainobjectsql.NormalizeParameters
var NormalizeDeclaredParameters = domainobjectsql.NormalizeDeclaredParameters
var EnsureDatasetStableOrder = domainobjectsql.EnsureDatasetStableOrder
var ResultType = domainobjectsql.ResultType
var Binary = domainobjectsql.Binary
var Conjunction = domainobjectsql.Conjunction
var OrderContains = domainobjectsql.OrderContains
var SafeCrossWorkspaceAggregatePlan = domainobjectsql.SafeCrossWorkspaceAggregatePlan
var ExpressionHasAggregate = domainobjectsql.ExpressionHasAggregate

const (
	ReportObjectSQLDefaultLimitRows = domainobjectsql.ReportObjectSQLDefaultLimitRows
	ReportObjectSQLMaximumLimitRows = domainobjectsql.ReportObjectSQLMaximumLimitRows
)

func CompileReportObjectSQL(schema reportmodel.ReportObjectSQLSchema, objects map[string]reportengine.Object) (reportmodel.ReportObjectSQLPlan, error) {
	return domainobjectsql.CompileReportObjectSQL(schema, objects)
}

func ReportObjectSQLPlanSingleRow(plan reportmodel.ReportObjectSQLPlan) bool {
	return domainobjectsql.ReportObjectSQLPlanSingleRow(plan)
}

func BuildDatasetPlan(dataset reportmodel.ReportDatasetSchema, aliasObjects map[string]string, aliases []string, selectFields map[string][]string, objects map[string]reportengine.Object) (DatasetPlan, error) {
	return domainobjectsql.BuildDatasetPlan(dataset, aliasObjects, aliases, selectFields, objects)
}

func DatasetFieldExpression(objects map[string]reportengine.Object, reference reportmodel.ReportDatasetField) reportmodel.ReportObjectSQLExpression {
	return domainobjectsql.DatasetFieldExpression(objects, reference)
}

func DatasetFilter(objects map[string]reportengine.Object, filter reportmodel.ReportDatasetFilter, prefix string, declared map[string]reportmodel.ReportObjectSQLParameter, parameters map[string]any) (reportmodel.ReportObjectSQLExpression, error) {
	return domainobjectsql.DatasetFilter(objects, filter, prefix, declared, parameters)
}
