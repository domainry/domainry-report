package engine

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

func TestReportAggregateDatasetRowsEdges(t *testing.T) {
	field := func(alias, key string) *reportmodel.ReportDatasetField {
		return &reportmodel.ReportDatasetField{SourceAlias: alias, FieldKey: key}
	}
	if _, err := reportAggregateDatasetRows(nil, reportmodel.ReportDatasetSchema{TimeZone: "not/a-zone"}, nil); err == nil {
		t.Fatal("expected invalid time zone error")
	}
	groups, err := reportAggregateDatasetRows(nil, reportmodel.ReportDatasetSchema{
		Dimensions: []reportmodel.ReportDatasetDimension{{Key: "kind", Field: *field("events", "kind")}},
	}, nil)
	if err != nil || len(groups) != 0 {
		t.Fatalf("dimensioned empty groups=%#v err=%v", groups, err)
	}
	rows := []Row{
		{"events": {ID: "", Data: map[string]any{"kind": "a", "amount": "1.00", "entity": ""}}},
		{"events": {ID: "event-1", Data: map[string]any{"kind": "a", "amount": "2.00"}}},
		{"events": nil},
	}
	dataset := reportmodel.ReportDatasetSchema{
		Dimensions: []reportmodel.ReportDatasetDimension{{Key: "kind", Field: *field("events", "kind")}},
		Privacy:    &reportmodel.ReportDatasetPrivacy{MinimumGroupSize: 1, EntityField: *field("events", "entity")},
		Measures: []reportmodel.ReportDatasetMeasure{
			{Key: "ratio", Operation: "ratio"},
			{Key: "count", Operation: "count", SourceAlias: "events"},
		},
	}
	groups, err = reportAggregateDatasetRows(rows, dataset, nil)
	if err != nil || len(groups) != 2 {
		t.Fatalf("groups=%#v err=%v", groups, err)
	}
}

func TestReportAccumulateMeasureEdges(t *testing.T) {
	field := func(key string) *reportmodel.ReportDatasetField {
		return &reportmodel.ReportDatasetField{SourceAlias: "events", FieldKey: key}
	}
	row := Row{"events": {ID: "event-1", Data: map[string]any{
		"nil": nil, "blank": " ", "value": "10", "low": "2", "high": "20",
		"bad": "x", "start": "2026-01-01", "end": "2026-01-02",
		"bad_start": "bad", "bad_end": "bad",
	}}}
	objects := map[string]Object{"events": {Fields: []Field{
		{Key: "value", Type: "number"}, {Key: "bad", Type: "number"},
	}}}
	state := &reportMeasureState{distinct: map[string]struct{}{}, scale: -1}
	if err := reportAccumulateMeasure(state, reportmodel.ReportDatasetMeasure{Operation: "count", SourceAlias: "missing"}, row, objects); err != nil || state.count != 0 {
		t.Fatalf("nil count state=%#v err=%v", state, err)
	}
	for _, measure := range []reportmodel.ReportDatasetMeasure{
		{Operation: "duration", StartField: field("missing"), EndField: field("end")},
		{Operation: "duration", StartField: field("start"), EndField: field("missing")},
	} {
		if err := reportAccumulateMeasure(state, measure, row, objects); err != nil {
			t.Fatal(err)
		}
	}
	for _, measure := range []reportmodel.ReportDatasetMeasure{
		{Operation: "duration", StartField: field("bad_start"), EndField: field("end")},
		{Operation: "duration", StartField: field("start"), EndField: field("bad_end")},
	} {
		if err := reportAccumulateMeasure(state, measure, row, objects); err == nil {
			t.Fatalf("expected duration parse error for %#v", measure)
		}
	}
	if err := reportAccumulateMeasure(state, reportmodel.ReportDatasetMeasure{Operation: "sum"}, row, objects); err == nil {
		t.Fatal("expected missing field error")
	}
	for _, key := range []string{"missing", "nil", "blank"} {
		if err := reportAccumulateMeasure(state, reportmodel.ReportDatasetMeasure{Operation: "sum", Field: field(key)}, row, objects); err != nil {
			t.Fatalf("empty %s: %v", key, err)
		}
	}
	if err := reportAccumulateMeasure(state, reportmodel.ReportDatasetMeasure{Operation: "distinct_count", Field: field("value")}, row, objects); err != nil {
		t.Fatal(err)
	}
	minimum := &reportMeasureState{distinct: map[string]struct{}{}, minimum: "10", hasExtremum: true}
	maximum := &reportMeasureState{distinct: map[string]struct{}{}, maximum: "20", hasExtremum: true}
	if err := reportAccumulateMeasure(minimum, reportmodel.ReportDatasetMeasure{Operation: "min", Field: field("high")}, row, objects); err != nil || minimum.minimum != "10" {
		t.Fatalf("minimum=%#v err=%v", minimum, err)
	}
	if err := reportAccumulateMeasure(minimum, reportmodel.ReportDatasetMeasure{Operation: "min", Field: field("low")}, row, objects); err != nil || minimum.minimum != "2" {
		t.Fatalf("lower minimum=%#v err=%v", minimum, err)
	}
	if err := reportAccumulateMeasure(maximum, reportmodel.ReportDatasetMeasure{Operation: "max", Field: field("low")}, row, objects); err != nil || maximum.maximum != "20" {
		t.Fatalf("maximum=%#v err=%v", maximum, err)
	}
	if err := reportAccumulateMeasure(state, reportmodel.ReportDatasetMeasure{Operation: "sum", Field: field("bad")}, row, objects); err == nil {
		t.Fatal("expected numeric error")
	}
}

func TestReportFinalizeGroupsAndMeasuresEdges(t *testing.T) {
	base := &reportGroupState{dimensions: map[string]string{}, measures: map[string]*reportMeasureState{}, privacyEntities: map[string]struct{}{}}
	if rows := reportFinalizeGroups(map[string]*reportGroupState{"hidden": base}, reportmodel.ReportDatasetSchema{Privacy: &reportmodel.ReportDatasetPrivacy{MinimumGroupSize: 1}}); len(rows) != 0 {
		t.Fatalf("privacy rows=%#v", rows)
	}
	dataset := reportmodel.ReportDatasetSchema{Measures: []reportmodel.ReportDatasetMeasure{
		{Key: "bad_numerator", Operation: "ratio", NumeratorKey: "missing", DenominatorKey: "one"},
		{Key: "bad_denominator", Operation: "ratio", NumeratorKey: "one", DenominatorKey: "missing"},
		{Key: "zero", Operation: "ratio", NumeratorKey: "one", DenominatorKey: "zero_base"},
		{Key: "one", Operation: "sum"}, {Key: "zero_base", Operation: "sum"},
	}}
	group := &reportGroupState{dimensions: map[string]string{}, measures: map[string]*reportMeasureState{
		"one": {sum: decimal.NewFromInt(1), scale: -1}, "zero_base": {sum: decimal.Zero, scale: -1},
	}, privacyEntities: map[string]struct{}{}}
	rows := reportFinalizeGroups(map[string]*reportGroupState{"": group}, dataset)
	if len(rows) != 1 || rows[0].Measures["bad_numerator"] != "" || rows[0].Measures["bad_denominator"] != "" || rows[0].Measures["zero"] != "" {
		t.Fatalf("rows=%#v", rows)
	}
	for _, operation := range []string{"count", "distinct_count", "sum", "duration"} {
		if got := reportFinalizeMeasure(nil, reportmodel.ReportDatasetMeasure{Operation: operation}); got != "0" {
			t.Fatalf("nil %s=%q", operation, got)
		}
	}
	if got := reportFinalizeMeasure(nil, reportmodel.ReportDatasetMeasure{Operation: "avg"}); got != "" {
		t.Fatalf("nil avg=%q", got)
	}
	if got := reportFinalizeMeasure(&reportMeasureState{}, reportmodel.ReportDatasetMeasure{Operation: "avg"}); got != "" {
		t.Fatalf("empty avg=%q", got)
	}
	if got := reportFinalizeMeasure(&reportMeasureState{}, reportmodel.ReportDatasetMeasure{Operation: "percentile"}); got != "" {
		t.Fatalf("empty percentile=%q", got)
	}
	percentile := &reportMeasureState{values: []decimal.Decimal{decimal.NewFromInt(2)}, scale: -1}
	if got := reportFinalizeMeasure(percentile, reportmodel.ReportDatasetMeasure{Operation: "percentile", Percentile: "0"}); got != "2" {
		t.Fatalf("zero percentile=%q", got)
	}
	if got := reportFinalizeMeasure(&reportMeasureState{}, reportmodel.ReportDatasetMeasure{Operation: "unknown"}); got != "" {
		t.Fatalf("unknown=%q", got)
	}
}

func TestReportSortAndComparisonEdges(t *testing.T) {
	equalRows := []reportmodel.ReportResultRow{
		{Dimensions: map[string]string{"same": "x"}},
		{Dimensions: map[string]string{"same": "x"}},
	}
	reportSortResultRows(equalRows, []reportmodel.ReportDatasetSort{{Key: "same", Direction: "asc"}})
	rows := []reportmodel.ReportResultRow{
		{Dimensions: map[string]string{"kind": "b", "same": "x"}, Measures: map[string]string{"amount": "1"}},
		{Dimensions: map[string]string{"kind": "a", "same": "x"}, Measures: map[string]string{"amount": "2"}},
	}
	reportSortResultRows(rows, nil)
	reportSortResultRows(rows, []reportmodel.ReportDatasetSort{{Key: "same", Direction: "asc"}, {Key: "amount", Direction: "desc"}})
	if rows[0].Measures["amount"] != "2" {
		t.Fatalf("descending rows=%#v", rows)
	}
	reportSortResultRows(rows, []reportmodel.ReportDatasetSort{{Key: "kind", Direction: "asc"}})
	if rows[0].Dimensions["kind"] != "a" {
		t.Fatalf("ascending rows=%#v", rows)
	}
	if err := reportApplyDatasetComparisons(rows, reportmodel.ReportDatasetSchema{}); err != nil {
		t.Fatal(err)
	}
	dimensions := []reportmodel.ReportDatasetDimension{{Key: "bucket", TimeGrain: ""}, {Key: "kind"}}
	comparisonRows := []reportmodel.ReportResultRow{
		{Dimensions: map[string]string{"bucket": "2026-01", "kind": "x"}, Measures: map[string]string{"value": "0", "invalid": "bad"}},
		{Dimensions: map[string]string{"bucket": "2026-02", "kind": "x"}, Measures: map[string]string{"value": "10", "invalid": "bad"}},
	}
	base := reportmodel.ReportDatasetSchema{Dimensions: dimensions, DefaultTimeGrain: "month"}
	for _, comparison := range []reportmodel.ReportDatasetComparison{
		{Key: "difference", TimeDimensionKey: "bucket", MeasureKey: "value", Operation: "difference"},
		{Key: "ratio", TimeDimensionKey: "bucket", MeasureKey: "value", Operation: "ratio"},
		{Key: "percent", TimeDimensionKey: "bucket", MeasureKey: "value", Operation: "percent_change"},
	} {
		dataset := base
		dataset.Comparisons = []reportmodel.ReportDatasetComparison{comparison}
		cloned := cloneReportResultRows(comparisonRows)
		if err := reportApplyDatasetComparisons(cloned, dataset); err != nil {
			t.Fatalf("comparison %#v: %v", comparison, err)
		}
	}
	for _, measures := range [][2]string{{"bad", "1"}, {"1", "bad"}} {
		invalidRows := cloneReportResultRows(comparisonRows)
		invalidRows[1].Measures["value"] = measures[0]
		invalidRows[0].Measures["value"] = measures[1]
		dataset := base
		dataset.Comparisons = []reportmodel.ReportDatasetComparison{{Key: "invalid", TimeDimensionKey: "bucket", MeasureKey: "value", Operation: "difference"}}
		if err := reportApplyDatasetComparisons(invalidRows, dataset); err != nil || invalidRows[1].Measures["invalid"] != "" {
			t.Fatalf("invalid rows=%#v err=%v", invalidRows, err)
		}
	}
	for _, operation := range []string{"ratio", "percent_change"} {
		dataset := base
		dataset.Comparisons = []reportmodel.ReportDatasetComparison{{Key: "zero", TimeDimensionKey: "bucket", MeasureKey: "value", Operation: operation}}
		zeroRows := cloneReportResultRows(comparisonRows)
		if err := reportApplyDatasetComparisons(zeroRows, dataset); err != nil || zeroRows[1].Measures["zero"] != "" {
			t.Fatalf("zero %s rows=%#v err=%v", operation, zeroRows, err)
		}
	}
	nonzeroRows := cloneReportResultRows(comparisonRows)
	nonzeroRows[0].Measures["value"] = "2"
	dataset := base
	dataset.Comparisons = []reportmodel.ReportDatasetComparison{{Key: "ratio", TimeDimensionKey: "bucket", MeasureKey: "value", Operation: "ratio"}}
	if err := reportApplyDatasetComparisons(nonzeroRows, dataset); err != nil || nonzeroRows[1].Measures["ratio"] != "5.000000" {
		t.Fatalf("nonzero ratio rows=%#v err=%v", nonzeroRows, err)
	}
	dataset = base
	dataset.Comparisons = []reportmodel.ReportDatasetComparison{{Key: "bad", TimeDimensionKey: "bucket", MeasureKey: "value", Operation: "unknown"}}
	if err := reportApplyDatasetComparisons(cloneReportResultRows(comparisonRows), dataset); err == nil {
		t.Fatal("expected unsupported comparison error")
	}
	dataset = reportmodel.ReportDatasetSchema{
		Dimensions:  []reportmodel.ReportDatasetDimension{{Key: "bucket", TimeGrain: "day"}},
		Comparisons: []reportmodel.ReportDatasetComparison{{Key: "bad", TimeDimensionKey: "bucket", MeasureKey: "value", Operation: "difference", OffsetPeriods: 2}},
	}
	if err := reportApplyDatasetComparisons([]reportmodel.ReportResultRow{{Dimensions: map[string]string{"bucket": "bad"}, Measures: map[string]string{"value": "1"}}}, dataset); err == nil {
		t.Fatal("expected comparison bucket error")
	}
}

func cloneReportResultRows(rows []reportmodel.ReportResultRow) []reportmodel.ReportResultRow {
	result := make([]reportmodel.ReportResultRow, len(rows))
	for index, row := range rows {
		result[index] = reportmodel.ReportResultRow{Dimensions: map[string]string{}, Measures: map[string]string{}}
		for key, value := range row.Dimensions {
			result[index].Dimensions[key] = value
		}
		for key, value := range row.Measures {
			result[index].Measures[key] = value
		}
	}
	return result
}

func TestReportAggregatePrimitiveEdges(t *testing.T) {
	timeCases := []struct {
		grain, value string
		periods      int
	}{
		{"minute", "2026-01-01T00:00", 1}, {"hour", "2026-01-01T00:00", 1},
		{"day", "2026-01-01", 1}, {"week", "2026-01-01", 1}, {"month", "2026-01", 1},
		{"quarter", "2026-Q4", 1}, {"year", "2026", 1},
	}
	for _, testCase := range timeCases {
		if _, err := reportShiftTimeBucket(testCase.value, testCase.grain, testCase.periods); err != nil {
			t.Fatalf("shift %#v: %v", testCase, err)
		}
	}
	for _, testCase := range []struct{ grain, value string }{
		{"minute", "bad"}, {"hour", "bad"}, {"day", "bad"}, {"month", "bad"},
		{"quarter", "bad"}, {"quarter", "2026-Q0"}, {"quarter", "2026-Q5"}, {"year", "bad"}, {"unknown", "bad"},
	} {
		if _, err := reportShiftTimeBucket(testCase.value, testCase.grain, 1); err == nil {
			t.Fatalf("expected shift error %#v", testCase)
		}
	}
	if got := reportMaximumDecimalScale("1", "2.345", "3.4"); got != 3 {
		t.Fatalf("scale=%d", got)
	}
	record := Record{ID: "id", CreatedAt: "created", UpdatedAt: "updated", Data: map[string]any{"field": "value"}}
	for _, field := range []string{"id", "created_at", "updated_at", "field"} {
		if _, ok := RecordValue(record, field); !ok {
			t.Fatalf("missing %s", field)
		}
	}
	empty := Record{Data: map[string]any{}}
	for _, field := range []string{"id", "created_at", "updated_at", "missing"} {
		if _, ok := RecordValue(empty, field); ok {
			t.Fatalf("unexpected %s", field)
		}
	}
	if _, ok := RecordPointerValue(nil, "id"); ok {
		t.Fatal("nil record found")
	}
	object := Object{Fields: []Field{{Key: " amount ", Type: "decimal"}}}
	if got := reportDatasetFieldSchema(object, "amount"); got.Type != "decimal" {
		t.Fatalf("field=%#v", got)
	}
	if got := reportDatasetFieldSchema(object, "missing"); got.Type != "text" {
		t.Fatalf("fallback=%#v", got)
	}
	for _, testCase := range []struct {
		field Field
		value any
		ok    bool
	}{
		{Field{Type: "currency"}, "1.23", true},
		{Field{Type: "decimal"}, "1.23", true},
		{Field{Type: "decimal"}, "bad", false},
		{Field{Type: "number"}, "1.5", true},
		{Field{Type: "number"}, "bad", false},
	} {
		_, _, err := reportNumericValue(testCase.field, testCase.value)
		if (err == nil) != testCase.ok {
			t.Fatalf("numeric %#v err=%v", testCase, err)
		}
	}
	if reportFormatDecimal(decimal.NewFromInt(1), 2) != "1.00" || reportFormatDecimal(decimal.NewFromInt(1), -1) != "1" {
		t.Fatal("decimal formatting mismatch")
	}
}

func TestReportDimensionAndTimeParsingEdges(t *testing.T) {
	location := time.FixedZone("test", 2*60*60)
	if got := dimensionValue(nil, "day", "", location); got != "" {
		t.Fatalf("nil=%q", got)
	}
	if got := dimensionValue("plain", "", "", location); got != "plain" {
		t.Fatalf("plain=%q", got)
	}
	if got := dimensionValue("bad", "", "day", location); got != "bad" {
		t.Fatalf("bad=%q", got)
	}
	for _, grain := range []string{"minute", "hour", "day", "week", "month", "quarter", "year", "unknown"} {
		if got := dimensionValue("2026-07-22T10:11:12Z", grain, "", location); got == "" {
			t.Fatalf("grain %s empty", grain)
		}
	}
	for _, value := range []string{"2026-07-22T10:11:12.123Z", "2026-07-22 10:11:12", "2026-07-22"} {
		if _, err := parseTime(value); err != nil {
			t.Fatalf("parse %q: %v", value, err)
		}
	}
	if _, err := parseTime("not-time"); err == nil {
		t.Fatal("expected parse error")
	}
}
