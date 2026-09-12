package analysis

import (
	"sort"
	"time"

	model "github.com/domainry/domainry-report-sdk/model"
)

func trendRows(plan Plan, rows []model.AnalysisRow) error {
	groupKeys := plan.GroupKeys[:len(plan.GroupKeys)-1]
	periods := map[string]time.Time{}
	location, _ := time.LoadLocation(plan.Spec.Time.TimeZone)
	for _, row := range rows {
		if value := row.Values["period"]; value != nil {
			stamp, err := parsePeriod(*value, location)
			if err != nil {
				return err
			}
			periods[*value] = stamp
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := groupKey(rows[i], groupKeys), groupKey(rows[j], groupKeys)
		if a != b {
			return a < b
		}
		x, y := rows[i].Values["period"], rows[j].Values["period"]
		if x == nil {
			return false
		}
		if y == nil {
			return true
		}
		return periods[*x].Before(periods[*y])
	})
	previous := map[string]model.AnalysisRow{}
	for i := range rows {
		row := &rows[i]
		key := groupKey(*row, groupKeys)
		prior, found := previous[key]
		period := row.Values["period"]
		if period == nil {
			found = false
			row.Issues = append(row.Issues, model.AnalysisCellIssue{Column: "period", Code: "missing_time"})
		}
		for _, measure := range plan.MeasureKeys {
			value := (*string)(nil)
			if found {
				value = prior.Values[measure]
			}
			row.Values["previous_"+measure] = value
			if err := changes(row, measure, value, row.Values[measure]); err != nil {
				return err
			}
		}
		if period != nil {
			if found && !periods[*period].Equal(nextPeriod(periods[*prior.Values["period"]].In(location), plan.Spec.Time.Grain)) {
				row.Issues = append(row.Issues, model.AnalysisCellIssue{Column: "period", Code: "non_adjacent_observed_period"})
			}
			previous[key] = *row
		}
	}
	return nil
}

func parsePeriod(value string, location *time.Location) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed, nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999999", "2006-01-02T15:04:05.999999999", "2006-01-02"} {
		if parsed, err := time.ParseInLocation(layout, value, location); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, resultInvalid("period")
}

func nextPeriod(t time.Time, grain string) time.Time {
	switch grain {
	case "hour":
		return t.Add(time.Hour)
	case "day":
		return t.AddDate(0, 0, 1)
	case "week":
		return t.AddDate(0, 0, 7)
	case "month":
		return t.AddDate(0, 1, 0)
	case "quarter":
		return t.AddDate(0, 3, 0)
	case "year":
		return t.AddDate(1, 0, 0)
	}
	return t
}
