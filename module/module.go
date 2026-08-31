// Package module is the public embedded-module facade.
package module

import moduleassembly "github.com/domainry/domainry-report/internal/assembly/module"

type Factory = moduleassembly.Factory

func NewFactory() *Factory { return moduleassembly.NewFactory() }
