package contract

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/domainry/domainry-foundation/idempotency"
)

// Report's source-owned wire contract for governed export preparation. The
// embedding host derives enforcement from the matching Action approval and
// idempotency metadata; clients can use these values without depending on
// Runtime implementation packages.
const (
	IdempotencyKeyHeader                   = "Idempotency-Key"
	OperationReasonHeader                  = "X-Operation-Reason"
	OperationConfirmationHeader            = "X-Operation-Confirmation"
	OperationConfirmationConfirmed         = "confirmed"
	IdempotencyKeyRequiredErrorCode        = idempotency.ErrorCodeMissingKey
	OperationReasonRequiredErrorCode       = "operations.reason_required"
	OperationReasonEncodingErrorCode       = "operations.reason_encoding_invalid"
	OperationConfirmationRequiredErrorCode = "operations.confirmation_required"
)

// ApplyExportPrepareHeaders adds every caller-controlled governance header
// required by POST /report/{reportKey}/exports/{objectKey}/prepare. It rejects
// incomplete inputs before the request is sent and always emits the only
// accepted confirmation value.
func ApplyExportPrepareHeaders(header http.Header, idempotencyKey, reason string) error {
	if header == nil {
		return fmt.Errorf("Report export prepare headers are required")
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return fmt.Errorf("Report export prepare %s is required", IdempotencyKeyHeader)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("Report export prepare %s is required", OperationReasonHeader)
	}
	header.Set(IdempotencyKeyHeader, idempotencyKey)
	header.Set(OperationReasonHeader, reason)
	header.Set(OperationConfirmationHeader, OperationConfirmationConfirmed)
	return nil
}
