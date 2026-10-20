package reportsdk

import (
	"net/http/httptest"
	"testing"

	sdk "github.com/domainry/domainry-report-sdk"
)

type classificationCapture struct {
	*httptest.ResponseRecorder
	kind, code string
}

func (capture *classificationCapture) RecordError(kind, code string) {
	capture.kind, capture.code = kind, code
}

func TestReportErrorClassificationMatrix(t *testing.T) {
	tests := []struct {
		status int
		kind   string
		code   string
	}{
		{503, "unavailable", "backend.report.database_unavailable"},
		{502, "unavailable", "backend.report.source_unavailable"},
		{409, "conflict", "backend.report.snapshot_fence_rejected"},
		{400, "bad_request", "backend.report.query_invalid"},
		{403, "forbidden", "backend.report.permission_denied"},
	}
	for _, test := range tests {
		capture := &classificationCapture{ResponseRecorder: httptest.NewRecorder()}
		writeReportError(capture, &sdk.Error{StatusCode: test.status, Code: test.code})
		if capture.kind != test.kind || capture.code != test.code {
			t.Fatalf("status=%d kind=%q code=%q", test.status, capture.kind, capture.code)
		}
	}
}
