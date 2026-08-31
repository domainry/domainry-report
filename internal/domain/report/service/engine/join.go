package engine

import (
	"fmt"
	"strings"

	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

func joinRecordKey(record *Record, equalities []reportmodel.ReportDatasetJoinFieldEquality, left bool) (string, bool) {
	if record == nil || len(equalities) == 0 {
		return "", false
	}
	var key strings.Builder
	for _, equality := range equalities {
		field := equality.RightField
		if left {
			field = equality.LeftField
		}
		value, ok := RecordPointerValue(record, field)
		if !ok || value == nil {
			return "", false
		}
		stable := stableValue(value)
		fmt.Fprintf(&key, "%d:%s", len(stable), stable)
	}
	return key.String(), true
}

func ValidateJoinCardinality(leftRows []Row, rightRecords []Record, join reportmodel.ReportDatasetJoin) error {
	cardinality := strings.TrimSpace(join.Cardinality)
	if cardinality == "one_to_many" {
		return nil
	}
	right := map[string]map[string]bool{}
	for index := range rightRecords {
		record := &rightRecords[index]
		if key, ok := joinRecordKey(record, join.Equalities(), false); ok {
			if right[key] == nil {
				right[key] = map[string]bool{}
			}
			right[key][record.ID] = true
			if len(right[key]) > 1 {
				return joinCardinalityError(join)
			}
		}
	}
	if cardinality != "one_to_one" {
		return nil
	}
	left := map[string]map[string]bool{}
	for _, row := range leftRows {
		record := row[strings.TrimSpace(join.LeftAlias)]
		if key, ok := joinRecordKey(record, join.Equalities(), true); ok {
			if left[key] == nil {
				left[key] = map[string]bool{}
			}
			left[key][record.ID] = true
			if len(left[key]) > 1 {
				return joinCardinalityError(join)
			}
		}
	}
	return nil
}

func ValidateJoinedCardinality(rows []Row, join reportmodel.ReportDatasetJoin) error {
	rightRecords := []Record{}
	leftRows := make([]Row, 0, len(rows))
	seenRight := map[string]bool{}
	for _, row := range rows {
		leftRows = append(leftRows, row)
		if record := row[strings.TrimSpace(join.Alias)]; record != nil && !seenRight[record.ID] {
			seenRight[record.ID] = true
			rightRecords = append(rightRecords, *record)
		}
	}
	return ValidateJoinCardinality(leftRows, rightRecords, join)
}

func joinCardinalityError(join reportmodel.ReportDatasetJoin) error {
	return &JoinCardinalityError{Alias: strings.TrimSpace(join.Alias), Cardinality: strings.TrimSpace(join.Cardinality)}
}

func JoinRows(leftRows []Row, rightRecords []Record, join reportmodel.ReportDatasetJoin) []Row {
	index := map[string][]*Record{}
	for recordIndex := range rightRecords {
		record := &rightRecords[recordIndex]
		if key, ok := joinRecordKey(record, join.Equalities(), false); ok {
			index[key] = append(index[key], record)
		}
	}
	result := []Row{}
	for _, row := range leftRows {
		leftKey, ok := joinRecordKey(row[strings.TrimSpace(join.LeftAlias)], join.Equalities(), true)
		matches := index[leftKey]
		if !ok || len(matches) == 0 {
			if strings.TrimSpace(join.Type) == "left" {
				copyRow := CopyRow(row)
				copyRow[strings.TrimSpace(join.Alias)] = nil
				result = append(result, copyRow)
			}
			continue
		}
		for _, match := range matches {
			copyRow := CopyRow(row)
			copyRow[strings.TrimSpace(join.Alias)] = match
			result = append(result, copyRow)
		}
	}
	return result
}

type JoinCardinalityError struct {
	Alias       string
	Cardinality string
}

func (e *JoinCardinalityError) Error() string {
	return fmt.Sprintf("join %s violates %s cardinality", e.Alias, e.Cardinality)
}
