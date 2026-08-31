package objectsql

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	reportmodel "github.com/domainry/domainry-report-sdk/model"
	reportengine "github.com/domainry/domainry-report/internal/domain/report/service/engine"
	"vitess.io/vitess/go/vt/sqlparser"
)

func (c *objectSQLCompiler) compileExpression(raw sqlparser.Expr, allowResultAlias bool) (reportmodel.ReportObjectSQLExpression, error) {
	switch value := raw.(type) {
	case *sqlparser.ColName:
		qualifier, fieldKey := strings.TrimSpace(value.Qualifier.Name.String()), strings.TrimSpace(value.Name.String())
		if qualifier == "" && allowResultAlias {
			if expression, ok := c.projections[fieldKey]; ok {
				return reportmodel.ReportObjectSQLExpression{Kind: "result", Alias: fieldKey, Type: expression.Type, Precision: expression.Precision, Scale: expression.Scale}, nil
			}
		}
		if qualifier == "" || !value.Qualifier.Qualifier.IsEmpty() {
			return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_field_unqualified", "object_sql_v1.sql", map[string]string{"field_key": fieldKey})
		}
		object, ok := c.aliases[qualifier]
		if !ok {
			return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_alias_not_found", "object_sql_v1.sql", map[string]string{"alias": qualifier})
		}
		field, ok := objectSQLField(object, fieldKey)
		if !ok {
			return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.field_not_found", "object_sql_v1.sql", map[string]string{"object": object.Key, "field_key": fieldKey})
		}
		fieldType, precision, scale := objectSQLFieldType(field)
		if fieldType == "" {
			return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_field_type_unsupported", "object_sql_v1.sql", map[string]string{"object": object.Key, "field_key": fieldKey, "type": field.Type})
		}
		return reportmodel.ReportObjectSQLExpression{Kind: "field", Alias: qualifier, FieldKey: fieldKey, Type: fieldType, Precision: precision, Scale: scale}, nil
	case *sqlparser.Argument:
		parameter, ok := c.parameters[strings.TrimPrefix(value.Name, ":")]
		if !ok {
			return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_parameter_not_declared", "object_sql_v1.sql", map[string]string{"parameter": value.Name})
		}
		return reportmodel.ReportObjectSQLExpression{Kind: "parameter", Name: parameter.Key, Type: parameter.Type}, nil
	case *sqlparser.Literal:
		switch value.Type {
		case sqlparser.IntVal:
			if _, err := strconv.ParseInt(value.Val, 10, 64); err != nil {
				return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_literal_invalid", "object_sql_v1.sql", nil)
			}
			return reportmodel.ReportObjectSQLExpression{Kind: "literal", Value: value.Val, ValueType: "integer", Type: "integer"}, nil
		case sqlparser.DecimalVal:
			return reportmodel.ReportObjectSQLExpression{Kind: "literal", Value: value.Val, ValueType: "decimal", Type: "decimal"}, nil
		default:
			return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_literal_forbidden", "object_sql_v1.sql", nil)
		}
	case *sqlparser.NullVal:
		return reportmodel.ReportObjectSQLExpression{Kind: "null", Type: "null"}, nil
	case sqlparser.BoolVal:
		return reportmodel.ReportObjectSQLExpression{Kind: "literal", Value: strconv.FormatBool(bool(value)), ValueType: "boolean", Type: "boolean"}, nil
	case *sqlparser.BinaryExpr:
		operator := strings.TrimSpace(value.Operator.ToString())
		if operator != "+" && operator != "-" && operator != "*" && operator != "/" {
			return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_operator_forbidden", "object_sql_v1.sql", map[string]string{"operator": operator})
		}
		left, err := c.compileExpression(value.Left, false)
		if err != nil {
			return reportmodel.ReportObjectSQLExpression{}, err
		}
		right, err := c.compileExpression(value.Right, false)
		if err != nil {
			return reportmodel.ReportObjectSQLExpression{}, err
		}
		if operator != "/" {
			objectSQLCoerceCurrencyPair(&left, &right)
		}
		resultType, precision, scale, ok := objectSQLArithmeticType(operator, left, right)
		if !ok {
			return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_type_invalid", "object_sql_v1.sql", map[string]string{"operator": operator})
		}
		return reportmodel.ReportObjectSQLExpression{Kind: "binary", Operator: operator, Arguments: []reportmodel.ReportObjectSQLExpression{left, right}, Type: resultType, Precision: precision, Scale: scale}, nil
	case *sqlparser.UnaryExpr:
		operator := strings.TrimSpace(value.Operator.ToString())
		if operator != "+" && operator != "-" {
			return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_operator_forbidden", "object_sql_v1.sql", map[string]string{"operator": operator})
		}
		expression, err := c.compileExpression(value.Expr, false)
		if err != nil {
			return reportmodel.ReportObjectSQLExpression{}, err
		}
		if !objectSQLNumericType(expression.Type) {
			return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_type_invalid", "object_sql_v1.sql", nil)
		}
		return reportmodel.ReportObjectSQLExpression{Kind: "unary", Operator: operator, Arguments: []reportmodel.ReportObjectSQLExpression{expression}, Type: expression.Type, Precision: expression.Precision, Scale: expression.Scale}, nil
	case *sqlparser.ComparisonExpr:
		if value.Modifier != 0 || value.Escape != nil {
			return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_operator_forbidden", "object_sql_v1.sql", nil)
		}
		operator := strings.ToLower(strings.TrimSpace(value.Operator.ToString()))
		if !objectSQLComparisonOperator(operator) {
			return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_operator_forbidden", "object_sql_v1.sql", map[string]string{"operator": operator})
		}
		left, err := c.compileExpression(value.Left, false)
		if err != nil {
			return reportmodel.ReportObjectSQLExpression{}, err
		}
		right, err := c.compileExpression(value.Right, false)
		if err != nil {
			return reportmodel.ReportObjectSQLExpression{}, err
		}
		objectSQLCoerceCurrencyPair(&left, &right)
		if !objectSQLComparable(left.Type, right.Type) {
			return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_type_invalid", "object_sql_v1.sql", nil)
		}
		return reportmodel.ReportObjectSQLExpression{Kind: "comparison", Operator: operator, Arguments: []reportmodel.ReportObjectSQLExpression{left, right}, Type: "boolean"}, nil
	case *sqlparser.BetweenExpr:
		left, err := c.compileExpression(value.Left, false)
		if err != nil {
			return reportmodel.ReportObjectSQLExpression{}, err
		}
		from, err := c.compileExpression(value.From, false)
		if err != nil {
			return reportmodel.ReportObjectSQLExpression{}, err
		}
		to, err := c.compileExpression(value.To, false)
		if err != nil {
			return reportmodel.ReportObjectSQLExpression{}, err
		}
		if !objectSQLComparable(left.Type, from.Type) || !objectSQLComparable(left.Type, to.Type) {
			return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_type_invalid", "object_sql_v1.sql", nil)
		}
		operator := "between"
		if !value.IsBetween {
			operator = "not between"
		}
		return reportmodel.ReportObjectSQLExpression{Kind: "between", Operator: operator, Arguments: []reportmodel.ReportObjectSQLExpression{left, from, to}, Type: "boolean"}, nil
	case *sqlparser.IsExpr:
		left, err := c.compileExpression(value.Left, false)
		if err != nil {
			return reportmodel.ReportObjectSQLExpression{}, err
		}
		operator := strings.ToLower(strings.TrimSpace(value.Right.ToString()))
		if operator != "is null" && operator != "is not null" && operator != "is true" && operator != "is not true" && operator != "is false" && operator != "is not false" {
			return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_operator_forbidden", "object_sql_v1.sql", nil)
		}
		return reportmodel.ReportObjectSQLExpression{Kind: "is", Operator: operator, Arguments: []reportmodel.ReportObjectSQLExpression{left}, Type: "boolean"}, nil
	case *sqlparser.AndExpr:
		return c.compileLogical("and", value.Left, value.Right)
	case *sqlparser.OrExpr:
		return c.compileLogical("or", value.Left, value.Right)
	case *sqlparser.NotExpr:
		expression, err := c.compileExpression(value.Expr, false)
		if err != nil {
			return reportmodel.ReportObjectSQLExpression{}, err
		}
		if expression.Type != "boolean" {
			return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_type_invalid", "object_sql_v1.sql", nil)
		}
		return reportmodel.ReportObjectSQLExpression{Kind: "not", Arguments: []reportmodel.ReportObjectSQLExpression{expression}, Type: "boolean"}, nil
	case *sqlparser.CaseExpr:
		return c.compileCase(value)
	case *sqlparser.FuncExpr:
		return c.compileFunction(value)
	case sqlparser.AggrFunc:
		return c.compileAggregate(value)
	default:
		return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_syntax_forbidden", "object_sql_v1.sql", map[string]string{"node": fmt.Sprintf("%T", raw)})
	}
}

func (c *objectSQLCompiler) compileLogical(operator string, leftRaw, rightRaw sqlparser.Expr) (reportmodel.ReportObjectSQLExpression, error) {
	left, err := c.compileExpression(leftRaw, false)
	if err != nil {
		return reportmodel.ReportObjectSQLExpression{}, err
	}
	right, err := c.compileExpression(rightRaw, false)
	if err != nil {
		return reportmodel.ReportObjectSQLExpression{}, err
	}
	if left.Type != "boolean" || right.Type != "boolean" {
		return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_type_invalid", "object_sql_v1.sql", nil)
	}
	return reportmodel.ReportObjectSQLExpression{Kind: "logical", Operator: operator, Arguments: []reportmodel.ReportObjectSQLExpression{left, right}, Type: "boolean"}, nil
}

func (c *objectSQLCompiler) compileCase(value *sqlparser.CaseExpr) (reportmodel.ReportObjectSQLExpression, error) {
	if value.Expr != nil || len(value.Whens) == 0 {
		return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_case_invalid", "object_sql_v1.sql", nil)
	}
	result := reportmodel.ReportObjectSQLExpression{Kind: "case"}
	for _, when := range value.Whens {
		condition, err := c.compileExpression(when.Cond, false)
		if err != nil {
			return reportmodel.ReportObjectSQLExpression{}, err
		}
		branch, err := c.compileExpression(when.Val, false)
		if err != nil {
			return reportmodel.ReportObjectSQLExpression{}, err
		}
		if condition.Type != "boolean" {
			return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_type_invalid", "object_sql_v1.sql", nil)
		}
		objectSQLCoerceCaseBranch(result, &branch)
		if !objectSQLMergeExpressionType(&result, branch) {
			return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_type_invalid", "object_sql_v1.sql", nil)
		}
		result.Whens = append(result.Whens, reportmodel.ReportObjectSQLWhen{Condition: condition, Value: branch})
	}
	if value.Else != nil {
		other, err := c.compileExpression(value.Else, false)
		if err != nil {
			return reportmodel.ReportObjectSQLExpression{}, err
		}
		objectSQLCoerceCaseBranch(result, &other)
		if !objectSQLMergeExpressionType(&result, other) {
			return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_type_invalid", "object_sql_v1.sql", nil)
		}
		result.Else = &other
	}
	return result, nil
}

func (c *objectSQLCompiler) compileFunction(value *sqlparser.FuncExpr) (reportmodel.ReportObjectSQLExpression, error) {
	if !value.Qualifier.IsEmpty() {
		return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_function_forbidden", "object_sql_v1.sql", nil)
	}
	name := strings.ToLower(strings.TrimSpace(value.Name.String()))
	if name == "date_bucket" {
		return c.compileDateBucket(value)
	}
	allowedArity := map[string][2]int{"coalesce": {2, 8}, "nullif": {2, 2}, "round": {1, 2}, "floor": {1, 1}}
	arity, ok := allowedArity[name]
	if !ok || len(value.Exprs) < arity[0] || len(value.Exprs) > arity[1] {
		return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_function_forbidden", "object_sql_v1.sql", map[string]string{"function": name})
	}
	result := reportmodel.ReportObjectSQLExpression{Kind: "function", Name: name}
	for _, raw := range value.Exprs {
		expression, err := c.compileExpression(raw, false)
		if err != nil {
			return reportmodel.ReportObjectSQLExpression{}, err
		}
		result.Arguments = append(result.Arguments, expression)
	}
	switch name {
	case "coalesce":
		result.Type, result.Precision, result.Scale = result.Arguments[0].Type, result.Arguments[0].Precision, result.Arguments[0].Scale
		for _, argument := range result.Arguments[1:] {
			if argument.Type != "null" && !objectSQLComparable(result.Type, argument.Type) {
				return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_type_invalid", "object_sql_v1.sql", nil)
			}
			if result.Type == "number" || argument.Type == "number" {
				result.Type, result.Precision, result.Scale = "number", 0, 0
			}
		}
	case "nullif":
		if !objectSQLComparable(result.Arguments[0].Type, result.Arguments[1].Type) {
			return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_type_invalid", "object_sql_v1.sql", nil)
		}
		result.Type, result.Precision, result.Scale = result.Arguments[0].Type, result.Arguments[0].Precision, result.Arguments[0].Scale
	case "round", "floor":
		if !objectSQLNumericType(result.Arguments[0].Type) {
			return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_type_invalid", "object_sql_v1.sql", nil)
		}
		result.Type, result.Precision, result.Scale = result.Arguments[0].Type, result.Arguments[0].Precision, result.Arguments[0].Scale
	}
	return result, nil
}

func (c *objectSQLCompiler) compileDateBucket(value *sqlparser.FuncExpr) (reportmodel.ReportObjectSQLExpression, error) {
	if len(value.Exprs) != 2 {
		return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_function_forbidden", "object_sql_v1.sql", map[string]string{"function": "date_bucket"})
	}
	grain, ok := value.Exprs[0].(*sqlparser.Literal)
	if !ok || grain.Type != sqlparser.StrVal {
		return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_date_bucket_invalid", "object_sql_v1.sql", nil)
	}
	grainValue := strings.ToLower(strings.TrimSpace(grain.Val))
	if !map[string]bool{"hour": true, "day": true, "week": true, "month": true, "quarter": true, "year": true}[grainValue] {
		return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_date_bucket_invalid", "object_sql_v1.sql", map[string]string{"grain": grainValue})
	}
	expression, err := c.compileExpression(value.Exprs[1], false)
	if err != nil {
		return reportmodel.ReportObjectSQLExpression{}, err
	}
	if expression.Type != "date" && expression.Type != "datetime" {
		return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_type_invalid", "object_sql_v1.sql", nil)
	}
	timeZone := "UTC"
	if configured := strings.TrimSpace(c.schema.TimeZone); configured != "" {
		timeZone = configured
	}
	if _, err := time.LoadLocation(timeZone); err != nil {
		return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_timezone_invalid", "object_sql_v1.time_zone", map[string]string{"timezone": timeZone})
	}
	return reportmodel.ReportObjectSQLExpression{Kind: "function", Name: "date_bucket", Value: grainValue, TimeZone: timeZone, Arguments: []reportmodel.ReportObjectSQLExpression{expression}, Type: "datetime"}, nil
}

func (c *objectSQLCompiler) compileAggregate(value sqlparser.AggrFunc) (reportmodel.ReportObjectSQLExpression, error) {
	name := strings.ToLower(value.AggrName())
	if !map[string]bool{"count": true, "sum": true, "avg": true, "min": true, "max": true}[name] {
		return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_function_forbidden", "object_sql_v1.sql", map[string]string{"function": name})
	}
	result := reportmodel.ReportObjectSQLExpression{Kind: "aggregate", Name: name, Type: "integer"}
	if distinct, ok := value.(sqlparser.DistinctableAggr); ok {
		result.Distinct = distinct.IsDistinct()
	}
	for _, raw := range value.GetArgs() {
		expression, err := c.compileExpression(raw, false)
		if err != nil {
			return reportmodel.ReportObjectSQLExpression{}, err
		}
		result.Arguments = append(result.Arguments, expression)
	}
	if name == "count" {
		if len(result.Arguments) > 1 || len(result.Arguments) == 0 && fmt.Sprintf("%T", value) != "*sqlparser.CountStar" {
			return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_function_forbidden", "object_sql_v1.sql", nil)
		}
		return result, nil
	}
	if len(result.Arguments) != 1 {
		return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_function_forbidden", "object_sql_v1.sql", nil)
	}
	argument := result.Arguments[0]
	if (name == "sum" || name == "avg") && !objectSQLNumericType(argument.Type) {
		return reportmodel.ReportObjectSQLExpression{}, objectSQLPlanError("backend.report.object_sql_type_invalid", "object_sql_v1.sql", nil)
	}
	result.Type, result.Precision, result.Scale = argument.Type, argument.Precision, argument.Scale
	return result, nil
}

func objectSQLField(object reportengine.Object, key string) (reportengine.Field, bool) {
	if key == "workspace_id" {
		return reportengine.Field{Key: key, Type: "text"}, true
	}
	if key == "id" {
		return reportengine.Field{Key: key, Type: "text"}, true
	}
	if key == "created_at" || key == "updated_at" {
		return reportengine.Field{Key: key, Type: "datetime"}, true
	}
	for _, field := range object.Fields {
		if strings.TrimSpace(field.Key) == key {
			return field, true
		}
	}
	return reportengine.Field{}, false
}

func objectSQLFieldType(field reportengine.Field) (string, int, int) {
	switch strings.TrimSpace(field.Type) {
	case "text", "email", "long_text", "phone", "relation", "select", "url", "user":
		return "text", 0, 0
	case "number":
		return "number", 0, 0
	case "percent":
		precision, scale := objectSQLDecimalFieldConfig(field)
		return "decimal", precision, scale
	case "boolean", "date", "datetime", "decimal", "integer":
		return strings.TrimSpace(field.Type), 0, 0
	case "currency":
	default:
		return "", 0, 0
	}
	precision, scale := objectSQLDecimalFieldConfig(field)
	return "currency", precision, scale
}

func objectSQLDecimalFieldConfig(field reportengine.Field) (int, int) {
	precision, scale := field.Precision, int(field.Scale)
	if precision <= 0 {
		precision = 19
	}
	if scale < 0 {
		scale = 2
	}
	return precision, scale
}

func objectSQLNumericType(value string) bool {
	return value == "integer" || value == "decimal" || value == "number" || value == "percent" || value == "currency"
}
func objectSQLComparable(left, right string) bool {
	return left == right || left == "null" || right == "null" || objectSQLNumericType(left) && objectSQLNumericType(right)
}
func objectSQLComparisonOperator(value string) bool {
	return map[string]bool{"=": true, "!=": true, "<>": true, ">": true, ">=": true, "<": true, "<=": true}[value]
}

func objectSQLArithmeticType(operator string, left, right reportmodel.ReportObjectSQLExpression) (string, int, int, bool) {
	if !objectSQLNumericType(left.Type) || !objectSQLNumericType(right.Type) {
		return "", 0, 0, false
	}
	if left.Type == "currency" || right.Type == "currency" {
		if operator == "/" && left.Type == "currency" && (right.Type == "integer" || right.Type == "decimal") {
			return "currency", left.Precision, left.Scale, true
		}
		if operator != "+" && operator != "-" || left.Type != "currency" || right.Type != "currency" || left.Scale != right.Scale {
			return "", 0, 0, false
		}
		return "currency", max(left.Precision, right.Precision), left.Scale, true
	}
	if left.Type == "number" || right.Type == "number" {
		return "number", 0, 0, true
	}
	if left.Type == "decimal" || right.Type == "decimal" || operator == "/" {
		return "decimal", 0, 0, true
	}
	return "integer", 0, 0, true
}

func objectSQLMergeExpressionType(target *reportmodel.ReportObjectSQLExpression, branch reportmodel.ReportObjectSQLExpression) bool {
	if target.Type == "" || target.Type == "null" {
		target.Type, target.Precision, target.Scale = branch.Type, branch.Precision, branch.Scale
		return true
	}
	if branch.Type == "null" {
		return true
	}
	if objectSQLNumericType(target.Type) && objectSQLNumericType(branch.Type) && (target.Type == "number" || branch.Type == "number") {
		target.Type, target.Precision, target.Scale = "number", 0, 0
		return true
	}
	return target.Type == branch.Type && (target.Type != "currency" || target.Scale == branch.Scale)
}

func objectSQLCoerceCurrencyPair(left, right *reportmodel.ReportObjectSQLExpression) {
	if left.Type == "currency" && (right.Kind == "parameter" && right.Type == "decimal" || right.Kind == "literal" && right.Value == "0") {
		right.Type, right.Precision, right.Scale = "currency", left.Precision, left.Scale
	}
	if right.Type == "currency" && (left.Kind == "parameter" && left.Type == "decimal" || left.Kind == "literal" && left.Value == "0") {
		left.Type, left.Precision, left.Scale = "currency", right.Precision, right.Scale
	}
}

func objectSQLCoerceCaseBranch(target reportmodel.ReportObjectSQLExpression, branch *reportmodel.ReportObjectSQLExpression) {
	if target.Type == "currency" && branch.Kind == "literal" && branch.Value == "0" {
		branch.Type, branch.Precision, branch.Scale = "currency", target.Precision, target.Scale
	}
}
