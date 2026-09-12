package analysis

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strings"

	model "github.com/domainry/domainry-report-sdk/model"
)

type predicateBuilder struct {
	compiler     *compiler
	parameters   map[string]any
	declarations []model.ReportObjectSQLParameter
}

var scalarNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]{1,3})?$`)

func (p *predicateBuilder) group(filters []model.AnalysisFilter, operator string, depth int) (string, error) {
	if len(filters) > 16 || depth > 4 {
		return "", invalid("filters.limit")
	}
	parts := []string{}
	for _, filter := range filters {
		part, err := p.filter(filter, depth)
		if err != nil {
			return "", err
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return "", nil
	}
	return "(" + strings.Join(parts, " "+operator+" ") + ")", nil
}

func (p *predicateBuilder) filter(f model.AnalysisFilter, depth int) (string, error) {
	p.compiler.filterNodes++
	if p.compiler.filterNodes > 128 {
		return "", invalid("filters.limit")
	}
	if len(f.All) > 0 || len(f.Any) > 0 {
		if f.Field != "" || f.Operator != "" || len(f.Values) > 0 || len(f.All) > 0 && len(f.Any) > 0 {
			return "", invalid("filters.shape")
		}
		if len(f.All) > 0 {
			return p.group(f.All, "AND", depth+1)
		}
		return p.group(f.Any, "OR", depth+1)
	}
	field, ok := p.compiler.fields[f.Field]
	if !ok {
		return "", invalid("filters.field")
	}
	expression := fieldSQL(f.Field)
	if f.Operator == "is_null" || f.Operator == "not_null" {
		if len(f.Values) != 0 {
			return "", invalid("filters.values")
		}
		if f.Operator == "is_null" {
			return expression + " IS NULL", nil
		}
		return expression + " IS NOT NULL", nil
	}
	arity := 1
	switch f.Operator {
	case "eq", "ne":
	case "gt", "ge", "lt", "le", "between":
		if field.Type == "boolean" {
			return "", invalid("filters.operator")
		}
		if f.Operator == "between" {
			arity = 2
		}
	case "in", "not_in":
		if len(f.Values) < 1 || len(f.Values) > 32 {
			return "", invalid("filters.values")
		}
		arity = len(f.Values)
	case "contains":
		if field.Type != "text" {
			return "", invalid("filters.type")
		}
	default:
		return "", invalid("filters.operator")
	}
	if len(f.Values) != arity {
		return "", invalid("filters.values")
	}
	parameters := []string{}
	for _, value := range f.Values {
		parameter, err := p.parameter(field.Type, value)
		if err != nil {
			return "", err
		}
		parameters = append(parameters, parameter)
	}
	switch f.Operator {
	case "in":
		return expression + " IN (" + strings.Join(parameters, ",") + ")", nil
	case "not_in":
		return expression + " NOT IN (" + strings.Join(parameters, ",") + ")", nil
	case "between":
		return "(" + expression + ">=" + parameters[0] + " AND " + expression + "<=" + parameters[1] + ")", nil
	case "contains":
		return "CONTAINS(" + expression + "," + parameters[0] + ")", nil
	default:
		return expression + map[string]string{"eq": "=", "ne": "<>", "gt": ">", "ge": ">=", "lt": "<", "le": "<="}[f.Operator] + parameters[0], nil
	}
}

func (p *predicateBuilder) parameter(kind string, value any) (string, error) {
	if len(p.parameters) >= 128 {
		return "", invalid("filters.values_limit")
	}
	parameterType := kind
	switch kind {
	case "text", "date", "datetime":
		text, ok := value.(string)
		if !ok || len(text) > 4096 {
			return "", invalid("filters.value_type")
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return "", invalid("filters.value_type")
		}
	default:
		number, ok := value.(json.Number)
		if !numeric(kind) || !ok || len(number) > 128 || !scalarNumber.MatchString(string(number)) {
			return "", invalid("filters.value_type")
		}
		rational, ok := new(big.Rat).SetString(string(number))
		if !ok || rational.Num().BitLen() > 1024 || rational.Denom().BitLen() > 1024 || kind == "integer" && !rational.IsInt() {
			return "", invalid("filters.value_type")
		}
		if kind != "integer" {
			parameterType = "decimal"
		}
	}
	name := fmt.Sprintf("p%d", len(p.parameters))
	p.parameters[name] = value
	p.declarations = append(p.declarations, model.ReportObjectSQLParameter{Key: name, Type: parameterType, Required: true})
	return ":" + name, nil
}
