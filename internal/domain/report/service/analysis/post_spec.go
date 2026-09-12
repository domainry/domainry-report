package analysis

import (
	"math/big"
	"regexp"

	model "github.com/domainry/domainry-report-sdk/model"
)

var plainDecimal = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

func decimalValue(text string) (*big.Rat, error) {
	if len(text) > 128 || !plainDecimal.MatchString(text) {
		return nil, invalid("decimal")
	}
	value, ok := new(big.Rat).SetString(text)
	if !ok {
		return nil, invalid("decimal")
	}
	return value, nil
}

type numericInfo struct {
	unit                string
	scalar, approximate bool
}

func (c *compiler) postSpecifications() error {
	if c.plan.Spec.Mode == "trend" {
		columns := append([]model.AnalysisColumn{}, c.plan.Columns...)
		for _, key := range c.plan.MeasureKeys {
			var source model.AnalysisColumn
			for _, column := range columns {
				if column.Key == key {
					source = column
				}
			}
			for _, prefix := range []string{"previous_", "delta_", "change_pct_"} {
				column := changeColumn(source, prefix)
				if err := c.addColumn(column); err != nil {
					return err
				}
			}
			c.plan.Methods = append(c.plan.Methods, model.AnalysisMethod{Column: "previous_" + key, Method: "previous_observed_period_within_group; no_zero_fill", Numerics: numerics(source.Type), Nulls: "null_period_has_no_comparison"})
			c.changeMethods(source, "previous")
		}
	}
	for _, calculation := range c.plan.Spec.Calculations {
		if calculation.Scale < 0 || calculation.Scale > 12 {
			return invalid("calculations.scale")
		}
		nodes := 0
		info, err := c.expression(calculation.Expression, 0, &nodes)
		if err != nil {
			return err
		}
		kind := "decimal"
		if info.approximate {
			kind = "number"
		}
		if err := c.addColumn(model.AnalysisColumn{Key: calculation.Key, Type: kind, Unit: info.unit, Scale: calculation.Scale}); err != nil {
			return err
		}
		c.plan.Methods = append(c.plan.Methods, model.AnalysisMethod{Column: calculation.Key, Method: "structured_arithmetic_on_preceding_rounded_cells", Numerics: numerics(kind), Nulls: "propagate_null; division_by_zero_is_undefined", Rounding: "half_even_to_column_scale"})
	}
	keys := map[string]bool{}
	for _, rule := range c.plan.Spec.AnomalyRules {
		if !identifier.MatchString(rule.Key) || keys[rule.Key] {
			return invalid("anomaly_rules.key")
		}
		keys[rule.Key] = true
		column := c.column(rule.Column)
		if !numeric(column.Type) {
			return invalid("anomaly_rules.column")
		}
		arity := 1
		switch rule.Operator {
		case "gt", "ge", "lt", "le", "eq", "ne":
		case "outside":
			arity = 2
		default:
			return invalid("anomaly_rules.operator")
		}
		if len(rule.Values) != arity {
			return invalid("anomaly_rules.values")
		}
		values := []*big.Rat{}
		for _, raw := range rule.Values {
			value, err := decimalValue(raw)
			if err != nil {
				return err
			}
			values = append(values, value)
		}
		if arity == 2 && values[0].Cmp(values[1]) > 0 {
			return invalid("anomaly_rules.range")
		}
	}
	return nil
}

func numerics(kind string) string {
	if kind == "number" {
		return "source_approximate; rational_postprocessing"
	}
	if numeric(kind) {
		return "exact_integer_or_decimal; rational_postprocessing"
	}
	return "source_ordering"
}

func changeColumn(source model.AnalysisColumn, prefix string) model.AnalysisColumn {
	column := source
	column.Key = prefix + source.Key
	if prefix == "delta_" || prefix == "change_pct_" {
		column.Precision, column.Scale = 0, 6
		if column.Type != "number" {
			column.Type = "decimal"
		}
	}
	if prefix == "change_pct_" {
		column.Unit = "%"
	}
	return column
}

func (c *compiler) changeMethods(source model.AnalysisColumn, from string) {
	c.plan.Methods = append(c.plan.Methods,
		model.AnalysisMethod{Column: "delta_" + source.Key, Method: "current_minus_" + from, Numerics: numerics(source.Type), Nulls: "missing_operand_is_undefined", Rounding: "half_even_6_decimal_places"},
		model.AnalysisMethod{Column: "change_pct_" + source.Key, Method: "100_times_delta_divided_by_abs_" + from, Numerics: numerics(source.Type), Nulls: "missing_operand_or_zero_denominator_is_undefined", Rounding: "half_even_6_decimal_places"})
}

func (c *compiler) column(key string) model.AnalysisColumn {
	for _, column := range c.plan.Columns {
		if column.Key == key {
			return column
		}
	}
	return model.AnalysisColumn{}
}

func (c *compiler) expression(expression model.AnalysisExpression, depth int, nodes *int) (numericInfo, error) {
	*nodes++
	if depth > 8 || *nodes > 64 {
		return numericInfo{}, invalid("calculations.limit")
	}
	if expression.Reference != "" {
		column := c.column(expression.Reference)
		if expression.Decimal != "" || expression.Operator != "" || len(expression.Arguments) > 0 || !numeric(column.Type) {
			return numericInfo{}, invalid("calculations.reference")
		}
		return numericInfo{unit: column.Unit, approximate: column.Type == "number"}, nil
	}
	if expression.Decimal != "" {
		if expression.Operator != "" || len(expression.Arguments) > 0 {
			return numericInfo{}, invalid("calculations.shape")
		}
		if _, err := decimalValue(expression.Decimal); err != nil {
			return numericInfo{}, err
		}
		return numericInfo{unit: "ratio", scalar: true}, nil
	}
	arity := 2
	switch expression.Operator {
	case "add", "subtract", "multiply", "divide":
	case "negate":
		arity = 1
	default:
		return numericInfo{}, invalid("calculations.operator")
	}
	if len(expression.Arguments) != arity {
		return numericInfo{}, invalid("calculations.arguments")
	}
	a, err := c.expression(expression.Arguments[0], depth+1, nodes)
	if err != nil {
		return numericInfo{}, err
	}
	if arity == 1 {
		return a, nil
	}
	b, err := c.expression(expression.Arguments[1], depth+1, nodes)
	if err != nil {
		return numericInfo{}, err
	}
	result := numericInfo{unit: a.unit, scalar: a.scalar && b.scalar, approximate: a.approximate || b.approximate}
	switch expression.Operator {
	case "add", "subtract":
		if a.scalar {
			result.unit = b.unit
		} else if !b.scalar && a.unit != b.unit {
			if a.unit != "unspecified" && b.unit != "unspecified" {
				return numericInfo{}, invalid("calculations.units")
			}
			result.unit = "unspecified"
		}
	case "multiply":
		if a.scalar {
			result.unit = b.unit
		} else if !b.scalar {
			if a.unit == "unspecified" || b.unit == "unspecified" {
				result.unit = "unspecified"
			} else {
				result.unit = "(" + a.unit + ")*(" + b.unit + ")"
			}
		}
	case "divide":
		if !b.scalar {
			if a.unit == "unspecified" || b.unit == "unspecified" {
				result.unit = "unspecified"
			} else if a.unit == b.unit {
				result.unit = "ratio"
			} else {
				result.unit = "(" + a.unit + ")/(" + b.unit + ")"
			}
		}
	}
	if len(result.unit) > 256 {
		return numericInfo{}, invalid("calculations.units")
	}
	return result, nil
}
