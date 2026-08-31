// Package report coordinates Report use cases without selecting a deployment
// mode or persistence implementation.
package report

import reportrepository "github.com/domainry/domainry-report/internal/domain/report/repository"

type Service struct {
	definitions reportrepository.Definition
	snapshots   reportrepository.Snapshot
}

func NewService(definitions reportrepository.Definition, snapshots reportrepository.Snapshot) Service {
	return Service{definitions: definitions, snapshots: snapshots}
}

func (s Service) Definitions() reportrepository.Definition { return s.definitions }

func (s Service) Snapshots() reportrepository.Snapshot { return s.snapshots }
