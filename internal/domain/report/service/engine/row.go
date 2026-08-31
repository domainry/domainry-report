package engine

import (
	"strings"

	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

// Record is the minimum, source-owned representation required by Report's
// in-memory dataset engine. Hosts adapt their authorized records into it.
type Record struct {
	ID        string
	Data      map[string]any
	CreatedAt string
	UpdatedAt string
}

// Field and Object carry only metadata that affects Report calculations. They
// deliberately exclude Runtime definition, authorization, and persistence data.
type Field struct {
	Key       string
	Type      string
	Precision int
	Scale     int32
}

type Object struct {
	Fields []Field
}

type AggregateResult struct {
	Rows              []reportmodel.ReportResultRow
	VisibleSourceRows int
}

type CalculationError struct {
	Stage string
	Err   error
}

func (e *CalculationError) Error() string { return e.Err.Error() }
func (e *CalculationError) Unwrap() error { return e.Err }

// Row is an alias-addressed record tuple consumed by joins, filters, analyses,
// and aggregation. Authorization must be completed by the host before adapting
// records into a Row.
type Row map[string]*Record

func CopyRow(row Row) Row {
	copyRow := make(Row, len(row)+1)
	for alias, record := range row {
		copyRow[alias] = record
	}
	return copyRow
}

func FieldValue(row Row, field reportmodel.ReportDatasetField) (any, bool) {
	return RecordPointerValue(row[strings.TrimSpace(field.SourceAlias)], field.FieldKey)
}

func RecordPointerValue(record *Record, field string) (any, bool) {
	if record == nil {
		return nil, false
	}
	return RecordValue(*record, field)
}

func RecordValue(record Record, field string) (any, bool) {
	switch strings.TrimSpace(field) {
	case "id":
		return record.ID, record.ID != ""
	case "created_at":
		return record.CreatedAt, record.CreatedAt != ""
	case "updated_at":
		return record.UpdatedAt, record.UpdatedAt != ""
	default:
		value, ok := record.Data[strings.TrimSpace(field)]
		return value, ok
	}
}
