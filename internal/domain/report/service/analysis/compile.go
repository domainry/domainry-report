// Package analysis owns the bounded analysis specification and arithmetic.
// Hosts supply authorized dataset metadata and execute compiled aggregation;
// this package never reads records, storage, principals or result pages.
package analysis

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	model "github.com/domainry/domainry-report-sdk/model"
)

const MaximumRows = 500

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

type Error struct{ Code, Path string }

func (e *Error) Error() string  { return "backend.report.analysis." + e.Code + " at " + e.Path }
func invalid(path string) error { return &Error{Code: "spec_invalid", Path: path} }

type ValueBinding struct {
	Key, Alias, NullAlias, CountAlias, Function string
	Distinct                                    bool
}

type Query struct {
	Segment    string
	Report     model.ReportSchema
	Parameters map[string]any
	Values     []ValueBinding
	CountAlias string
}

type Plan struct {
	Spec        model.AnalysisRequest
	Dataset     model.AnalysisDataset
	Queries     []Query
	Columns     []model.AnalysisColumn
	Methods     []model.AnalysisMethod
	GroupKeys   []string
	MeasureKeys []string
}

type compiler struct {
	plan        Plan
	fields      map[string]model.AnalysisColumn
	output      map[string]bool
	filterNodes int
}

func Compile(request model.AnalysisRequest, dataset model.AnalysisDataset) (Plan, error) {
	raw, err := json.Marshal(request)
	if err != nil || len(raw) > 65536 {
		return Plan{}, invalid("request")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&request); err != nil {
		return Plan{}, invalid("request")
	}
	if !identifier.MatchString(dataset.Key) || dataset.Kind != "business_object" || dataset.Version == "" || len(dataset.Columns) == 0 || len(dataset.Columns) > 256 || request.DatasetKey != dataset.Key {
		return Plan{}, invalid("dataset")
	}
	if request.Mode == "" {
		request.Mode = "aggregate"
	}
	if request.MaxRows == 0 {
		request.MaxRows = 100
	}
	if request.MaxRows < 1 || request.MaxRows > MaximumRows || len(request.GroupBy) > 4 || len(request.Measures) > 16 || len(request.Select) > 16 || len(request.Calculations) > 16 || len(request.AnomalyRules) > 16 {
		return Plan{}, invalid("limits")
	}
	c := compiler{plan: Plan{Spec: request, Dataset: dataset}, fields: map[string]model.AnalysisColumn{}, output: map[string]bool{}}
	for _, field := range dataset.Columns {
		if !identifier.MatchString(field.Key) || c.fields[field.Key].Key != "" || !supportedType(field.Type) || len(field.Unit) > 64 {
			return Plan{}, invalid("dataset.columns")
		}
		c.fields[field.Key] = field
	}
	switch request.Mode {
	case "aggregate":
		if request.Time != nil || request.Comparison != nil || len(request.Select) != 0 {
			return Plan{}, invalid("mode")
		}
	case "compare":
		if request.Comparison == nil || request.Time != nil || len(request.Select) != 0 {
			return Plan{}, invalid("comparison")
		}
	case "trend":
		if request.Time == nil || request.Comparison != nil || len(request.Select) != 0 {
			return Plan{}, invalid("time")
		}
		bucket := *request.Time
		field := c.fields[bucket.Field]
		if field.Type != "date" && field.Type != "datetime" || !map[string]bool{"hour": true, "day": true, "week": true, "month": true, "quarter": true, "year": true}[bucket.Grain] {
			return Plan{}, invalid("time")
		}
		if bucket.TimeZone == "" {
			bucket.TimeZone = "UTC"
		}
		if _, err := time.LoadLocation(bucket.TimeZone); err != nil {
			return Plan{}, invalid("time.time_zone")
		}
		c.plan.Spec.Time = &bucket
	case "table":
		if request.Time != nil || request.Comparison != nil || len(request.GroupBy) != 0 || len(request.Measures) != 0 || len(request.Select) == 0 {
			return Plan{}, invalid("table")
		}
	default:
		return Plan{}, invalid("mode")
	}
	if request.Mode != "table" && len(request.Measures) == 0 {
		return Plan{}, invalid("measures")
	}
	keys := request.GroupBy
	if request.Mode == "table" {
		keys = request.Select
	}
	for _, key := range keys {
		field, ok := c.fields[key]
		if !ok || c.addColumn(field) != nil {
			return Plan{}, invalid("fields")
		}
		if request.Mode != "table" {
			c.plan.GroupKeys = append(c.plan.GroupKeys, key)
		}
	}
	if request.Time != nil {
		if c.addColumn(model.AnalysisColumn{Key: "period", Type: "datetime", Unit: "calendar_period"}) != nil {
			return Plan{}, invalid("time.field_collision")
		}
		c.plan.GroupKeys = append(c.plan.GroupKeys, "period")
	}
	for _, measure := range request.Measures {
		field := c.fields[measure.Field]
		if !identifier.MatchString(measure.Key) || measure.Distinct && (measure.Function != "count" || measure.Field == "") {
			return Plan{}, invalid("measures")
		}
		if measure.Field == "" && measure.Function != "count" || measure.Field != "" && field.Key == "" {
			return Plan{}, invalid("measures.field")
		}
		switch measure.Function {
		case "sum", "avg":
			if !numeric(field.Type) {
				return Plan{}, invalid("measures.numeric")
			}
		case "min", "max":
			if field.Type == "boolean" {
				return Plan{}, invalid("measures.type")
			}
		case "count":
		default:
			return Plan{}, invalid("measures.function")
		}
		column := field
		column.Key = measure.Key
		if measure.Function == "count" {
			column.Type, column.Unit, column.Precision, column.Scale = "integer", "records", 0, 0
			if measure.Distinct {
				column.Unit = "distinct_values"
			}
		}
		if measure.Function == "sum" || measure.Function == "avg" {
			column.Precision = 0 // Aggregate precision is not the input field precision.
		}
		if measure.Function == "avg" {
			column.Scale = 6
			if column.Type != "number" {
				column.Type = "decimal"
			}
		}
		if request.Mode == "compare" || request.Mode == "trend" {
			if !numeric(column.Type) {
				return Plan{}, invalid("measures.numeric")
			}
		}
		if request.Mode == "compare" {
			for _, prefix := range []string{"baseline_", "current_", "delta_", "change_pct_"} {
				out := changeColumn(column, prefix)
				if c.addColumn(out) != nil {
					return Plan{}, invalid("measures.keys")
				}
			}
		} else if c.addColumn(column) != nil {
			return Plan{}, invalid("measures.keys")
		}
		c.plan.MeasureKeys = append(c.plan.MeasureKeys, measure.Key)
		method := model.AnalysisMethod{Column: measure.Key, Method: measure.Function, Numerics: numerics(column.Type), Nulls: "ignore_null_inputs"}
		if measure.Function == "count" && measure.Field == "" {
			method.Nulls = "include_rows"
		}
		if measure.Distinct {
			method.Method = "count_distinct_non_null_values"
		}
		if measure.Function == "avg" {
			method.Method, method.Rounding = "sum_divided_by_non_null_count", "half_even_6_decimal_places"
		}
		if request.Mode == "compare" {
			for _, prefix := range []string{"baseline_", "current_"} {
				m := method
				m.Column = prefix + measure.Key
				c.plan.Methods = append(c.plan.Methods, m)
			}
			c.changeMethods(column, "baseline")
		} else {
			c.plan.Methods = append(c.plan.Methods, method)
		}
	}
	segments := []struct {
		name    string
		filters []model.AnalysisFilter
	}{{"dataset", nil}}
	if request.Mode == "compare" {
		segments = []struct {
			name    string
			filters []model.AnalysisFilter
		}{{"baseline", request.Comparison.Baseline}, {"current", request.Comparison.Current}}
	}
	for _, segment := range segments {
		query, err := c.query(segment.name, append(append([]model.AnalysisFilter{}, request.Filters...), segment.filters...))
		if err != nil {
			return Plan{}, err
		}
		c.plan.Queries = append(c.plan.Queries, query)
	}
	if err := c.postSpecifications(); err != nil {
		return Plan{}, err
	}
	return c.plan, nil
}

func (c *compiler) addColumn(field model.AnalysisColumn) error {
	if !identifier.MatchString(field.Key) || c.output[field.Key] {
		return invalid("columns")
	}
	if field.Unit == "" {
		field.Unit = "unspecified"
	}
	c.output[field.Key] = true
	c.plan.Columns = append(c.plan.Columns, field)
	return nil
}

func numeric(t string) bool {
	return t == "integer" || t == "decimal" || t == "currency" || t == "percent" || t == "number"
}
func supportedType(t string) bool {
	return numeric(t) || t == "text" || t == "boolean" || t == "date" || t == "datetime"
}
func fieldSQL(key string) string { return "d.`" + key + "`" }

func (c *compiler) query(segment string, filters []model.AnalysisFilter) (Query, error) {
	q := Query{Segment: segment, Parameters: map[string]any{}}
	p := predicateBuilder{compiler: c, parameters: q.Parameters}
	where, err := p.group(filters, "AND", 0)
	if err != nil {
		return Query{}, err
	}
	projections, groups, orders := []string{}, []string{}, []string{}
	for i, key := range c.plan.GroupKeys {
		expr := fieldSQL(key)
		if key == "period" && c.plan.Spec.Time != nil {
			expr = "DATE_BUCKET('" + c.plan.Spec.Time.Grain + "'," + fieldSQL(c.plan.Spec.Time.Field) + ")"
		}
		alias, nullAlias := fmt.Sprintf("g%d", i), fmt.Sprintf("gn%d", i)
		projections = append(projections, expr+" AS "+alias, "MAX(CASE WHEN "+expr+" IS NULL THEN 1 ELSE 0 END) AS "+nullAlias)
		groups = append(groups, expr)
		orders = append(orders, alias)
		q.Values = append(q.Values, ValueBinding{Key: key, Alias: alias, NullAlias: nullAlias})
	}
	for i, key := range c.plan.Spec.Select {
		alias, nullAlias := fmt.Sprintf("v%d", i), fmt.Sprintf("vn%d", i)
		expr := fieldSQL(key)
		projections = append(projections, expr+" AS "+alias, "CASE WHEN "+expr+" IS NULL THEN 1 ELSE 0 END AS "+nullAlias)
		q.Values = append(q.Values, ValueBinding{Key: key, Alias: alias, NullAlias: nullAlias})
		orders = append(orders, alias)
	}
	for i, m := range c.plan.Spec.Measures {
		alias, countAlias := fmt.Sprintf("m%d", i), fmt.Sprintf("n%d", i)
		arg := "*"
		if m.Field != "" {
			arg = fieldSQL(m.Field)
		}
		function := strings.ToUpper(m.Function)
		if m.Function == "avg" {
			function = "SUM"
		}
		argument := arg
		if m.Distinct {
			argument = "DISTINCT " + arg
		}
		projections = append(projections, function+"("+argument+") AS "+alias, "COUNT("+arg+") AS "+countAlias)
		q.Values = append(q.Values, ValueBinding{Key: m.Key, Alias: alias, CountAlias: countAlias, Function: m.Function, Distinct: m.Distinct})
	}
	if c.plan.Spec.Mode != "table" {
		q.CountAlias = "source_count"
		projections = append(projections, "COUNT(*) AS source_count")
	}
	sql := "SELECT " + strings.Join(projections, ", ") + " FROM `" + c.plan.Dataset.Key + "` d"
	if where != "" {
		sql += " WHERE " + where
	}
	if len(groups) > 0 {
		sql += " GROUP BY " + strings.Join(groups, ", ")
	}
	if len(orders) > 0 {
		sql += " ORDER BY " + strings.Join(orders, ", ")
	}
	sql += " LIMIT " + strconv.Itoa(c.plan.Spec.MaxRows+1)
	timezone := "UTC"
	if c.plan.Spec.Time != nil {
		timezone = c.plan.Spec.Time.TimeZone
	}
	q.Report = model.ReportSchema{Key: "analysis_" + c.plan.Dataset.Key, RequiredPermissions: []string{c.plan.Dataset.Key + ".read"}, ObjectSQLV1: &model.ReportObjectSQLSchema{SQL: sql, Parameters: p.declarations, TimeoutMilliseconds: 30000, TimeZone: timezone}}
	return q, nil
}
