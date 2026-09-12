package analysis

import (
	"context"
	"encoding/json"
	"math/big"
	"sort"
	"strconv"

	model "github.com/domainry/domainry-report-sdk/model"
)

const MaximumTableRows = 100000
const MaximumTableBytes = 16 << 20

// TableAccumulator consumes an authorized structured stream supplied by a host.
// It performs no IO or file parsing, and exposes no result before the caller
// verifies the source's complete EOF receipt and actual delivered row count.
type TableAccumulator struct {
	plan     Plan
	fields   map[string]model.AnalysisColumn
	keys     []string
	segments []tableSegment
	rows     int64
	bytes    int
	retained int
	failure  error
}
type tableSegment struct {
	match  tablePredicate
	groups map[string]*tableGroup
	table  []map[string]string
}
type tableGroup struct {
	values   map[string]tableValue
	count    int64
	measures []tableMeasure
}
type tableMeasure struct {
	count    int64
	sum      *big.Rat
	extreme  tableValue
	distinct map[string]bool
}

func TableFields(plan Plan) []string {
	keys := map[string]bool{}
	for _, key := range plan.Spec.Select {
		keys[key] = true
	}
	for _, key := range plan.Spec.GroupBy {
		keys[key] = true
	}
	for _, m := range plan.Spec.Measures {
		if m.Field != "" {
			keys[m.Field] = true
		}
	}
	if plan.Spec.Time != nil {
		keys[plan.Spec.Time.Field] = true
	}
	var visit func([]model.AnalysisFilter)
	visit = func(filters []model.AnalysisFilter) {
		for _, f := range filters {
			if f.Field != "" {
				keys[f.Field] = true
			}
			visit(f.All)
			visit(f.Any)
		}
	}
	for _, q := range plan.Queries {
		visit(q.Filters)
	}
	out := []string{}
	for key := range keys {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func NewTableAccumulator(plan Plan) (*TableAccumulator, error) {
	if plan.Dataset.Kind != "table_file" {
		return nil, invalid("dataset")
	}
	t := &TableAccumulator{plan: plan, fields: map[string]model.AnalysisColumn{}, keys: TableFields(plan)}
	for _, f := range plan.Dataset.Columns {
		t.fields[f.Key] = f
	}
	for _, q := range plan.Queries {
		match, err := tableFilters(q.Filters, t.fields, false)
		if err != nil {
			return nil, err
		}
		segment := tableSegment{match: match, groups: map[string]*tableGroup{}}
		if plan.Spec.Mode != "table" && len(plan.GroupKeys) == 0 {
			segment.groups["[]"] = t.newGroup(nil)
		}
		t.segments = append(t.segments, segment)
	}
	return t, nil
}
func (t *TableAccumulator) Rows() int64 { return t.rows }
func (t *TableAccumulator) newGroup(values map[string]tableValue) *tableGroup {
	g := &tableGroup{values: values, measures: make([]tableMeasure, len(t.plan.Spec.Measures))}
	for i, m := range t.plan.Spec.Measures {
		g.measures[i].sum = new(big.Rat)
		if m.Distinct {
			g.measures[i].distinct = map[string]bool{}
		}
	}
	return g
}
func tableLimit(path string) error { return &Error{Code: "result_limit_exceeded", Path: path} }

func (t *TableAccumulator) Add(ctx context.Context, raw model.AnalysisTableRow) error {
	if t.failure != nil {
		return t.failure
	}
	t.failure = t.add(ctx, raw)
	return t.failure
}
func (t *TableAccumulator) add(ctx context.Context, raw model.AnalysisTableRow) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.rows++
	if t.rows > MaximumTableRows {
		return tableLimit("table.input_rows")
	}
	if len(raw) != len(t.keys) {
		return resultInvalid("table.fields")
	}
	values := map[string]tableValue{}
	for _, key := range t.keys {
		cell, ok := raw[key]
		if !ok {
			return resultInvalid("table.fields")
		}
		// Include per-cell and per-row overhead so empty cells also consume budget.
		t.bytes += len(key) + 16
		if cell != nil {
			t.bytes += len(*cell)
		}
		if t.bytes > MaximumTableBytes {
			return tableLimit("table.input_bytes")
		}
		v, err := tableScalar(t.fields[key].Type, cell)
		if err != nil {
			return err
		}
		// Hosts may reuse a row buffer after Add returns; retain owned strings.
		if v.raw != nil {
			v.raw = textPointer(*v.raw)
		}
		values[key] = v
	}
	t.bytes += 16
	if t.bytes > MaximumTableBytes {
		return tableLimit("table.input_bytes")
	}
	for index := range t.segments {
		segment := &t.segments[index]
		if !segment.match(values) {
			continue
		}
		if t.plan.Spec.Mode == "table" {
			if len(segment.table) >= t.plan.Spec.MaxRows {
				return tableLimit("table.output_rows")
			}
			r := map[string]string{}
			for _, b := range t.plan.Queries[index].Values {
				tableRawCell(r, b, values[b.Key])
			}
			segment.table = append(segment.table, r)
			continue
		}
		groups := map[string]tableValue{}
		keyParts := []*string{}
		for _, key := range t.plan.GroupKeys {
			v := values[key]
			var err error
			v, err = tableGroupValue(v, t.fields[key].Type)
			if err != nil {
				return err
			}
			if key == "period" && t.plan.Spec.Time != nil {
				var err error
				v, err = tablePeriod(values[t.plan.Spec.Time.Field], t.fields[t.plan.Spec.Time.Field], *t.plan.Spec.Time)
				if err != nil {
					return err
				}
			}
			groups[key] = v
			var part *string
			if v.raw != nil {
				part = textPointer(v.key)
			}
			keyParts = append(keyParts, part)
		}
		encoded, _ := json.Marshal(keyParts)
		key := string(encoded)
		g := segment.groups[key]
		if g == nil {
			if len(segment.groups) >= t.plan.Spec.MaxRows {
				return tableLimit("table.output_groups")
			}
			g = t.newGroup(groups)
			t.retained += len(key)*2 + len(t.plan.Spec.Measures)*128 + 128
			if t.retained > MaximumTableBytes {
				return tableLimit("table.state_bytes")
			}
			segment.groups[key] = g
		}
		g.count++
		for i, m := range t.plan.Spec.Measures {
			a := &g.measures[i]
			v := values[m.Field]
			if m.Field != "" && v.raw == nil {
				continue
			}
			a.count++
			if m.Distinct {
				if !a.distinct[v.key] {
					t.retained += len(v.key) + 64
					if t.retained > MaximumTableBytes {
						return tableLimit("table.state_bytes")
					}
				}
				a.distinct[v.key] = true
			}
			switch m.Function {
			case "sum", "avg":
				a.sum.Add(a.sum, v.number)
				if a.sum.Num().BitLen() > 4096 || a.sum.Denom().BitLen() > 4096 {
					return &Error{Code: "arithmetic_limit", Path: "table.sum"}
				}
			case "min", "max":
				if a.extreme.raw == nil || m.Function == "min" && compareTableValue(v, a.extreme) < 0 || m.Function == "max" && compareTableValue(v, a.extreme) > 0 {
					a.extreme = v
				}
			}
		}
	}
	return nil
}

func tableRawCell(raw map[string]string, b ValueBinding, v tableValue) {
	raw[b.NullAlias] = "1"
	if v.raw != nil {
		raw[b.Alias] = *v.raw
		raw[b.NullAlias] = "0"
	}
}

func (t *TableAccumulator) Evaluate() (Evaluation, error) {
	if t.failure != nil {
		return Evaluation{}, t.failure
	}
	results := []model.ReportObjectSQLExecutionResult{}
	for index, segment := range t.segments {
		rows := segment.table
		keys := []string{}
		for key := range segment.groups {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			g := segment.groups[key]
			q := t.plan.Queries[index]
			raw := map[string]string{q.CountAlias: strconv.FormatInt(g.count, 10)}
			for _, b := range q.Values {
				if b.Function == "" {
					tableRawCell(raw, b, g.values[b.Key])
					continue
				}
				for i, m := range t.plan.Spec.Measures {
					if m.Key != b.Key {
						continue
					}
					a := g.measures[i]
					raw[b.CountAlias] = strconv.FormatInt(a.count, 10)
					switch m.Function {
					case "count":
						n := a.count
						if m.Distinct {
							n = int64(len(a.distinct))
						}
						raw[b.Alias] = strconv.FormatInt(n, 10)
					case "sum", "avg":
						v, err := exactTableNumber(a.sum)
						if err != nil {
							return Evaluation{}, err
						}
						raw[b.Alias] = v
					case "min", "max":
						if a.extreme.raw != nil {
							raw[b.Alias] = *a.extreme.raw
						}
					}
				}
			}
			rows = append(rows, raw)
		}
		results = append(results, model.ReportObjectSQLExecutionResult{Rows: rows, Total: len(rows), TotalKnown: true})
	}
	return Evaluate(t.plan, results)
}
