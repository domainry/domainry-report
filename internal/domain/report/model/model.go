// Package model defines Report's domain vocabulary. The wire-stable SDK model
// is re-exported here so internal layers depend on a domain-named boundary.
package model

import reportmodel "github.com/domainry/domainry-report-sdk/model"

type ReportSchema = reportmodel.ReportSchema
type ReportDatasetPlan = reportmodel.ReportDatasetPlan
type ReportDatasetSchema = reportmodel.ReportDatasetSchema
type ReportResultRow = reportmodel.ReportResultRow
