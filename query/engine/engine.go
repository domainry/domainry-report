// Package engine is the compatibility facade for Report's source-owned query engine.
// New composition code lives under internal/domain/report/service/engine.
package engine

import (
	reportmodel "github.com/domainry/domainry-report-sdk/model"
	domainengine "github.com/domainry/domainry-report/internal/domain/report/service/engine"
)

type Record = domainengine.Record
type Field = domainengine.Field
type Object = domainengine.Object
type AggregateResult = domainengine.AggregateResult
type CalculationError = domainengine.CalculationError
type JoinCardinalityError = domainengine.JoinCardinalityError
type Row = domainengine.Row

var CopyRow = domainengine.CopyRow
var FieldValue = domainengine.FieldValue
var RecordPointerValue = domainengine.RecordPointerValue
var RecordValue = domainengine.RecordValue
var CompareValues = domainengine.CompareValues
var StableValue = domainengine.StableValue
var RowMatchesFilters = domainengine.RowMatchesFilters
var FilterMatches = domainengine.FilterMatches
var ValidateJoinCardinality = domainengine.ValidateJoinCardinality
var ValidateJoinedCardinality = domainengine.ValidateJoinedCardinality
var JoinRows = domainengine.JoinRows
var ApplyRuntimeQuery = domainengine.ApplyRuntimeQuery
var ApplyRuntimeTags = domainengine.ApplyRuntimeTags
var ExecuteAnalyses = domainengine.ExecuteAnalyses

func Aggregate(rows []Row, dataset reportmodel.ReportDatasetSchema, objects map[string]Object) (AggregateResult, error) {
	return domainengine.Aggregate(rows, dataset, objects)
}
