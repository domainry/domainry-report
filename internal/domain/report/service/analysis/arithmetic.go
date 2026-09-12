package analysis

import (
	"math/big"
	"strings"

	model "github.com/domainry/domainry-report-sdk/model"
)

func readNumber(text string) (*big.Rat, error) {
	if len(text) > 256 || !scalarNumber.MatchString(text) {
		return nil, &Error{Code: "result_invalid", Path: "number"}
	}
	n, ok := new(big.Rat).SetString(text)
	if !ok || n.Num().BitLen() > 4096 || n.Denom().BitLen() > 4096 {
		return nil, &Error{Code: "result_invalid", Path: "number"}
	}
	return n, nil
}

// roundNumber uses integer quotient/remainder arithmetic and half-even ties.
// It never converts a source number to float64, including for large counts.
func roundNumber(number *big.Rat, scale int) string {
	factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	numerator := new(big.Int).Mul(number.Num(), factor)
	negative := numerator.Sign() < 0
	numerator.Abs(numerator)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, number.Denom(), remainder)
	tie := new(big.Int).Lsh(remainder, 1).Cmp(number.Denom())
	if tie > 0 || tie == 0 && quotient.Bit(0) == 1 {
		quotient.Add(quotient, big.NewInt(1))
	}
	digits := quotient.String()
	if scale > 0 {
		if len(digits) <= scale {
			digits = strings.Repeat("0", scale+1-len(digits)) + digits
		}
		digits = digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
	}
	if negative && quotient.Sign() != 0 {
		digits = "-" + digits
	}
	return digits
}

func evaluateExpression(expression model.AnalysisExpression, values map[string]*string) (*big.Rat, string, error) {
	if expression.Reference != "" {
		text := values[expression.Reference]
		if text == nil {
			return nil, "null_input", nil
		}
		n, err := readNumber(*text)
		return n, "", err
	}
	if expression.Decimal != "" {
		n, err := decimalValue(expression.Decimal)
		return n, "", err
	}
	a, issue, err := evaluateExpression(expression.Arguments[0], values)
	if err != nil || issue != "" {
		return nil, issue, err
	}
	if expression.Operator == "negate" {
		return new(big.Rat).Neg(a), "", nil
	}
	b, issue, err := evaluateExpression(expression.Arguments[1], values)
	if err != nil || issue != "" {
		return nil, issue, err
	}
	value := new(big.Rat)
	switch expression.Operator {
	case "add":
		value.Add(a, b)
	case "subtract":
		value.Sub(a, b)
	case "multiply":
		value.Mul(a, b)
	case "divide":
		if b.Sign() == 0 {
			return nil, "division_by_zero", nil
		}
		value.Quo(a, b)
	default:
		return nil, "", invalid("calculations.operator")
	}
	if value.Num().BitLen() > 4096 || value.Denom().BitLen() > 4096 {
		return nil, "", &Error{Code: "arithmetic_limit", Path: "calculations"}
	}
	return value, "", nil
}

func applyCalculationsAndRules(plan Plan, row *model.AnalysisRow) error {
	for _, calculation := range plan.Spec.Calculations {
		value, issue, err := evaluateExpression(calculation.Expression, row.Values)
		if err != nil {
			return err
		}
		row.Values[calculation.Key] = nil
		if issue != "" {
			row.Issues = append(row.Issues, model.AnalysisCellIssue{Column: calculation.Key, Code: issue})
		} else {
			row.Values[calculation.Key] = textPointer(roundNumber(value, calculation.Scale))
		}
	}
	for _, rule := range plan.Spec.AnomalyRules {
		value := row.Values[rule.Column]
		if value == nil {
			row.Issues = append(row.Issues, model.AnalysisCellIssue{Column: rule.Column, Code: "anomaly_rule_" + rule.Key + "_undefined"})
			continue
		}
		n, err := readNumber(*value)
		if err != nil {
			return err
		}
		threshold, _ := decimalValue(rule.Values[0])
		cmp := n.Cmp(threshold)
		matched := false
		switch rule.Operator {
		case "gt":
			matched = cmp > 0
		case "ge":
			matched = cmp >= 0
		case "lt":
			matched = cmp < 0
		case "le":
			matched = cmp <= 0
		case "eq":
			matched = cmp == 0
		case "ne":
			matched = cmp != 0
		case "outside":
			upper, _ := decimalValue(rule.Values[1])
			matched = cmp < 0 || n.Cmp(upper) > 0
		}
		if matched {
			row.Anomalies = append(row.Anomalies, rule.Key)
		}
	}
	return nil
}

func textPointer(value string) *string { return &value }
