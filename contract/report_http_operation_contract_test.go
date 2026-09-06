package contract

import (
	"fmt"
	"net/http"
	"testing"
)

func TestApplyExportPrepareHeaders(t *testing.T) {
	header := make(http.Header)
	if err := ApplyExportPrepareHeaders(header, " export:orders:2026-09-05 ", " approved customer export "); err != nil {
		t.Fatal(err)
	}
	if header.Get(IdempotencyKeyHeader) != "export:orders:2026-09-05" ||
		header.Get(OperationReasonHeader) != "approved customer export" ||
		header.Get(OperationConfirmationHeader) != OperationConfirmationConfirmed {
		t.Fatalf("governed export headers=%#v", header)
	}
	for name, values := range map[string][2]string{
		"missing idempotency key": {"", "approved customer export"},
		"missing reason":          {"export:orders:2026-09-05", "  "},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ApplyExportPrepareHeaders(make(http.Header), values[0], values[1]); err == nil {
				t.Fatal("accepted incomplete governed export headers")
			}
		})
	}
	if err := ApplyExportPrepareHeaders(nil, "export:orders:2026-09-05", "approved customer export"); err == nil {
		t.Fatal("accepted nil governed export headers")
	}
}

func ExampleApplyExportPrepareHeaders() {
	request, _ := http.NewRequest(http.MethodPost, "https://runtime.example/report/sales/exports/order/prepare", nil)
	_ = ApplyExportPrepareHeaders(request.Header, "export:orders:2026-09-05", "approved customer export")
	fmt.Println(request.Header.Get(IdempotencyKeyHeader))
	fmt.Println(request.Header.Get(OperationReasonHeader))
	fmt.Println(request.Header.Get(OperationConfirmationHeader))
	// Output:
	// export:orders:2026-09-05
	// approved customer export
	// confirmed
}
