package reportsdk

import (
	"net/http"
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
		class  sdk.ErrorClass
		status int
		kind   string
		code   string
	}{
		{sdk.ErrorClassUnavailable, http.StatusServiceUnavailable, "unavailable", "backend.report.database_unavailable"},
		{sdk.ErrorClassConflict, http.StatusConflict, "conflict", "backend.report.snapshot_fence_rejected"},
		{sdk.ErrorClassBadRequest, http.StatusBadRequest, "bad_request", "backend.report.query_invalid"},
		{sdk.ErrorClassForbidden, http.StatusForbidden, "forbidden", "backend.report.permission_denied"},
	}
	for _, test := range tests {
		capture := &classificationCapture{ResponseRecorder: httptest.NewRecorder()}
		writeReportError(capture, &sdk.Error{Class: test.class, Code: test.code})
		if capture.Code != test.status || capture.kind != test.kind || capture.code != test.code {
			t.Fatalf("status=%d kind=%q code=%q", capture.Code, capture.kind, capture.code)
		}
	}
}
