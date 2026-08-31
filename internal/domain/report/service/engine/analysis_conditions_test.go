package engine

import (
	"testing"
	"time"

	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

func TestReportFunnelCoversMissingFieldsTimeWindowEmptyStagesPrivacyAndZeroCounts(t *testing.T) {
	entity := reportmodel.ReportDatasetField{SourceAlias: "events", FieldKey: "entity"}
	event := reportmodel.ReportDatasetField{SourceAlias: "events", FieldKey: "event"}
	at := reportmodel.ReportDatasetField{SourceAlias: "events", FieldKey: "at"}
	analysis := reportmodel.ReportDatasetAnalysis{Key: "funnel", Type: "funnel", EntityField: entity, EventField: &event, TimeField: at, Stages: []reportmodel.ReportDatasetFunnelStage{{Key: "start", Values: []any{"start"}}, {Key: "finish", Values: []any{"finish"}}}}
	rows := []Row{
		{},
		reportAnalysisRow(map[string]any{"entity": "one"}),
		reportAnalysisRow(map[string]any{"entity": "one", "event": "start"}),
		reportAnalysisRow(map[string]any{"entity": "", "event": "start", "at": "2026-01-01T00:00:00Z"}),
	}
	if result, err := reportExecuteFunnel(rows, analysis, nil); err != nil || len(result) != 2 || result[0].Measures["conversion_rate"] != "" || result[1].Measures["previous_stage_rate"] != "" {
		t.Fatalf("missing result=%#v err=%v", result, err)
	}
	bad := append(rows, reportAnalysisRow(map[string]any{"entity": "bad", "event": "start", "at": "bad"}))
	if _, err := reportExecuteFunnel(bad, analysis, nil); err == nil {
		t.Fatal("bad funnel time accepted")
	}
	if result, err := reportExecuteFunnel(nil, reportmodel.ReportDatasetAnalysis{EventField: &event}, nil); err != nil || len(result) != 0 {
		t.Fatalf("empty stages=%#v err=%v", result, err)
	}
	analysis.WindowSeconds = 1
	windowRows := []Row{
		reportAnalysisRow(map[string]any{"entity": "one", "event": "start", "at": "2026-01-01T00:00:00Z"}),
		reportAnalysisRow(map[string]any{"entity": "one", "event": "other", "at": "2026-01-01T00:00:01Z"}),
		reportAnalysisRow(map[string]any{"entity": "one", "event": "finish", "at": "2026-01-01T00:00:02Z"}),
	}
	analysis.WindowSeconds = 0
	if result, err := reportExecuteFunnel(windowRows, analysis, &reportmodel.ReportDatasetPrivacy{MinimumGroupSize: 0}); err != nil || len(result) != 2 {
		t.Fatalf("unbounded funnel result=%#v err=%v", result, err)
	}
	privacy := &reportmodel.ReportDatasetPrivacy{MinimumGroupSize: 2}
	analysis.WindowSeconds = 1
	if result, err := reportExecuteFunnel(windowRows, analysis, privacy); err != nil || len(result) != 0 {
		t.Fatalf("private window result=%#v err=%v", result, err)
	}
}

func TestReportCohortCoversMissingFieldsDefaultsInvalidPeriodsBoundsPrivacyAndBadTime(t *testing.T) {
	entity := reportmodel.ReportDatasetField{SourceAlias: "events", FieldKey: "entity"}
	at := reportmodel.ReportDatasetField{SourceAlias: "events", FieldKey: "at"}
	analysis := reportmodel.ReportDatasetAnalysis{Key: "retention", Type: "cohort_retention", EntityField: entity, TimeField: at}
	rows := []Row{{}, reportAnalysisRow(map[string]any{"entity": "one"}), reportAnalysisRow(map[string]any{"entity": "", "at": "2026-01-01T00:00:00Z"})}
	if result, err := reportExecuteCohortRetention(rows, analysis, nil); err != nil || len(result) != 0 {
		t.Fatalf("missing cohort=%#v err=%v", result, err)
	}
	if _, err := reportExecuteCohortRetention([]Row{reportAnalysisRow(map[string]any{"entity": "bad", "at": "bad"})}, analysis, nil); err == nil {
		t.Fatal("bad cohort time accepted")
	}
	activity := []Row{
		reportAnalysisRow(map[string]any{"entity": "one", "at": "2026-01-01T00:00:00Z"}),
		reportAnalysisRow(map[string]any{"entity": "one", "at": "2027-02-01T00:00:00Z"}),
	}
	if result, err := reportExecuteCohortRetention(activity, analysis, &reportmodel.ReportDatasetPrivacy{MinimumGroupSize: 1}); err != nil || len(result) == 0 {
		t.Fatalf("visible cohort=%#v err=%v", result, err)
	}
	if result, err := reportExecuteCohortRetention(activity, analysis, &reportmodel.ReportDatasetPrivacy{MinimumGroupSize: 2}); err != nil || len(result) != 0 {
		t.Fatalf("private cohort=%#v err=%v", result, err)
	}
	analysis.PeriodGrain, analysis.MaximumPeriods = "invalid", 2
	if result, err := reportExecuteCohortRetention(activity, analysis, nil); err != nil || len(result) != 0 {
		t.Fatalf("invalid period result=%#v err=%v", result, err)
	}
}

func TestReportAnalysisHelpersCoverAllGrainsAndUnsupportedAnalysis(t *testing.T) {
	if _, err := ExecuteAnalyses(nil, reportmodel.ReportDatasetSchema{Analyses: []reportmodel.ReportDatasetAnalysis{{Key: "bad", Type: "unknown"}}}); err == nil {
		t.Fatal("unsupported analysis accepted")
	}
	if !reportAnalysisValueIn("one", []any{"zero", "one"}) || reportAnalysisValueIn("missing", []any{"one"}) {
		t.Fatal("analysis membership mismatch")
	}
	if cohort, period := reportAnalysisGrains(reportmodel.ReportDatasetAnalysis{}); cohort != "month" || period != "month" {
		t.Fatalf("default grains=%q,%q", cohort, period)
	}
	if cohort, period := reportAnalysisGrains(reportmodel.ReportDatasetAnalysis{CohortGrain: "week", PeriodGrain: "day"}); cohort != "week" || period != "day" {
		t.Fatalf("explicit grains=%q,%q", cohort, period)
	}
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	for _, grain := range []string{"day", "week", "month", "quarter", "year"} {
		if reportAnalysisPeriodDifference(start, end, grain) < 0 {
			t.Fatalf("grain %s rejected", grain)
		}
	}
	if reportAnalysisPeriodDifference(start, end, "bad") != -1 {
		t.Fatal("unsupported grain accepted")
	}
}

func reportAnalysisRow(data map[string]any) Row {
	record := &Record{ID: "event", Data: data}
	return Row{"events": record}
}
