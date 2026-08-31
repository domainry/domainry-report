package plan

import (
	"testing"

	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

func reportPlanValidReport() reportmodel.ReportSchema {
	return reportmodel.ReportSchema{Key: "report", Dataset: reportmodel.ReportDatasetSchema{
		Source:   reportmodel.ReportDatasetSource{ObjectKey: "event", Alias: "events"},
		Measures: []reportmodel.ReportDatasetMeasure{{Key: "count", Operation: "count"}},
	}}
}

func requireReportPlanError(t *testing.T, report reportmodel.ReportSchema) {
	t.Helper()
	if _, err := BuildReportDatasetPlan(report); err == nil {
		t.Fatalf("expected plan error for %#v", report)
	}
}

func TestReportDatasetPlanMaterializationSourceAndJoinEdges(t *testing.T) {
	valid := reportPlanValidReport()
	valid.Materialization = &reportmodel.ReportMaterializationPolicy{MaximumLagSeconds: 1, ConsistencyRetries: 0}
	if _, err := BuildReportDatasetPlan(valid); err != nil {
		t.Fatal(err)
	}
	for _, policy := range []reportmodel.ReportMaterializationPolicy{
		{MaximumLagSeconds: 0}, {MaximumLagSeconds: 31536001},
		{MaximumLagSeconds: 1, ConsistencyRetries: -1}, {MaximumLagSeconds: 1, ConsistencyRetries: 11},
	} {
		report := reportPlanValidReport()
		report.Materialization = &policy
		requireReportPlanError(t, report)
	}
	for _, source := range []reportmodel.ReportDatasetSource{{Alias: "events"}, {ObjectKey: "event"}} {
		report := reportPlanValidReport()
		report.Dataset.Source = source
		requireReportPlanError(t, report)
	}
	joinBase := reportmodel.ReportDatasetJoin{Alias: "details", ObjectKey: "detail", Type: "inner", LeftAlias: "events", LeftField: "id", RightField: "event_id", Cardinality: "one_to_one"}
	for _, cardinality := range []string{"one_to_one", "many_to_one", "one_to_many"} {
		report := reportPlanValidReport()
		join := joinBase
		join.Cardinality = cardinality
		report.Dataset.Joins = []reportmodel.ReportDatasetJoin{join}
		if cardinality == "one_to_many" {
			report.Dataset.Measures[0].SourceAlias = "details"
		}
		if _, err := BuildReportDatasetPlan(report); err != nil {
			t.Fatalf("cardinality %s: %v", cardinality, err)
		}
	}
	left := reportPlanValidReport()
	join := joinBase
	join.Type = "left"
	left.Dataset.Joins = []reportmodel.ReportDatasetJoin{join}
	if _, err := BuildReportDatasetPlan(left); err != nil {
		t.Fatal(err)
	}
	compound := reportPlanValidReport()
	compoundJoin := joinBase
	compoundJoin.LeftField, compoundJoin.RightField = "", ""
	compoundJoin.FieldEqualities = []reportmodel.ReportDatasetJoinFieldEquality{{LeftField: "id", RightField: "event_id"}, {LeftField: "version_counter", RightField: "version_no"}}
	compound.Dataset.Joins = []reportmodel.ReportDatasetJoin{compoundJoin}
	if _, err := BuildReportDatasetPlan(compound); err != nil {
		t.Fatalf("compound join: %v", err)
	}
	for _, mutate := range []func(*reportmodel.ReportDatasetJoin){
		func(v *reportmodel.ReportDatasetJoin) { v.LeftField, v.RightField = "id", "event_id" },
		func(v *reportmodel.ReportDatasetJoin) { v.FieldEqualities[0].RightField = "" },
		func(v *reportmodel.ReportDatasetJoin) {
			v.FieldEqualities = append(v.FieldEqualities, v.FieldEqualities[0])
		},
		func(v *reportmodel.ReportDatasetJoin) {
			v.FieldEqualities = make([]reportmodel.ReportDatasetJoinFieldEquality, 9)
		},
	} {
		report := reportPlanValidReport()
		invalid := compoundJoin
		invalid.FieldEqualities = append([]reportmodel.ReportDatasetJoinFieldEquality(nil), compoundJoin.FieldEqualities...)
		mutate(&invalid)
		report.Dataset.Joins = []reportmodel.ReportDatasetJoin{invalid}
		requireReportPlanError(t, report)
	}
	for _, mutate := range []func(*reportmodel.ReportDatasetJoin){
		func(v *reportmodel.ReportDatasetJoin) { v.Alias = "" },
		func(v *reportmodel.ReportDatasetJoin) { v.ObjectKey = "" },
		func(v *reportmodel.ReportDatasetJoin) { v.RightField = "" },
	} {
		report := reportPlanValidReport()
		join := joinBase
		mutate(&join)
		report.Dataset.Joins = []reportmodel.ReportDatasetJoin{join}
		requireReportPlanError(t, report)
	}
	report := reportPlanValidReport()
	report.Dataset.Measures = []reportmodel.ReportDatasetMeasure{{Key: "bad", Operation: "sum", Field: reportPlanField("missing", "amount")}}
	requireReportPlanError(t, report)
}

func TestReportDatasetPlanComparisonEdges(t *testing.T) {
	base := reportPlanValidReport()
	base.Dataset.Dimensions = []reportmodel.ReportDatasetDimension{{Key: "bucket", Field: *reportPlanField("events", "created_at"), TimeGrain: "day"}}
	valid := reportmodel.ReportDatasetComparison{Key: "change", Type: "period_over_period", TimeDimensionKey: "bucket", MeasureKey: "count", Operation: "difference", OffsetPeriods: 0}
	for _, operation := range []string{"difference", "ratio", "percent_change"} {
		report := base
		comparison := valid
		comparison.Operation = operation
		report.Dataset.Comparisons = []reportmodel.ReportDatasetComparison{comparison}
		if _, err := BuildReportDatasetPlan(report); err != nil {
			t.Fatalf("operation %s: %v", operation, err)
		}
	}
	defaultGrain := base
	defaultGrain.Dataset.Dimensions = append([]reportmodel.ReportDatasetDimension(nil), base.Dataset.Dimensions...)
	defaultGrain.Dataset.Dimensions[0].TimeGrain = ""
	defaultGrain.Dataset.DefaultTimeGrain = "month"
	defaultGrain.Dataset.Comparisons = []reportmodel.ReportDatasetComparison{valid}
	if _, err := BuildReportDatasetPlan(defaultGrain); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*reportmodel.ReportDatasetComparison, *reportmodel.ReportSchema){
		func(v *reportmodel.ReportDatasetComparison, _ *reportmodel.ReportSchema) { v.Type = "invalid" },
		func(v *reportmodel.ReportDatasetComparison, _ *reportmodel.ReportSchema) {
			v.TimeDimensionKey = "missing"
		},
		func(v *reportmodel.ReportDatasetComparison, _ *reportmodel.ReportSchema) { v.MeasureKey = "missing" },
		func(_ *reportmodel.ReportDatasetComparison, r *reportmodel.ReportSchema) {
			r.Dataset.Dimensions[0].TimeGrain = "invalid"
		},
		func(v *reportmodel.ReportDatasetComparison, _ *reportmodel.ReportSchema) { v.Key = "" },
		func(v *reportmodel.ReportDatasetComparison, _ *reportmodel.ReportSchema) { v.Operation = "invalid" },
		func(v *reportmodel.ReportDatasetComparison, _ *reportmodel.ReportSchema) { v.OffsetPeriods = -1 },
		func(v *reportmodel.ReportDatasetComparison, _ *reportmodel.ReportSchema) { v.OffsetPeriods = 101 },
	} {
		report := base
		report.Dataset.Dimensions = append([]reportmodel.ReportDatasetDimension(nil), base.Dataset.Dimensions...)
		comparison := valid
		mutate(&comparison, &report)
		report.Dataset.Comparisons = []reportmodel.ReportDatasetComparison{comparison}
		requireReportPlanError(t, report)
	}
}

func reportPlanValidFunnel() reportmodel.ReportDatasetAnalysis {
	return reportmodel.ReportDatasetAnalysis{
		Key: "funnel", Type: "funnel", EntityField: *reportPlanField("events", "person"), EventField: reportPlanField("events", "kind"),
		TimeField: *reportPlanField("events", "created_at"), WindowSeconds: 0,
		Stages: []reportmodel.ReportDatasetFunnelStage{{Key: "first", Values: []any{"first"}}, {Key: "second", Values: []any{"second"}}},
	}
}

func TestReportDatasetPlanAnalysisEdges(t *testing.T) {
	base := reportPlanValidReport()
	funnel := reportPlanValidFunnel()
	base.Dataset.Analyses = []reportmodel.ReportDatasetAnalysis{funnel}
	if _, err := BuildReportDatasetPlan(base); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*reportmodel.ReportDatasetAnalysis){
		func(v *reportmodel.ReportDatasetAnalysis) { v.Key = "" },
		func(v *reportmodel.ReportDatasetAnalysis) { v.EntityField.SourceAlias = "missing" },
		func(v *reportmodel.ReportDatasetAnalysis) { v.TimeField.SourceAlias = "missing" },
		func(v *reportmodel.ReportDatasetAnalysis) { v.EventField = nil },
		func(v *reportmodel.ReportDatasetAnalysis) { v.EventField.SourceAlias = "missing" },
		func(v *reportmodel.ReportDatasetAnalysis) { v.Stages = v.Stages[:1] },
		func(v *reportmodel.ReportDatasetAnalysis) { v.WindowSeconds = -1 },
		func(v *reportmodel.ReportDatasetAnalysis) { v.WindowSeconds = 31536001 },
		func(v *reportmodel.ReportDatasetAnalysis) { v.Stages[0].Key = "" },
		func(v *reportmodel.ReportDatasetAnalysis) { v.Stages[1].Key = v.Stages[0].Key },
		func(v *reportmodel.ReportDatasetAnalysis) { v.Stages[0].Values = nil },
	} {
		report := reportPlanValidReport()
		candidate := reportPlanValidFunnel()
		candidate.Stages = append([]reportmodel.ReportDatasetFunnelStage(nil), candidate.Stages...)
		mutate(&candidate)
		report.Dataset.Analyses = []reportmodel.ReportDatasetAnalysis{candidate}
		requireReportPlanError(t, report)
	}
	duplicate := reportPlanValidReport()
	duplicate.Dataset.Analyses = []reportmodel.ReportDatasetAnalysis{reportPlanValidFunnel(), reportPlanValidFunnel()}
	requireReportPlanError(t, duplicate)
	cohort := reportmodel.ReportDatasetAnalysis{Key: "cohort", Type: "cohort_retention", EntityField: *reportPlanField("events", "person"), TimeField: *reportPlanField("events", "created_at")}
	for _, grains := range [][2]string{{"", ""}, {"day", "week"}, {"quarter", "year"}} {
		report := reportPlanValidReport()
		candidate := cohort
		candidate.CohortGrain, candidate.PeriodGrain = grains[0], grains[1]
		report.Dataset.Analyses = []reportmodel.ReportDatasetAnalysis{candidate}
		if _, err := BuildReportDatasetPlan(report); err != nil {
			t.Fatalf("cohort grains=%v err=%v", grains, err)
		}
	}
	for _, mutate := range []func(*reportmodel.ReportDatasetAnalysis){
		func(v *reportmodel.ReportDatasetAnalysis) { v.CohortGrain = "hour" },
		func(v *reportmodel.ReportDatasetAnalysis) { v.PeriodGrain = "hour" },
		func(v *reportmodel.ReportDatasetAnalysis) { v.MaximumPeriods = -1 },
		func(v *reportmodel.ReportDatasetAnalysis) { v.MaximumPeriods = 121 },
		func(v *reportmodel.ReportDatasetAnalysis) { v.EventField = reportPlanField("events", "kind") },
		func(v *reportmodel.ReportDatasetAnalysis) {
			v.Stages = []reportmodel.ReportDatasetFunnelStage{{Key: "x", Values: []any{"x"}}}
		},
	} {
		report := reportPlanValidReport()
		candidate := cohort
		candidate.CohortGrain, candidate.PeriodGrain = "month", "month"
		mutate(&candidate)
		report.Dataset.Analyses = []reportmodel.ReportDatasetAnalysis{candidate}
		requireReportPlanError(t, report)
	}
	unknown := reportPlanValidReport()
	analysis := cohort
	analysis.Type = "unknown"
	unknown.Dataset.Analyses = []reportmodel.ReportDatasetAnalysis{analysis}
	requireReportPlanError(t, unknown)
}

func TestReportDatasetPlanMeasureAndPrimitiveEdges(t *testing.T) {
	field := reportPlanField("events", "value")
	measures := map[string]reportmodel.ReportDatasetMeasure{
		"count": {Key: "count", Operation: "count"},
		"sum":   {Key: "sum", Operation: "sum", Field: field},
		"ratio": {Key: "ratio", Operation: "ratio", NumeratorKey: "sum", DenominatorKey: "count"},
	}
	if sources, err := reportMeasureSources("events", measures["ratio"], measures, map[string]bool{}); err != nil || len(sources) != 1 || sources[0] != "events" {
		t.Fatalf("ratio sources=%#v err=%v", sources, err)
	}
	self := reportmodel.ReportDatasetMeasure{Key: "self", Operation: "ratio", NumeratorKey: "self", DenominatorKey: "count"}
	selfMeasures := map[string]reportmodel.ReportDatasetMeasure{"self": self, "count": measures["count"]}
	if _, err := reportMeasureSources("events", self, selfMeasures, map[string]bool{}); err == nil {
		t.Fatal("self ratio dependency accepted")
	}
	for _, measure := range []reportmodel.ReportDatasetMeasure{
		{Key: "missing", Operation: "ratio", NumeratorKey: "unknown", DenominatorKey: "count"},
		{Key: "duration", Operation: "duration", EndField: field},
		{Key: "duration", Operation: "duration", StartField: field},
		{Key: "sum", Operation: "sum"},
		{Key: "bad", Operation: "bad"},
	} {
		if _, err := reportMeasureSources("events", measure, measures, map[string]bool{}); err == nil {
			t.Fatalf("invalid measure accepted: %#v", measure)
		}
	}
	for _, operation := range []string{"distinct_count", "sum", "avg", "min", "max", "percentile"} {
		measure := reportmodel.ReportDatasetMeasure{Key: operation, Operation: operation, Field: field}
		if _, err := reportMeasureSources("events", measure, measures, map[string]bool{}); err != nil {
			t.Fatalf("operation %s: %v", operation, err)
		}
	}
	if sources, err := reportMeasureSources("events", reportmodel.ReportDatasetMeasure{Key: "count", Operation: "count", SourceAlias: "details"}, measures, map[string]bool{}); err != nil || sources[0] != "details" {
		t.Fatalf("explicit count=%#v err=%v", sources, err)
	}
	emptyAlias := reportmodel.ReportDatasetField{}
	if sources, err := reportMeasureSources("events", reportmodel.ReportDatasetMeasure{Key: "sum", Operation: "sum", Field: &emptyAlias}, measures, map[string]bool{}); err != nil || len(sources) != 0 {
		t.Fatalf("empty alias=%#v err=%v", sources, err)
	}
	if sources, err := reportMeasureSources("events", reportmodel.ReportDatasetMeasure{Key: "duration", Operation: "duration", StartField: field, EndField: reportPlanField("details", "end")}, measures, map[string]bool{}); err != nil || len(sources) != 2 {
		t.Fatalf("duration=%#v err=%v", sources, err)
	}
	for _, grain := range []string{"day", "week", "month", "quarter", "year", "invalid"} {
		_ = reportPlanCohortGrain(grain)
	}
	for _, grain := range []string{"minute", "hour", "day", "week", "month", "quarter", "year", "invalid"} {
		_ = reportPlanTimeGrain(grain)
	}
	for _, operation := range []string{"distinct_count", "min", "max", "sum"} {
		_ = reportMeasureDuplicateInsensitive(operation)
	}
	parents := map[string]string{"details": "events"}
	if !reportAliasDescendsFrom("details", "events", parents) || reportAliasDescendsFrom("events", "details", parents) {
		t.Fatal("alias ancestry mismatch")
	}
	if reportPlanError("code", "path", nil) == nil {
		t.Fatal("nil plan error")
	}
}
