// Package module is the public embedded-module facade.
package module

import (
	"github.com/domainry/domainry-foundation/schemaownership"
	moduleassembly "github.com/domainry/domainry-report/internal/assembly/module"
)

type Factory = moduleassembly.Factory

func NewFactory() *Factory { return moduleassembly.NewFactory() }

func SchemaOwnership() []schemaownership.Table { return moduleassembly.SchemaOwnership() }

func OwnedTables() []string { return moduleassembly.OwnedTables() }
