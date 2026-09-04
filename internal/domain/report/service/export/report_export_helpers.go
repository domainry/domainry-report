package export

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/domainry/domainry-foundation/apperror"
	reportcontract "github.com/domainry/domainry-report-sdk/contract"
)

func normalizeScopeProjection(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func CanonicalJSONSHA256(value any) (string, error) {
	return reportcontract.CanonicalJSONSHA256(value)
}

func canonicalJSONEqual(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func SHA256Hex(content []byte) string {
	return reportcontract.SHA256Hex(content)
}

func SafeFilename(reportKey, objectKey string) string {
	return reportcontract.SafeExportFilename(reportKey, objectKey)
}

func exportScopeError(code string) error {
	return &apperror.AppError{Kind: apperror.KindBadRequest, Code: code}
}
