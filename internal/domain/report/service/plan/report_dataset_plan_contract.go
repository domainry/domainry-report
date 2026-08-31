package plan

import (
	"fmt"
	"sort"
	"strings"

	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

// BuildReportDatasetPlan normalizes the alias graph and proves that declared
// measures cannot be multiplied by an unrelated or downstream one-to-many
// join. Unsafe plans are rejected instead of relying on DISTINCT or silently
// returning financially incorrect totals.
func BuildReportDatasetPlan(report reportmodel.ReportSchema) (reportmodel.ReportDatasetPlan, error) {
	dataset := report.Dataset
	if policy := report.Materialization; policy != nil {
		if policy.MaximumLagSeconds <= 0 || policy.MaximumLagSeconds > 31536000 || policy.ConsistencyRetries < 0 || policy.ConsistencyRetries > 10 {
			return reportmodel.ReportDatasetPlan{}, reportPlanError("backend.report.materialization_invalid", "materialization", map[string]string{"maximum_lag_seconds": fmt.Sprint(policy.MaximumLagSeconds), "consistency_retries": fmt.Sprint(policy.ConsistencyRetries)})
		}
	}
	rootAlias := strings.TrimSpace(dataset.Source.Alias)
	rootObject := strings.TrimSpace(dataset.Source.ObjectKey)
	if rootAlias == "" || rootObject == "" {
		return reportmodel.ReportDatasetPlan{}, reportPlanError("backend.report.dataset_source_invalid", "dataset.source", map[string]string{"report": report.Key})
	}

	plan := reportmodel.ReportDatasetPlan{
		ReportKey:      strings.TrimSpace(report.Key),
		Dataset:        dataset,
		AliasObjects:   map[string]string{rootAlias: rootObject},
		AliasParents:   map[string]string{},
		MeasureSources: map[string][]string{},
	}
	oneToMany := []string{}
	for index, join := range dataset.Joins {
		path := fmt.Sprintf("dataset.joins[%d]", index)
		alias := strings.TrimSpace(join.Alias)
		leftAlias := strings.TrimSpace(join.LeftAlias)
		objectKey := strings.TrimSpace(join.ObjectKey)
		if alias == "" || objectKey == "" || plan.AliasObjects[alias] != "" {
			return reportmodel.ReportDatasetPlan{}, reportPlanError("backend.report.dataset_alias_invalid", path+".alias", map[string]string{"alias": alias})
		}
		if plan.AliasObjects[leftAlias] == "" {
			return reportmodel.ReportDatasetPlan{}, reportPlanError("backend.report.join_invalid", path+".left_alias", map[string]string{"alias": leftAlias})
		}
		if join.Type != "inner" && join.Type != "left" {
			return reportmodel.ReportDatasetPlan{}, reportPlanError("backend.report.join_invalid", path+".type", map[string]string{"actual": join.Type})
		}
		equalities := join.Equalities()
		if len(join.FieldEqualities) > 0 && (strings.TrimSpace(join.LeftField) != "" || strings.TrimSpace(join.RightField) != "") || len(equalities) == 0 || len(equalities) > 8 {
			return reportmodel.ReportDatasetPlan{}, reportPlanError("backend.report.join_invalid", path, map[string]string{"reason": "declare either one legacy field pair or 1..8 field_equalities"})
		}
		seenEqualities := map[string]bool{}
		for _, equality := range equalities {
			leftField, rightField := strings.TrimSpace(equality.LeftField), strings.TrimSpace(equality.RightField)
			pair := leftField + "\x00" + rightField
			if leftField == "" || rightField == "" || seenEqualities[pair] {
				return reportmodel.ReportDatasetPlan{}, reportPlanError("backend.report.join_invalid", path+".field_equalities", map[string]string{"reason": "field equality must be complete and unique"})
			}
			seenEqualities[pair] = true
		}
		cardinality := strings.TrimSpace(join.Cardinality)
		switch cardinality {
		case "one_to_one", "many_to_one":
		case "one_to_many":
			if !join.RuntimeScopeOnly {
				oneToMany = append(oneToMany, alias)
			}
		default:
			return reportmodel.ReportDatasetPlan{}, reportPlanError("backend.report.join_cardinality_invalid", path+".cardinality", map[string]string{"actual": cardinality})
		}
		plan.AliasObjects[alias] = objectKey
		plan.AliasParents[alias] = leftAlias
	}

	measureByKey := make(map[string]reportmodel.ReportDatasetMeasure, len(dataset.Measures))
	for _, measure := range dataset.Measures {
		measureByKey[strings.TrimSpace(measure.Key)] = measure
	}
	for index, measure := range dataset.Measures {
		key := strings.TrimSpace(measure.Key)
		sources, err := reportMeasureSources(rootAlias, measure, measureByKey, map[string]bool{})
		if err != nil {
			return reportmodel.ReportDatasetPlan{}, reportPlanError("backend.report.measure_invalid", fmt.Sprintf("dataset.measures[%d]", index), map[string]string{"measure": key, "reason": err.Error()})
		}
		for _, source := range sources {
			if plan.AliasObjects[source] == "" {
				return reportmodel.ReportDatasetPlan{}, reportPlanError("backend.report.field_reference_invalid", fmt.Sprintf("dataset.measures[%d]", index), map[string]string{"measure": key, "alias": source})
			}
		}
		plan.MeasureSources[key] = sources
		if reportMeasureDuplicateInsensitive(measure.Operation) {
			continue
		}
		for _, multiplyingAlias := range oneToMany {
			for _, source := range sources {
				if !reportAliasDescendsFrom(source, multiplyingAlias, plan.AliasParents) {
					return reportmodel.ReportDatasetPlan{}, reportPlanError("backend.report.join_measure_amplification", fmt.Sprintf("dataset.measures[%d]", index), map[string]string{"measure": key, "source_alias": source, "join_alias": multiplyingAlias})
				}
			}
		}
	}
	dimensions := map[string]reportmodel.ReportDatasetDimension{}
	for _, dimension := range dataset.Dimensions {
		dimensions[strings.TrimSpace(dimension.Key)] = dimension
	}
	for index, comparison := range dataset.Comparisons {
		path := fmt.Sprintf("dataset.comparisons[%d]", index)
		dimension, dimensionOK := dimensions[strings.TrimSpace(comparison.TimeDimensionKey)]
		_, measureOK := measureByKey[strings.TrimSpace(comparison.MeasureKey)]
		grain := strings.TrimSpace(dimension.TimeGrain)
		if grain == "" {
			grain = strings.TrimSpace(dataset.DefaultTimeGrain)
		}
		if comparison.Type != "period_over_period" || !dimensionOK || !measureOK || !reportPlanTimeGrain(grain) || strings.TrimSpace(comparison.Key) == "" {
			return reportmodel.ReportDatasetPlan{}, reportPlanError("backend.report.comparison_invalid", path, map[string]string{"key": comparison.Key})
		}
		if comparison.Operation != "difference" && comparison.Operation != "ratio" && comparison.Operation != "percent_change" || comparison.OffsetPeriods < 0 || comparison.OffsetPeriods > 100 {
			return reportmodel.ReportDatasetPlan{}, reportPlanError("backend.report.comparison_invalid", path, map[string]string{"operation": comparison.Operation})
		}
	}
	analysisKeys := map[string]bool{}
	for index, analysis := range dataset.Analyses {
		path := fmt.Sprintf("dataset.analyses[%d]", index)
		key := strings.TrimSpace(analysis.Key)
		if key == "" || analysisKeys[key] || plan.AliasObjects[strings.TrimSpace(analysis.EntityField.SourceAlias)] == "" || plan.AliasObjects[strings.TrimSpace(analysis.TimeField.SourceAlias)] == "" {
			return reportmodel.ReportDatasetPlan{}, reportPlanError("backend.report.analysis_invalid", path, map[string]string{"key": key})
		}
		analysisKeys[key] = true
		switch analysis.Type {
		case "funnel":
			if analysis.EventField == nil || plan.AliasObjects[strings.TrimSpace(analysis.EventField.SourceAlias)] == "" || len(analysis.Stages) < 2 || analysis.WindowSeconds < 0 || analysis.WindowSeconds > 31536000 {
				return reportmodel.ReportDatasetPlan{}, reportPlanError("backend.report.analysis_invalid", path, map[string]string{"type": analysis.Type})
			}
			stageKeys := map[string]bool{}
			for _, stage := range analysis.Stages {
				stageKey := strings.TrimSpace(stage.Key)
				if stageKey == "" || stageKeys[stageKey] || len(stage.Values) == 0 {
					return reportmodel.ReportDatasetPlan{}, reportPlanError("backend.report.analysis_invalid", path+".stages", map[string]string{"stage": stageKey})
				}
				stageKeys[stageKey] = true
			}
		case "cohort_retention":
			cohortGrain, periodGrain := reportPlanAnalysisGrains(analysis)
			if !reportPlanCohortGrain(cohortGrain) || !reportPlanCohortGrain(periodGrain) || analysis.MaximumPeriods < 0 || analysis.MaximumPeriods > 120 || analysis.EventField != nil || len(analysis.Stages) != 0 {
				return reportmodel.ReportDatasetPlan{}, reportPlanError("backend.report.analysis_invalid", path, map[string]string{"type": analysis.Type})
			}
		default:
			return reportmodel.ReportDatasetPlan{}, reportPlanError("backend.report.analysis_invalid", path+".type", map[string]string{"type": analysis.Type})
		}
	}
	return plan, nil
}

func reportPlanAnalysisGrains(analysis reportmodel.ReportDatasetAnalysis) (string, string) {
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

func reportPlanCohortGrain(value string) bool {
	switch value {
	case "day", "week", "month", "quarter", "year":
		return true
	default:
		return false
	}
}

func reportPlanTimeGrain(value string) bool {
	switch strings.TrimSpace(value) {
	case "minute", "hour", "day", "week", "month", "quarter", "year":
		return true
	default:
		return false
	}
}

func reportMeasureSources(rootAlias string, measure reportmodel.ReportDatasetMeasure, measures map[string]reportmodel.ReportDatasetMeasure, visiting map[string]bool) ([]string, error) {
	key := strings.TrimSpace(measure.Key)
	if visiting[key] {
		return nil, fmt.Errorf("cyclic ratio dependency")
	}
	visiting[key] = true
	defer delete(visiting, key)

	sources := map[string]bool{}
	// Callers validate optional field pointers before adding them, so keep this
	// helper value-based and leave nil handling at the operation contract.
	add := func(field reportmodel.ReportDatasetField) {
		if strings.TrimSpace(field.SourceAlias) != "" {
			sources[strings.TrimSpace(field.SourceAlias)] = true
		}
	}
	switch strings.TrimSpace(measure.Operation) {
	case "count":
		sourceAlias := strings.TrimSpace(measure.SourceAlias)
		if sourceAlias == "" {
			sourceAlias = rootAlias
		}
		sources[sourceAlias] = true
	case "ratio":
		for _, dependencyKey := range []string{strings.TrimSpace(measure.NumeratorKey), strings.TrimSpace(measure.DenominatorKey)} {
			dependency, ok := measures[dependencyKey]
			if !ok || dependencyKey == key {
				return nil, fmt.Errorf("unknown ratio dependency %q", dependencyKey)
			}
			dependencySources, err := reportMeasureSources(rootAlias, dependency, measures, visiting)
			if err != nil {
				return nil, err
			}
			for _, source := range dependencySources {
				sources[source] = true
			}
		}
	case "duration":
		if measure.StartField == nil || measure.EndField == nil {
			return nil, fmt.Errorf("duration requires start_field and end_field")
		}
		add(*measure.StartField)
		add(*measure.EndField)
	case "distinct_count", "sum", "avg", "min", "max", "percentile":
		if measure.Field == nil {
			return nil, fmt.Errorf("operation %s requires field", measure.Operation)
		}
		add(*measure.Field)
	default:
		return nil, fmt.Errorf("unsupported operation %q", measure.Operation)
	}
	result := make([]string, 0, len(sources))
	for source := range sources {
		result = append(result, source)
	}
	sort.Strings(result)
	return result, nil
}

func reportMeasureDuplicateInsensitive(operation string) bool {
	switch strings.TrimSpace(operation) {
	case "distinct_count", "min", "max":
		return true
	default:
		return false
	}
}

func reportAliasDescendsFrom(alias, ancestor string, parents map[string]string) bool {
	for current := strings.TrimSpace(alias); current != ""; current = parents[current] {
		if current == ancestor {
			return true
		}
	}
	return false
}

func reportPlanError(code, path string, params map[string]string) error {
	return &reportmodel.ReportDatasetPlanError{Code: code, Path: path, Params: params}
}
