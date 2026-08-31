package objectsql

import (
	"fmt"
	"strconv"
	"strings"

	reportmodel "github.com/domainry/domainry-report-sdk/model"
	reportengine "github.com/domainry/domainry-report/internal/domain/report/service/engine"
	"vitess.io/vitess/go/vt/sqlparser"
)

const (
	reportObjectSQLMaximumLength  = 32 << 10
	reportObjectSQLMaximumNodes   = 1000
	reportObjectSQLMaximumJoins   = 8
	reportObjectSQLMaximumColumns = 64
	reportObjectSQLMaximumLimit   = 10000
	reportObjectSQLDefaultLimit   = 1000
)

type objectSQLCompiler struct {
	schema          reportmodel.ReportObjectSQLSchema
	objects         map[string]reportengine.Object
	aliases         map[string]reportengine.Object
	aliasOrder      map[string]int
	parameters      map[string]reportmodel.ReportObjectSQLParameter
	cardinality     map[string]string
	projections     map[string]reportmodel.ReportObjectSQLExpression
	declaredObjects map[string]bool
}

// CompileReportObjectSQL parses the canonical object_sql_v1 subset with a
// maintained third-party parser, binds every table/field to published
// metadata, and lowers only allowlisted AST nodes into an internal plan.
func CompileReportObjectSQL(schema reportmodel.ReportObjectSQLSchema, objects map[string]reportengine.Object) (reportmodel.ReportObjectSQLPlan, error) {
	if len(schema.SQL) == 0 || len(schema.SQL) > reportObjectSQLMaximumLength {
		return reportmodel.ReportObjectSQLPlan{}, objectSQLPlanError("backend.report.object_sql_length_invalid", "object_sql_v1.sql", nil)
	}
	if len(schema.ResultSchema) == 0 || len(schema.ResultSchema) > reportObjectSQLMaximumColumns {
		return reportmodel.ReportObjectSQLPlan{}, objectSQLPlanError("backend.report.object_sql_result_schema_invalid", "object_sql_v1.result_schema", nil)
	}
	compiler := objectSQLCompiler{schema: schema, objects: objects, aliases: map[string]reportengine.Object{}, aliasOrder: map[string]int{}, parameters: map[string]reportmodel.ReportObjectSQLParameter{}, cardinality: map[string]string{}, projections: map[string]reportmodel.ReportObjectSQLExpression{}, declaredObjects: map[string]bool{}}
	if err := compiler.compileMetadata(); err != nil {
		return reportmodel.ReportObjectSQLPlan{}, err
	}
	statement, err := sqlparser.NewTestParser().Parse(schema.SQL)
	if err != nil {
		return reportmodel.ReportObjectSQLPlan{}, objectSQLPlanError("backend.report.object_sql_parse_failed", "object_sql_v1.sql", map[string]string{"reason": err.Error()})
	}
	selectStatement, ok := statement.(*sqlparser.Select)
	if !ok {
		return reportmodel.ReportObjectSQLPlan{}, objectSQLPlanError("backend.report.object_sql_statement_forbidden", "object_sql_v1.sql", map[string]string{"statement": fmt.Sprintf("%T", statement)})
	}
	if selectStatement.With != nil || len(selectStatement.Windows) > 0 || selectStatement.Lock != sqlparser.NoLock || selectStatement.Into != nil || selectStatement.Distinct || selectStatement.GroupBy != nil && selectStatement.GroupBy.WithRollup {
		return reportmodel.ReportObjectSQLPlan{}, objectSQLPlanError("backend.report.object_sql_syntax_forbidden", "object_sql_v1.sql", nil)
	}
	nodeCount := 0
	hasWindow := false
	_ = sqlparser.Walk(func(node sqlparser.SQLNode) (bool, error) {
		nodeCount++
		if _, ok := node.(*sqlparser.OverClause); ok {
			hasWindow = true
		}
		return nodeCount <= reportObjectSQLMaximumNodes, nil
	}, statement)
	if nodeCount > reportObjectSQLMaximumNodes {
		return reportmodel.ReportObjectSQLPlan{}, objectSQLPlanError("backend.report.object_sql_complexity_exceeded", "object_sql_v1.sql", map[string]string{"limit": strconv.Itoa(reportObjectSQLMaximumNodes)})
	}
	if hasWindow {
		return reportmodel.ReportObjectSQLPlan{}, objectSQLPlanError("backend.report.object_sql_window_forbidden", "object_sql_v1.sql", nil)
	}
	plan := reportmodel.ReportObjectSQLPlan{Parameters: compiler.parameters, ResultSchema: append([]reportmodel.ReportResultColumnSchema(nil), schema.ResultSchema...), NodeCount: nodeCount}
	if err := compiler.compileSources(selectStatement.From, &plan); err != nil {
		return reportmodel.ReportObjectSQLPlan{}, err
	}
	if err := compiler.compileSelect(selectStatement, &plan); err != nil {
		return reportmodel.ReportObjectSQLPlan{}, err
	}
	if err := compiler.validateAmplification(plan); err != nil {
		return reportmodel.ReportObjectSQLPlan{}, err
	}
	return plan, nil
}

func (c *objectSQLCompiler) compileMetadata() error {
	for index, raw := range c.schema.SourceObjects {
		objectKey := strings.TrimSpace(raw)
		if objectKey == "" || c.declaredObjects[objectKey] || c.objects[objectKey].Key == "" {
			return objectSQLPlanError("backend.report.source_object_not_found", fmt.Sprintf("object_sql_v1.source_objects[%d]", index), map[string]string{"object": objectKey})
		}
		c.declaredObjects[objectKey] = true
	}
	if len(c.declaredObjects) == 0 {
		return objectSQLPlanError("backend.report.source_objects_required", "object_sql_v1.source_objects", nil)
	}
	for index, parameter := range c.schema.Parameters {
		key, parameterType := strings.TrimSpace(parameter.Key), strings.TrimSpace(parameter.Type)
		if key == "" || c.parameters[key].Key != "" || !objectSQLValueType(parameterType) || strings.HasPrefix(key, "__") {
			return objectSQLPlanError("backend.report.object_sql_parameter_invalid", fmt.Sprintf("object_sql_v1.parameters[%d]", index), map[string]string{"parameter": key, "type": parameterType})
		}
		parameter.Key, parameter.Type = key, parameterType
		c.parameters[key] = parameter
	}
	for index, contract := range c.schema.JoinCardinalities {
		alias, cardinality := strings.TrimSpace(contract.Alias), strings.TrimSpace(contract.Cardinality)
		if alias == "" || c.cardinality[alias] != "" || cardinality != "one_to_one" && cardinality != "many_to_one" && cardinality != "one_to_many" {
			return objectSQLPlanError("backend.report.join_cardinality_invalid", fmt.Sprintf("object_sql_v1.join_cardinalities[%d]", index), map[string]string{"alias": alias, "actual": cardinality})
		}
		c.cardinality[alias] = cardinality
	}
	if c.schema.TimeoutMilliseconds < 0 || c.schema.TimeoutMilliseconds > 30000 {
		return objectSQLPlanError("backend.report.object_sql_timeout_invalid", "object_sql_v1.timeout_milliseconds", nil)
	}
	return nil
}

func (c *objectSQLCompiler) compileSources(from []sqlparser.TableExpr, plan *reportmodel.ReportObjectSQLPlan) error {
	if len(from) != 1 {
		return objectSQLPlanError("backend.report.object_sql_source_invalid", "object_sql_v1.sql.from", map[string]string{"reason": "exactly one joined source tree is required"})
	}
	if err := c.compileSourceNode(from[0], plan, true); err != nil {
		return err
	}
	if len(plan.Sources)-1 > reportObjectSQLMaximumJoins {
		return objectSQLPlanError("backend.report.object_sql_complexity_exceeded", "object_sql_v1.sql.from", map[string]string{"join_limit": strconv.Itoa(reportObjectSQLMaximumJoins)})
	}
	if len(c.cardinality) != len(plan.Sources)-1 {
		return objectSQLPlanError("backend.report.join_cardinality_invalid", "object_sql_v1.join_cardinalities", map[string]string{"reason": "every join alias requires exactly one cardinality contract"})
	}
	usedObjects := map[string]bool{}
	for _, source := range plan.Sources {
		usedObjects[source.ObjectKey] = true
	}
	if len(usedObjects) != len(c.declaredObjects) {
		return objectSQLPlanError("backend.report.source_object_invalid", "object_sql_v1.source_objects", map[string]string{"reason": "declared objects must exactly match parsed base objects"})
	}
	for objectKey := range c.declaredObjects {
		if !usedObjects[objectKey] {
			return objectSQLPlanError("backend.report.source_object_invalid", "object_sql_v1.source_objects", map[string]string{"object": objectKey})
		}
	}
	return nil
}

func (c *objectSQLCompiler) compileSourceNode(node sqlparser.TableExpr, plan *reportmodel.ReportObjectSQLPlan, root bool) error {
	switch value := node.(type) {
	case *sqlparser.AliasedTableExpr:
		return c.compileBaseSource(value, plan, root, "", nil)
	case *sqlparser.JoinTableExpr:
		if err := c.compileSourceNode(value.LeftExpr, plan, root); err != nil {
			return err
		}
		joinType := strings.ToLower(strings.TrimSpace(value.Join.ToString()))
		if joinType == "join" || joinType == "inner join" {
			joinType = "inner"
		} else if joinType == "left join" {
			joinType = "left"
		} else {
			return objectSQLPlanError("backend.report.object_sql_join_forbidden", "object_sql_v1.sql.from", map[string]string{"join": joinType})
		}
		right, ok := value.RightExpr.(*sqlparser.AliasedTableExpr)
		if !ok || value.Condition == nil || value.Condition.On == nil || len(value.Condition.Using) > 0 {
			return objectSQLPlanError("backend.report.object_sql_join_invalid", "object_sql_v1.sql.from", nil)
		}
		if err := c.compileBaseSource(right, plan, false, joinType, value.Condition.On); err != nil {
			return err
		}
		return nil
	default:
		return objectSQLPlanError("backend.report.object_sql_source_invalid", "object_sql_v1.sql.from", map[string]string{"node": fmt.Sprintf("%T", node)})
	}
}

func (c *objectSQLCompiler) compileBaseSource(table *sqlparser.AliasedTableExpr, plan *reportmodel.ReportObjectSQLPlan, root bool, joinType string, on sqlparser.Expr) error {
	name, ok := table.Expr.(sqlparser.TableName)
	alias := strings.TrimSpace(table.As.String())
	if !ok || !name.Qualifier.IsEmpty() || alias == "" || len(table.Partitions) > 0 || table.Hints != nil || len(table.Columns) > 0 {
		return objectSQLPlanError("backend.report.object_sql_source_invalid", "object_sql_v1.sql.from", nil)
	}
	objectKey := strings.TrimSpace(name.Name.String())
	object, exists := c.objects[objectKey]
	if !exists || !c.declaredObjects[objectKey] || c.aliases[alias].Key != "" {
		return objectSQLPlanError("backend.report.source_object_not_found", "object_sql_v1.sql.from", map[string]string{"object": objectKey, "alias": alias})
	}
	c.aliases[alias], c.aliasOrder[alias] = object, len(plan.Sources)
	source := reportmodel.ReportObjectSQLSource{ObjectKey: objectKey, Alias: alias, JoinType: joinType}
	if !root {
		source.Cardinality = c.cardinality[alias]
		if source.Cardinality == "" {
			return objectSQLPlanError("backend.report.join_cardinality_invalid", "object_sql_v1.join_cardinalities", map[string]string{"alias": alias})
		}
		expression, err := c.compileExpression(on, false)
		if err != nil {
			return err
		}
		source.On = &expression
	}
	plan.Sources = append(plan.Sources, source)
	return nil
}

func (c *objectSQLCompiler) compileSelect(statement *sqlparser.Select, plan *reportmodel.ReportObjectSQLPlan) error {
	if statement.SelectExprs == nil || len(statement.SelectExprs.Exprs) == 0 || len(statement.SelectExprs.Exprs) > reportObjectSQLMaximumColumns {
		return objectSQLPlanError("backend.report.object_sql_result_schema_invalid", "object_sql_v1.sql.select", nil)
	}
	if len(statement.SelectExprs.Exprs) != len(c.schema.ResultSchema) {
		return objectSQLPlanError("backend.report.object_sql_result_schema_invalid", "object_sql_v1.result_schema", nil)
	}
	for index, raw := range statement.SelectExprs.Exprs {
		if _, star := raw.(*sqlparser.StarExpr); star {
			return objectSQLPlanError("backend.report.object_sql_star_forbidden", "object_sql_v1.sql.select", nil)
		}
		aliased, ok := raw.(*sqlparser.AliasedExpr)
		alias := ""
		if ok {
			alias = strings.TrimSpace(aliased.As.String())
		}
		result := c.schema.ResultSchema[index]
		if !ok || alias == "" || alias != strings.TrimSpace(result.Key) || c.projections[alias].Kind != "" || !objectSQLResultType(result.Type) || result.Kind != "dimension" && result.Kind != "measure" {
			return objectSQLPlanError("backend.report.object_sql_result_schema_invalid", fmt.Sprintf("object_sql_v1.result_schema[%d]", index), map[string]string{"alias": alias})
		}
		expression, err := c.compileExpression(aliased.Expr, false)
		if err != nil {
			return err
		}
		if !objectSQLResultCompatible(result, expression) {
			return objectSQLPlanError("backend.report.object_sql_result_schema_invalid", fmt.Sprintf("object_sql_v1.result_schema[%d]", index), map[string]string{"declared": result.Type, "actual": expression.Type})
		}
		if result.Type == "decimal" && expression.Type == "decimal" && expression.Precision > 0 {
			plan.ResultSchema[index].Precision = expression.Precision
			plan.ResultSchema[index].Scale = expression.Scale
		}
		c.projections[alias] = expression
		plan.Projections = append(plan.Projections, reportmodel.ReportObjectSQLProjection{Alias: alias, Expression: expression})
	}
	var err error
	if statement.Where != nil {
		expression, compileErr := c.compileExpression(statement.Where.Expr, false)
		err = compileErr
		plan.Where = &expression
	}
	if err != nil {
		return err
	}
	if statement.GroupBy != nil {
		for _, raw := range statement.GroupBy.Exprs {
			expression, compileErr := c.compileExpression(raw, false)
			if compileErr != nil {
				return compileErr
			}
			plan.GroupBy = append(plan.GroupBy, expression)
		}
	}
	if statement.Having != nil {
		expression, compileErr := c.compileExpression(statement.Having.Expr, false)
		if compileErr != nil {
			return compileErr
		}
		plan.Having = &expression
	}
	plan.ExplicitOrderBy = len(statement.OrderBy) > 0
	for _, order := range statement.OrderBy {
		expression, compileErr := c.compileExpression(order.Expr, true)
		if compileErr != nil {
			return compileErr
		}
		direction := strings.ToLower(strings.TrimSpace(order.Direction.ToString()))
		if direction == "" {
			direction = "asc"
		}
		if direction != "asc" && direction != "desc" {
			return objectSQLPlanError("backend.report.object_sql_order_invalid", "object_sql_v1.sql.order_by", nil)
		}
		plan.OrderBy = append(plan.OrderBy, reportmodel.ReportObjectSQLOrder{Expression: expression, Direction: direction})
	}
	plan.Limit = reportObjectSQLDefaultLimit
	plan.ExplicitLimit = statement.Limit != nil
	if statement.Limit != nil {
		if statement.Limit.Offset != nil {
			return objectSQLPlanError("backend.report.object_sql_limit_invalid", "object_sql_v1.sql.limit", nil)
		}
		literal, ok := statement.Limit.Rowcount.(*sqlparser.Literal)
		if !ok {
			return objectSQLPlanError("backend.report.object_sql_limit_invalid", "object_sql_v1.sql.limit", nil)
		}
		value, parseErr := strconv.Atoi(literal.Val)
		if parseErr != nil || value <= 0 || value > reportObjectSQLMaximumLimit {
			return objectSQLPlanError("backend.report.object_sql_limit_invalid", "object_sql_v1.sql.limit", map[string]string{"maximum": strconv.Itoa(reportObjectSQLMaximumLimit)})
		}
		plan.Limit = value
	}
	appendObjectSQLStableOrder(plan)
	c.populateSourceFields(plan)
	return nil
}

// appendObjectSQLStableOrder is a Runtime implementation detail. Authors keep
// writing native SQL; the platform adds only the deterministic tie terms needed
// by opaque cursor traversal and never exposes a report AST or ordering DSL.
func appendObjectSQLStableOrder(plan *reportmodel.ReportObjectSQLPlan) {
	if len(plan.GroupBy) > 0 {
		for _, expression := range plan.GroupBy {
			if !objectSQLOrderContains(plan.OrderBy, expression) {
				plan.OrderBy = append(plan.OrderBy, reportmodel.ReportObjectSQLOrder{Expression: expression, Direction: "asc"})
			}
		}
		return
	}
	for _, projection := range plan.Projections {
		if objectSQLExpressionAggregate(projection.Expression) {
			return // aggregate without GROUP BY has at most one result row
		}
	}
	for _, source := range plan.Sources {
		expression := reportmodel.ReportObjectSQLExpression{Kind: "field", Alias: source.Alias, FieldKey: "id", Type: "text"}
		if !objectSQLOrderContains(plan.OrderBy, expression) {
			plan.OrderBy = append(plan.OrderBy, reportmodel.ReportObjectSQLOrder{Expression: expression, Direction: "asc"})
		}
	}
}

// ReportObjectSQLDefaultLimitRows and ReportObjectSQLMaximumLimitRows expose
// the compiled limit contract to validation layers that explain why authored
// SQL must carry its own literal LIMIT.
const (
	ReportObjectSQLDefaultLimitRows = reportObjectSQLDefaultLimit
	ReportObjectSQLMaximumLimitRows = reportObjectSQLMaximumLimit
)

// ReportObjectSQLPlanSingleRow reports whether the compiled plan can only
// produce a single result row: an aggregate projection without GROUP BY. Such
// plans need no authored ORDER BY or LIMIT; every other plan can return
// multiple rows and silently truncates at the implicit default limit.
func ReportObjectSQLPlanSingleRow(plan reportmodel.ReportObjectSQLPlan) bool {
	if len(plan.GroupBy) > 0 {
		return false
	}
	for _, projection := range plan.Projections {
		if objectSQLExpressionAggregate(projection.Expression) {
			return true
		}
	}
	return false
}

func objectSQLOrderContains(orders []reportmodel.ReportObjectSQLOrder, expression reportmodel.ReportObjectSQLExpression) bool {
	for _, order := range orders {
		candidate := order.Expression
		if candidate.Kind == expression.Kind && candidate.Alias == expression.Alias && candidate.FieldKey == expression.FieldKey && candidate.Name == expression.Name {
			return true
		}
	}
	return false
}

func objectSQLExpressionAggregate(expression reportmodel.ReportObjectSQLExpression) bool {
	if expression.Kind == "aggregate" {
		return true
	}
	for _, argument := range expression.Arguments {
		if objectSQLExpressionAggregate(argument) {
			return true
		}
	}
	for _, when := range expression.Whens {
		if objectSQLExpressionAggregate(when.Condition) || objectSQLExpressionAggregate(when.Value) {
			return true
		}
	}
	return expression.Else != nil && objectSQLExpressionAggregate(*expression.Else)
}

func objectSQLPlanError(code, path string, params map[string]string) error {
	return &reportmodel.ReportObjectSQLPlanError{Code: code, Path: path, Params: params}
}

func objectSQLValueType(value string) bool {
	switch value {
	case "text", "integer", "number", "decimal", "boolean", "date", "datetime":
		return true
	}
	return false
}
func objectSQLResultType(value string) bool { return objectSQLValueType(value) || value == "currency" }
