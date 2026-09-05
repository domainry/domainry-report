package report

import (
	"reflect"
	"testing"

	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

func TestReportDataPermissionsComeFromParsedSQLSources(t *testing.T) {
	report := reportmodel.ReportSchema{ObjectSQLV1: &reportmodel.ReportObjectSQLSchema{
		SQL: `SELECT s.id AS sale_id FROM sale s LEFT JOIN payment p ON p.sale_id = s.id`,
	}, RequiredPermissions: []string{"finance.read", "sale.read"}}
	if got := reportDataPermissionKeys(report); !reflect.DeepEqual(got, []string{"finance.read", "payment.read", "sale.read"}) {
		t.Fatalf("permissions=%v", got)
	}
}

func TestReportEngineObjectPreservesJoinProofMetadata(t *testing.T) {
	object := reportEngineObject(reportmodel.ReportSourceObject{Key: "payment", Fields: []reportmodel.ReportSourceField{{
		Key: "sale_id", Type: "relation", Unique: true, RelationTarget: "sale", RelationCardinality: "one_to_one",
	}}})
	if len(object.Fields) != 1 || !object.Fields[0].Unique || object.Fields[0].RelationTarget != "sale" || object.Fields[0].RelationCardinality != "one_to_one" {
		t.Fatalf("object=%#v", object)
	}
}
