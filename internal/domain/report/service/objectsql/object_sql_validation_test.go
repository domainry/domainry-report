package objectsql

import (
	"fmt"
	"strings"
	"testing"

	reportmodel "github.com/domainry/domainry-report-sdk/model"
	reportengine "github.com/domainry/domainry-report/internal/domain/report/service/engine"
	"vitess.io/vitess/go/vt/sqlparser"
)

func TestObjectSQLParserBindsRepresentativePOSAggregate(t *testing.T) {
	schema := reportObjectSQLFixture(`SELECT s.store_id AS store_id,
ROUND(SUM(CASE WHEN p.kind = :payment_kind THEN p.amount ELSE 0 END), 2) AS paid_amount,
COUNT(DISTINCT p.id) AS payment_count
FROM sale s LEFT JOIN payment p ON p.sale_id = s.id
WHERE s.created_at >= :from_time
GROUP BY s.store_id
HAVING SUM(p.amount) > :minimum
ORDER BY paid_amount DESC LIMIT 100`)
	schema.SourceObjects = []string{"sale", "payment"}
	schema.Parameters = []reportmodel.ReportObjectSQLParameter{{Key: "payment_kind", Type: "text", Required: true}, {Key: "from_time", Type: "datetime", Required: true}, {Key: "minimum", Type: "decimal", Required: true}}
	schema.ResultSchema = []reportmodel.ReportResultColumnSchema{{Key: "store_id", Type: "text", Kind: "dimension"}, {Key: "paid_amount", Type: "currency", Kind: "measure", Precision: 19, Scale: 2}, {Key: "payment_count", Type: "integer", Kind: "measure"}}
	schema.JoinCardinalities = []reportmodel.ReportObjectSQLCardinality{{Alias: "p", Cardinality: "one_to_many"}}
	plan, err := CompileReportObjectSQL(schema, reportObjectSQLObjects())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Sources) != 2 || plan.Sources[0].ObjectKey != "sale" || plan.Sources[1].Alias != "p" || plan.Sources[1].JoinType != "left" {
		t.Fatalf("sources=%#v", plan.Sources)
	}
	if plan.Where == nil || plan.Having == nil || len(plan.GroupBy) != 1 || len(plan.OrderBy) != 2 || plan.OrderBy[1].Expression.FieldKey != "store_id" || plan.Limit != 100 || plan.NodeCount < 40 {
		t.Fatalf("plan=%#v", plan)
	}
	for _, field := range []string{"amount", "id", "kind", "sale_id"} {
		if !containsObjectSQLString(plan.Sources[1].Fields, field) {
			t.Fatalf("payment fields=%v missing %s", plan.Sources[1].Fields, field)
		}
	}
}

func TestObjectSQLParserRejectsForbiddenCorpus(t *testing.T) {
	base := reportObjectSQLFixture("")
	cases := map[string]string{
		"select star":           `SELECT * FROM sale s`,
		"multiple statements":   `SELECT s.id AS id FROM sale s; DELETE FROM sale`,
		"dml":                   `UPDATE sale SET status = 'x'`,
		"ddl":                   `DROP TABLE sale`,
		"cte":                   `WITH x AS (SELECT id FROM sale) SELECT id AS id FROM x`,
		"union":                 `SELECT s.id AS id FROM sale s UNION SELECT p.id AS id FROM payment p`,
		"subquery":              `SELECT (SELECT MAX(p.amount) FROM payment p) AS id FROM sale s`,
		"window":                `SELECT ROW_NUMBER() OVER (ORDER BY s.id) AS id FROM sale s`,
		"physical schema":       `SELECT s.id AS id FROM runtime.sale s`,
		"system table":          `SELECT s.id AS id FROM sqlite_master s`,
		"unregistered function": `SELECT ABS(s.quantity) AS id FROM sale s`,
		"unqualified field":     `SELECT id AS id FROM sale s`,
		"string literal":        `SELECT s.id AS id FROM sale s WHERE s.status = 'paid'`,
		"derived source":        `SELECT x.id AS id FROM (SELECT id FROM sale) x`,
	}
	for name, sql := range cases {
		t.Run(name, func(t *testing.T) {
			schema := base
			schema.SQL = sql
			_, err := CompileReportObjectSQL(schema, reportObjectSQLObjects())
			if err == nil {
				t.Fatalf("accepted %s", sql)
			}
			if planErr, ok := err.(*reportmodel.ReportObjectSQLPlanError); !ok || !strings.HasPrefix(planErr.Code, "backend.report.") {
				t.Fatalf("err=%T %v", err, err)
			}
		})
	}
}

func TestObjectSQLParserNeutralizesCommentAndInjectionShapes(t *testing.T) {
	for _, sql := range []string{`SELECT s.id AS id FROM sale s -- ; DROP TABLE sale`, `SELECT s.id AS id FROM sale s /* ; DELETE FROM sale */`} {
		schema := reportObjectSQLFixture(sql)
		plan, err := CompileReportObjectSQL(schema, reportObjectSQLObjects())
		if err != nil || len(plan.Sources) != 1 || plan.Sources[0].ObjectKey != "sale" || len(plan.OrderBy) != 1 || plan.OrderBy[0].Expression.FieldKey != "id" {
			t.Fatalf("sql=%q plan=%#v err=%v", sql, plan, err)
		}
	}
	schema := reportObjectSQLFixture(`SELECT s.id AS id FROM sale s; SELECT p.id AS id FROM payment p`)
	if _, err := CompileReportObjectSQL(schema, reportObjectSQLObjects()); err == nil {
		t.Fatal("multiple statement comment bypass accepted")
	}
}

func TestSelectedParserCanRewriteEveryBaseTableAsSecureDerivedSource(t *testing.T) {
	parser := sqlparser.NewTestParser()
	statement, err := parser.Parse(`SELECT s.id AS sale_id, p.id AS payment_id FROM sale s LEFT JOIN payment p ON p.sale_id = s.id`)
	if err != nil {
		t.Fatal(err)
	}
	secure := map[string]string{
		"sale":    `SELECT id FROM sale_physical WHERE workspace_id = :__workspace`,
		"payment": `SELECT id, sale_id FROM payment_physical WHERE workspace_id = :__workspace`,
	}
	rewritten := map[string]bool{}
	sqlparser.Rewrite(statement, func(cursor *sqlparser.Cursor) bool {
		table, ok := cursor.Node().(sqlparser.TableName)
		if !ok {
			return true
		}
		objectKey := table.Name.String()
		trustedSQL, exists := secure[objectKey]
		if !exists {
			return true
		}
		trusted, parseErr := parser.Parse(trustedSQL)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		cursor.Replace(&sqlparser.DerivedTable{Select: trusted.(*sqlparser.Select)})
		rewritten[objectKey] = true
		return false
	}, nil)
	output := sqlparser.String(statement)
	if len(rewritten) != 2 || !strings.Contains(output, "left join") || !strings.Contains(output, "sale_physical") || !strings.Contains(output, "payment_physical") || strings.Contains(output, " from sale as ") || strings.Contains(output, " join payment as ") {
		t.Fatalf("rewritten=%v sql=%s", rewritten, output)
	}
}

func TestObjectSQLParserRejectsJoinAmplificationAndAcceptsQualifiedDuplicateFields(t *testing.T) {
	unsafe := reportObjectSQLFixture(`SELECT SUM(s.amount) AS total FROM sale s LEFT JOIN payment p ON p.sale_id = s.id LIMIT 10`)
	unsafe.SourceObjects = []string{"sale", "payment"}
	unsafe.ResultSchema = []reportmodel.ReportResultColumnSchema{{Key: "total", Type: "currency", Kind: "measure", Precision: 19, Scale: 2}}
	unsafe.JoinCardinalities = []reportmodel.ReportObjectSQLCardinality{{Alias: "p", Cardinality: "one_to_many"}}
	if _, err := CompileReportObjectSQL(unsafe, reportObjectSQLObjects()); objectSQLValidationCode(err) != "backend.report.join_measure_amplification" {
		t.Fatalf("err=%v", err)
	}
	safe := unsafe
	safe.SQL = `SELECT COUNT(DISTINCT s.id) AS sale_count FROM sale s LEFT JOIN payment p ON p.sale_id = s.id WHERE s.id <> p.id LIMIT 10`
	safe.ResultSchema = []reportmodel.ReportResultColumnSchema{{Key: "sale_count", Type: "integer", Kind: "measure"}}
	if _, err := CompileReportObjectSQL(safe, reportObjectSQLObjects()); err != nil {
		t.Fatal(err)
	}
	unsafe.SQL = `SELECT SUM(s.amount) AS total FROM sale s LEFT JOIN payment p ON p.sale_id = s.id`
	unsafe.JoinCardinalities = []reportmodel.ReportObjectSQLCardinality{{Alias: "p", Cardinality: "many_to_one"}}
	if _, err := CompileReportObjectSQL(unsafe, reportObjectSQLObjects()); err != nil {
		t.Fatal(err)
	}
}

func TestObjectSQLParserSupportsP0ExpressionsAndDateBucket(t *testing.T) {
	schema := reportObjectSQLFixture(`SELECT DATE_BUCKET('month', s.created_at) AS month_bucket,
FLOOR(COALESCE(SUM(CASE WHEN s.status = :status THEN s.quantity + 1 ELSE 0 END), 0) / NULLIF(COUNT(*), 0)) AS score
FROM sale s GROUP BY DATE_BUCKET('month', s.created_at) ORDER BY month_bucket LIMIT 12`)
	schema.Parameters = []reportmodel.ReportObjectSQLParameter{{Key: "status", Type: "text", Required: true}}
	schema.ResultSchema = []reportmodel.ReportResultColumnSchema{{Key: "month_bucket", Type: "datetime", Kind: "dimension"}, {Key: "score", Type: "decimal", Kind: "measure"}}
	if _, err := CompileReportObjectSQL(schema, reportObjectSQLObjects()); err != nil {
		t.Fatal(err)
	}
}

func TestObjectSQLCurrencyDivisionPreservesScaleAndRejectsUnsafeDimensions(t *testing.T) {
	schema := reportObjectSQLFixture(`SELECT SUM(s.amount) / NULLIF(COUNT(*), 0) AS average_amount FROM sale s LIMIT 1`)
	schema.ResultSchema = []reportmodel.ReportResultColumnSchema{{Key: "average_amount", Type: "currency", Kind: "measure", Precision: 19, Scale: 2}}
	plan, err := CompileReportObjectSQL(schema, reportObjectSQLObjects())
	if err != nil {
		t.Fatal(err)
	}
	got := plan.Projections[0].Expression
	if got.Type != "currency" || got.Precision != 19 || got.Scale != 2 || got.Operator != "/" {
		t.Fatalf("currency division type=%#v", got)
	}

	decimalDivisor := schema
	decimalDivisor.SQL = `SELECT SUM(s.amount) / NULLIF(:divisor, 0) AS average_amount FROM sale s LIMIT 1`
	decimalDivisor.Parameters = []reportmodel.ReportObjectSQLParameter{{Key: "divisor", Type: "decimal", Required: true}}
	if _, err := CompileReportObjectSQL(decimalDivisor, reportObjectSQLObjects()); err != nil {
		t.Fatalf("currency / decimal rejected: %v", err)
	}

	wrongScale := schema
	wrongScale.ResultSchema = []reportmodel.ReportResultColumnSchema{{Key: "average_amount", Type: "currency", Kind: "measure", Precision: 19, Scale: 3}}
	if _, err := CompileReportObjectSQL(wrongScale, reportObjectSQLObjects()); objectSQLValidationCode(err) != "backend.report.object_sql_result_schema_invalid" {
		t.Fatalf("wrong result scale err=%v", err)
	}

	for name, sql := range map[string]string{
		"currency multiply integer": `SELECT s.amount * s.quantity AS value FROM sale s LIMIT 1`,
		"currency divide currency":  `SELECT s.amount / s.amount AS value FROM sale s LIMIT 1`,
		"integer divide currency":   `SELECT s.quantity / s.amount AS value FROM sale s LIMIT 1`,
	} {
		t.Run(name, func(t *testing.T) {
			unsafe := reportObjectSQLFixture(sql)
			unsafe.ResultSchema = []reportmodel.ReportResultColumnSchema{{Key: "value", Type: "currency", Kind: "measure", Precision: 19, Scale: 2}}
			if _, err := CompileReportObjectSQL(unsafe, reportObjectSQLObjects()); objectSQLValidationCode(err) != "backend.report.object_sql_type_invalid" {
				t.Fatalf("unsafe arithmetic err=%v", err)
			}
		})
	}
}

func TestObjectSQLParserEnforcesComplexityLimits(t *testing.T) {
	base := reportObjectSQLFixture(`SELECT s.id AS id FROM sale s LIMIT 10001`)
	if _, err := CompileReportObjectSQL(base, reportObjectSQLObjects()); objectSQLValidationCode(err) != "backend.report.object_sql_limit_invalid" {
		t.Fatalf("limit err=%v", err)
	}
	base.TimeoutMilliseconds = 30001
	base.SQL = `SELECT s.id AS id FROM sale s`
	if _, err := CompileReportObjectSQL(base, reportObjectSQLObjects()); objectSQLValidationCode(err) != "backend.report.object_sql_timeout_invalid" {
		t.Fatalf("timeout err=%v", err)
	}
	base.TimeoutMilliseconds = 0
	base.SQL = strings.Repeat(" ", reportObjectSQLMaximumLength+1)
	if _, err := CompileReportObjectSQL(base, reportObjectSQLObjects()); objectSQLValidationCode(err) != "backend.report.object_sql_length_invalid" {
		t.Fatalf("length err=%v", err)
	}
	base.SQL = `SELECT s.id AS id FROM sale s WHERE ` + strings.TrimSuffix(strings.Repeat(`s.quantity = 1 OR `, 600), " OR ")
	if _, err := CompileReportObjectSQL(base, reportObjectSQLObjects()); objectSQLValidationCode(err) != "backend.report.object_sql_complexity_exceeded" {
		t.Fatalf("node err=%v", err)
	}

	objects := map[string]reportengine.Object{}
	joins, sources, cardinalities := "", []string{}, []reportmodel.ReportObjectSQLCardinality{}
	for index := range reportObjectSQLMaximumJoins + 2 {
		key := fmt.Sprintf("object_%d", index)
		alias := fmt.Sprintf("o%d", index)
		objects[key] = reportengine.Object{Key: key, Fields: []reportengine.Field{{Key: "parent_id", Type: "text"}}}
		sources = append(sources, key)
		if index > 0 {
			joins += fmt.Sprintf(" INNER JOIN %s %s ON %s.parent_id = o0.id", key, alias, alias)
			cardinalities = append(cardinalities, reportmodel.ReportObjectSQLCardinality{Alias: alias, Cardinality: "many_to_one"})
		}
	}
	joinSchema := reportmodel.ReportObjectSQLSchema{SQL: `SELECT o0.id AS id FROM object_0 o0` + joins, SourceObjects: sources, JoinCardinalities: cardinalities, ResultSchema: []reportmodel.ReportResultColumnSchema{{Key: "id", Type: "text", Kind: "dimension"}}}
	if _, err := CompileReportObjectSQL(joinSchema, objects); objectSQLValidationCode(err) != "backend.report.object_sql_complexity_exceeded" {
		t.Fatalf("join err=%v", err)
	}

	columns, resultSchema := []string{}, []reportmodel.ReportResultColumnSchema{}
	for index := range reportObjectSQLMaximumColumns + 1 {
		key := fmt.Sprintf("c%d", index)
		columns = append(columns, fmt.Sprintf("%d AS %s", index, key))
		resultSchema = append(resultSchema, reportmodel.ReportResultColumnSchema{Key: key, Type: "integer", Kind: "measure"})
	}
	columnSchema := reportmodel.ReportObjectSQLSchema{SQL: `SELECT ` + strings.Join(columns, ", ") + ` FROM sale s`, SourceObjects: []string{"sale"}, ResultSchema: resultSchema}
	if _, err := CompileReportObjectSQL(columnSchema, reportObjectSQLObjects()); objectSQLValidationCode(err) != "backend.report.object_sql_result_schema_invalid" {
		t.Fatalf("column err=%v", err)
	}
}

func reportObjectSQLFixture(sql string) reportmodel.ReportObjectSQLSchema {
	return reportmodel.ReportObjectSQLSchema{SQL: sql, SourceObjects: []string{"sale"}, ResultSchema: []reportmodel.ReportResultColumnSchema{{Key: "id", Type: "text", Kind: "dimension"}}}
}

func reportObjectSQLObjects() map[string]reportengine.Object {
	return map[string]reportengine.Object{
		"sale":    {Key: "sale", Fields: []reportengine.Field{{Key: "store_id", Type: "text"}, {Key: "status", Type: "text"}, {Key: "amount", Type: "currency", Precision: 19, Scale: 2}, {Key: "quantity", Type: "integer"}}},
		"payment": {Key: "payment", Fields: []reportengine.Field{{Key: "sale_id", Type: "text"}, {Key: "kind", Type: "text"}, {Key: "amount", Type: "currency", Precision: 19, Scale: 2}}},
	}
}

func containsObjectSQLString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func objectSQLValidationCode(err error) string {
	if value, ok := err.(*reportmodel.ReportObjectSQLPlanError); ok {
		return value.Code
	}
	return ""
}
