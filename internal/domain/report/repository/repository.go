// Package repository owns the persistence ports consumed by Report use cases.
package repository

import sdkpersistence "github.com/domainry/domainry-report-sdk/persistence"

type Definition = sdkpersistence.DefinitionRepository
type Snapshot = sdkpersistence.SnapshotRepository
