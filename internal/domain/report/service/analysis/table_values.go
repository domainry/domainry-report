package analysis

import (
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	model "github.com/domainry/domainry-report-sdk/model"
)

type tableValue struct {
	raw    *string
	key    string
	number *big.Rat
	stamp  time.Time
	isTime bool
}

func tableScalar(kind string, raw *string) (tableValue, error) {
	v := tableValue{raw: raw}
	if raw == nil {
		return v, nil
	}
	if len(*raw) > 16384 || !utf8.ValidString(*raw) || strings.ContainsRune(*raw, 0) {
		return v, resultInvalid("table.cell")
	}
	v.key = *raw
	if numeric(kind) {
		n, err := readNumber(*raw)
		if err != nil || kind == "integer" && !n.IsInt() {
			return v, resultInvalid("table.number")
		}
		v.number, v.key = n, n.RatString()
	} else if kind == "boolean" {
		if *raw != "true" && *raw != "false" {
			return v, resultInvalid("table.boolean")
		}
	} else if kind == "date" || kind == "datetime" {
		layout := time.RFC3339Nano
		if kind == "date" {
			layout = "2006-01-02"
		}
		t, err := time.Parse(layout, *raw)
		if err != nil {
			return v, resultInvalid("table.time")
		}
		v.stamp, v.key, v.isTime = t, t.UTC().Format(time.RFC3339Nano), true
	}
	return v, nil
}

func compareTableValue(a, b tableValue) int {
	if a.number != nil {
		return a.number.Cmp(b.number)
	}
	if a.isTime {
		return a.stamp.Compare(b.stamp)
	}
	return strings.Compare(a.key, b.key)
}

func tableGroupValue(v tableValue, kind string) (tableValue, error) {
	if v.raw == nil {
		return v, nil
	}
	if v.number != nil {
		text, err := exactTableNumber(v.number)
		if err != nil {
			return tableValue{}, err
		}
		v.raw = textPointer(text)
	} else if kind == "datetime" {
		v.raw = textPointer(v.key)
	}
	return v, nil
}

type tablePredicate func(map[string]tableValue) bool

func tableFilters(filters []model.AnalysisFilter, fields map[string]model.AnalysisColumn, anyGroup bool) (tablePredicate, error) {
	parts := []tablePredicate{}
	for _, f := range filters {
		if len(f.All) > 0 || len(f.Any) > 0 {
			children := f.All
			if len(f.Any) > 0 {
				children = f.Any
			}
			part, err := tableFilters(children, fields, len(f.Any) > 0)
			if err != nil {
				return nil, err
			}
			parts = append(parts, part)
			continue
		}
		values := []tableValue{}
		for _, raw := range f.Values {
			var text string
			switch v := raw.(type) {
			case string:
				text = v
			case bool:
				text = strconv.FormatBool(v)
			case json.Number:
				text = string(v)
			default:
				return nil, invalid("filters.value_type")
			}
			v, err := tableScalar(fields[f.Field].Type, &text)
			if err != nil {
				return nil, invalid("filters.value_type")
			}
			values = append(values, v)
		}
		parts = append(parts, func(row map[string]tableValue) bool {
			v := row[f.Field]
			if f.Operator == "is_null" {
				return v.raw == nil
			}
			if f.Operator == "not_null" {
				return v.raw != nil
			}
			// SQL WHERE keeps only true; NULL comparisons, including NOT IN,
			// cannot become a match by negating a missing value.
			if v.raw == nil {
				return false
			}
			if f.Operator == "contains" {
				return strings.Contains(v.key, values[0].key)
			}
			if f.Operator == "in" || f.Operator == "not_in" {
				found := false
				for _, candidate := range values {
					if compareTableValue(v, candidate) == 0 {
						found = true
						break
					}
				}
				if f.Operator == "not_in" {
					return !found
				}
				return found
			}
			cmp := compareTableValue(v, values[0])
			switch f.Operator {
			case "eq":
				return cmp == 0
			case "ne":
				return cmp != 0
			case "gt":
				return cmp > 0
			case "ge":
				return cmp >= 0
			case "lt":
				return cmp < 0
			case "le":
				return cmp <= 0
			case "between":
				return cmp >= 0 && compareTableValue(v, values[1]) <= 0
			}
			return false
		})
	}
	return func(row map[string]tableValue) bool {
		for _, part := range parts {
			v := part(row)
			if anyGroup && v {
				return true
			}
			if !anyGroup && !v {
				return false
			}
		}
		return !anyGroup
	}, nil
}

func tablePeriod(value tableValue, field model.AnalysisColumn, bucket model.AnalysisTimeBucket) (tableValue, error) {
	if value.raw == nil {
		return value, nil
	}
	loc, _ := time.LoadLocation(bucket.TimeZone)
	t := value.stamp.In(loc)
	if field.Type == "date" {
		t, _ = time.ParseInLocation("2006-01-02", *value.raw, loc)
	}
	y, m, d := t.Date()
	switch bucket.Grain {
	case "hour":
		t = t.Add(-time.Duration(t.Minute())*time.Minute - time.Duration(t.Second())*time.Second - time.Duration(t.Nanosecond()))
	case "day":
		t = time.Date(y, m, d, 0, 0, 0, 0, loc)
	case "week":
		t = time.Date(y, m, d, 0, 0, 0, 0, loc)
		t = t.AddDate(0, 0, -(int(t.Weekday())+6)%7)
	case "month":
		t = time.Date(y, m, 1, 0, 0, 0, 0, loc)
	case "quarter":
		t = time.Date(y, ((m-1)/3)*3+1, 1, 0, 0, 0, 0, loc)
	case "year":
		t = time.Date(y, 1, 1, 0, 0, 0, 0, loc)
	}
	return tableScalar("datetime", textPointer(t.Format(time.RFC3339Nano)))
}

func exactTableNumber(n *big.Rat) (string, error) {
	scale, exact := n.FloatPrec()
	if !exact || scale > 256 || n.Num().BitLen() > 4096 || n.Denom().BitLen() > 4096 {
		return "", &Error{Code: "arithmetic_limit", Path: "table.sum"}
	}
	text := n.FloatString(scale)
	if len(text) > 256 {
		return "", &Error{Code: "arithmetic_limit", Path: "table.sum"}
	}
	return text, nil
}
