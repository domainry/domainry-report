package engine

import (
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

func stableValue(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func compareValues(left, right any) int {
	leftText, rightText := stableValue(left), stableValue(right)
	leftTime, leftTimeErr := parseTime(leftText)
	rightTime, rightTimeErr := parseTime(rightText)
	if leftTimeErr == nil && rightTimeErr == nil {
		if leftTime.Before(rightTime) {
			return -1
		}
		if leftTime.After(rightTime) {
			return 1
		}
		return 0
	}
	leftNumber, leftErr := decimal.NewFromString(leftText)
	rightNumber, rightErr := decimal.NewFromString(rightText)
	if leftErr == nil && rightErr == nil {
		return leftNumber.Cmp(rightNumber)
	}
	return strings.Compare(leftText, rightText)
}

func CompareValues(left, right any) int { return compareValues(left, right) }

func parseTime(value any) (time.Time, error) {
	text := strings.TrimSpace(fmt.Sprint(value))
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid date or datetime %q", text)
}

func dimensionValue(value any, grain, defaultGrain string, location *time.Location) string {
	grain = strings.TrimSpace(grain)
	if grain == "" {
		grain = strings.TrimSpace(defaultGrain)
	}
	if grain == "" || value == nil {
		return stableValue(value)
	}
	parsed, err := parseTime(value)
	if err != nil {
		return stableValue(value)
	}
	parsed = parsed.In(location)
	switch grain {
	case "minute":
		return parsed.Format("2006-01-02T15:04")
	case "hour":
		return parsed.Format("2006-01-02T15:00")
	case "day":
		return parsed.Format("2006-01-02")
	case "week":
		weekday := (int(parsed.Weekday()) + 6) % 7
		return parsed.AddDate(0, 0, -weekday).Format("2006-01-02")
	case "month":
		return parsed.Format("2006-01")
	case "quarter":
		return fmt.Sprintf("%04d-Q%d", parsed.Year(), (int(parsed.Month())-1)/3+1)
	case "year":
		return parsed.Format("2006")
	default:
		return stableValue(value)
	}
}
