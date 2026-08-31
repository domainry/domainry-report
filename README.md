# domainry-report

Source-owned Report module for HTTP delivery, query and Object SQL use cases,
dataset planning and calculation, governed export entrypoints, definition
synchronization, and snapshot orchestration/persistence.

## Layout

- `internal/application/report`: deployment-neutral use-case composition
- `internal/domain/report`: Report model vocabulary, repository ports, dataset/Object SQL policy, and calculation services
- `internal/adapter/reportsdk`: Report SDK binding and Report-owned Foundation `modulehttp.Surface`
- `internal/assembly/module`: embedded-module composition
- `internal/infrastructure/persistence/database`: Report stores, migrations, and schema DDL
- `module`: thin public embedded-module facade
- `contract`, `query`: compatibility facades used by current Runtime releases

Report is embedded through `domainry-report-sdk/modulehost.ApplicationHost`.
The host owns database/transaction selection, dialect, migration locking,
authenticated subject resolution, authorized record/SQL access, shared audit,
and cross-owner transaction adapters. Report owns four public product HTTP
operations—the summary query, Object SQL query, snapshot refresh, and governed
export preparation endpoints—together with their OpenAPI/governance
declarations, authorization decisions, dataset/Object SQL rules, stable
pagination, snapshot consistency, and export-entrypoint semantics. Once an
export is submitted, Data Exchange owns its job, cancellation, artifact, and
download lifecycle through the canonical `/data-exchange/jobs/*` surface;
Report does not publish parallel `/report-exports/*` aliases. The asynchronous
provider resolves and authorizes current definitions through Report's
`Exports.ResolveExecution`, executes pages through `Exports.ReadPage`, and
checks watermarks through `Exports.SourceVersion`; Runtime's schema projection
and host-side code are not a second online definition or execution engine.

Runtime mounts Report's Surface and supplies host adapters; it must not copy
Report handlers, OpenAPI paths, endpoint policies, or application/domain
orchestration. This repository does not currently expose a standalone SaaS
server, so adding empty `cmd`, `assembly/saas`, or dialect directories would
imply a deployment mode that is not implemented.

## Verification

```sh
go test ./...
```

Until the protocol-v3 Report SDK changes are released under a module tag,
standalone `GOWORK=off` verification must use the release consumer's temporary
module/proxy flow. The repository intentionally does not add a local `replace`.
