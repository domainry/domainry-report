// Package capability exposes Report's source-owned capability contract without
// opening persistence, query executors, or snapshot workers.
package capability

import (
	"github.com/domainry/domainry-foundation/modulecapability"
	reportadapter "github.com/domainry/domainry-report/internal/adapter/reportsdk"
)

type Inputs struct{}

func Open(Inputs) (*modulecapability.StaticBinding, error) {
	return reportadapter.NewCapabilityBinding(reportadapter.ValidateCapabilityCandidate)
}
