package objectsql

import sdkobjectsql "github.com/domainry/domainry-report-sdk/query/objectsql"

type DatasetPlan = sdkobjectsql.DatasetPlan

var NormalizeParameters = sdkobjectsql.NormalizeParameters
var NormalizeDeclaredParameters = sdkobjectsql.NormalizeDeclaredParameters
var EnsureDatasetStableOrder = sdkobjectsql.EnsureDatasetStableOrder
var ResultType = sdkobjectsql.ResultType
var Binary = sdkobjectsql.Binary
var Conjunction = sdkobjectsql.Conjunction
var OrderContains = sdkobjectsql.OrderContains
var SafeCrossWorkspaceAggregatePlan = sdkobjectsql.SafeCrossWorkspaceAggregatePlan
var ExpressionHasAggregate = sdkobjectsql.ExpressionHasAggregate
var CompileReportObjectSQL = sdkobjectsql.CompileReportObjectSQL
var ReportObjectSQLPlanSingleRow = sdkobjectsql.ReportObjectSQLPlanSingleRow
var BuildDatasetPlan = sdkobjectsql.BuildDatasetPlan
var DatasetFieldExpression = sdkobjectsql.DatasetFieldExpression
var DatasetFilter = sdkobjectsql.DatasetFilter

const (
	ReportObjectSQLDefaultLimitRows = sdkobjectsql.ReportObjectSQLDefaultLimitRows
	ReportObjectSQLMaximumLimitRows = sdkobjectsql.ReportObjectSQLMaximumLimitRows
)
