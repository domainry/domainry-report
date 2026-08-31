package engine

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

type reportTimedEvent struct {
	at    time.Time
	value string
}

func ExecuteAnalyses(rows []Row, dataset reportmodel.ReportDatasetSchema) ([]reportmodel.ReportAnalysisResult, error) {
	results := make([]reportmodel.ReportAnalysisResult, 0, len(dataset.Analyses))
	for _, analysis := range dataset.Analyses {
		var (
			rowsResult []reportmodel.ReportResultRow
			err        error
		)
		switch analysis.Type {
		case "funnel":
			rowsResult, err = reportExecuteFunnel(rows, analysis, dataset.Privacy)
		case "cohort_retention":
			rowsResult, err = reportExecuteCohortRetention(rows, analysis, dataset.Privacy)
		default:
			err = fmt.Errorf("unsupported analysis type %q", analysis.Type)
		}
		if err != nil {
			return nil, fmt.Errorf("analysis %s: %w", analysis.Key, err)
		}
		results = append(results, reportmodel.ReportAnalysisResult{Key: analysis.Key, Type: analysis.Type, Rows: rowsResult})
	}
	return results, nil
}

func reportExecuteFunnel(rows []Row, analysis reportmodel.ReportDatasetAnalysis, privacy *reportmodel.ReportDatasetPrivacy) ([]reportmodel.ReportResultRow, error) {
	events := map[string][]reportTimedEvent{}
	for _, row := range rows {
		entity, entityOK := FieldValue(row, analysis.EntityField)
		eventValue, eventOK := FieldValue(row, *analysis.EventField)
		timeValue, timeOK := FieldValue(row, analysis.TimeField)
		if !entityOK || !eventOK || !timeOK || stableValue(entity) == "" {
			continue
		}
		at, err := parseTime(timeValue)
		if err != nil {
			return nil, err
		}
		entityKey := stableValue(entity)
		events[entityKey] = append(events[entityKey], reportTimedEvent{at: at, value: stableValue(eventValue)})
	}
	counts := make([]int, len(analysis.Stages))
	for _, entityEvents := range events {
		sort.SliceStable(entityEvents, func(i, j int) bool { return entityEvents[i].at.Before(entityEvents[j].at) })
		nextEvent := 0
		firstMatch := time.Time{}
		for stageIndex, stage := range analysis.Stages {
			matched := false
			for nextEvent < len(entityEvents) {
				event := entityEvents[nextEvent]
				nextEvent++
				if !reportAnalysisValueIn(event.value, stage.Values) {
					continue
				}
				if firstMatch.IsZero() {
					firstMatch = event.at
				}
				if analysis.WindowSeconds > 0 && event.at.Sub(firstMatch) > time.Duration(analysis.WindowSeconds)*time.Second {
					break
				}
				matched = true
				break
			}
			if !matched {
				break
			}
			counts[stageIndex]++
		}
	}
	first := 0
	if len(counts) > 0 {
		first = counts[0]
	}
	result := []reportmodel.ReportResultRow{}
	for index, stage := range analysis.Stages {
		if privacy != nil && counts[index] < privacy.MinimumGroupSize {
			continue
		}
		conversion, previousConversion := "", ""
		if first > 0 {
			conversion = decimal.NewFromInt(int64(counts[index])).Div(decimal.NewFromInt(int64(first))).RoundBank(6).StringFixed(6)
		}
		if index == 0 {
			previousConversion = "1.000000"
		} else if counts[index-1] > 0 {
			previousConversion = decimal.NewFromInt(int64(counts[index])).Div(decimal.NewFromInt(int64(counts[index-1]))).RoundBank(6).StringFixed(6)
		}
		result = append(result, reportmodel.ReportResultRow{Dimensions: map[string]string{"stage": stage.Key}, Measures: map[string]string{"entities": strconv.Itoa(counts[index]), "conversion_rate": conversion, "previous_stage_rate": previousConversion}})
	}
	return result, nil
}

func reportExecuteCohortRetention(rows []Row, analysis reportmodel.ReportDatasetAnalysis, privacy *reportmodel.ReportDatasetPrivacy) ([]reportmodel.ReportResultRow, error) {
	entityTimes := map[string]map[int64]time.Time{}
	for _, row := range rows {
		entity, entityOK := FieldValue(row, analysis.EntityField)
		timeValue, timeOK := FieldValue(row, analysis.TimeField)
		if !entityOK || !timeOK || stableValue(entity) == "" {
			continue
		}
		at, err := parseTime(timeValue)
		if err != nil {
			return nil, err
		}
		entityKey := stableValue(entity)
		if entityTimes[entityKey] == nil {
			entityTimes[entityKey] = map[int64]time.Time{}
		}
		entityTimes[entityKey][at.UnixNano()] = at
	}
	cohortGrain, periodGrain := reportAnalysisGrains(analysis)
	maximumPeriods := analysis.MaximumPeriods
	if maximumPeriods == 0 {
		maximumPeriods = 12
	}
	cohortEntities := map[string]map[string]struct{}{}
	retained := map[string]map[int]map[string]struct{}{}
	for entity, uniqueTimes := range entityTimes {
		times := make([]time.Time, 0, len(uniqueTimes))
		for _, at := range uniqueTimes {
			times = append(times, at)
		}
		sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
		cohort := dimensionValue(times[0].Format(time.RFC3339Nano), cohortGrain, "", time.UTC)
		if cohortEntities[cohort] == nil {
			cohortEntities[cohort] = map[string]struct{}{}
		}
		cohortEntities[cohort][entity] = struct{}{}
		if retained[cohort] == nil {
			retained[cohort] = map[int]map[string]struct{}{}
		}
		for _, activity := range times {
			period := reportAnalysisPeriodDifference(times[0], activity, periodGrain)
			if period < 0 || period >= maximumPeriods {
				continue
			}
			if retained[cohort][period] == nil {
				retained[cohort][period] = map[string]struct{}{}
			}
			retained[cohort][period][entity] = struct{}{}
		}
	}
	cohorts := make([]string, 0, len(cohortEntities))
	for cohort := range cohortEntities {
		cohorts = append(cohorts, cohort)
	}
	sort.Strings(cohorts)
	result := []reportmodel.ReportResultRow{}
	for _, cohort := range cohorts {
		cohortSize := len(cohortEntities[cohort])
		if privacy != nil && cohortSize < privacy.MinimumGroupSize {
			continue
		}
		periods := make([]int, 0, len(retained[cohort]))
		for period := range retained[cohort] {
			periods = append(periods, period)
		}
		sort.Ints(periods)
		for _, period := range periods {
			count := len(retained[cohort][period])
			rate := decimal.NewFromInt(int64(count)).Div(decimal.NewFromInt(int64(cohortSize))).RoundBank(6).StringFixed(6)
			result = append(result, reportmodel.ReportResultRow{Dimensions: map[string]string{"cohort": cohort, "period_index": strconv.Itoa(period)}, Measures: map[string]string{"cohort_size": strconv.Itoa(cohortSize), "retained_entities": strconv.Itoa(count), "retention_rate": rate}})
		}
	}
	return result, nil
}

func reportAnalysisValueIn(actual string, expected []any) bool {
	for _, candidate := range expected {
		if actual == stableValue(candidate) {
			return true
		}
	}
	return false
}

func reportAnalysisGrains(analysis reportmodel.ReportDatasetAnalysis) (string, string) {
	cohortGrain := strings.TrimSpace(analysis.CohortGrain)
	if cohortGrain == "" {
		cohortGrain = "month"
	}
	periodGrain := strings.TrimSpace(analysis.PeriodGrain)
	if periodGrain == "" {
		periodGrain = cohortGrain
	}
	return cohortGrain, periodGrain
}

func reportAnalysisPeriodDifference(start, end time.Time, grain string) int {
	switch grain {
	case "day":
		startDay := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
		endDay := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
		return int(endDay.Sub(startDay) / (24 * time.Hour))
	case "week":
		return reportAnalysisPeriodDifference(start, end, "day") / 7
	case "month":
		return (end.Year()-start.Year())*12 + int(end.Month()-start.Month())
	case "quarter":
		return ((end.Year()-start.Year())*12 + int(end.Month()-start.Month())) / 3
	case "year":
		return end.Year() - start.Year()
	default:
		return -1
	}
}
