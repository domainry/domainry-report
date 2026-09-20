# domainry-report

Agent-facing question index and source-owned guides: [`capability/agent/index.json`](capability/agent/index.json).

Source-owned Report module for HTTP delivery, query and Object SQL use cases,
governed export entrypoints, definition synchronization, and snapshot
orchestration/persistence.

## Layout

- `internal/application/report`: deployment-neutral use-case composition
- `internal/domain/report`: Report repository ports and Object SQL/export policy
- `internal/adapter/reportsdk`: Report SDK binding and Report-owned Foundation `modulehttp.Adapter`
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
declarations, authorization decisions, Object SQL rules, stable
pagination, snapshot consistency, and export-entrypoint semantics. Once an
export is submitted, Data Exchange owns its job, cancellation, artifact, and
download lifecycle through the canonical `/data-exchange/jobs/*` adapter;
Report does not publish parallel `/report-exports/*` aliases. The asynchronous
provider resolves and authorizes current definitions through Report's
`Exports.ResolveExecution`, executes pages through `Exports.ReadPage`, and
checks watermarks through `Exports.SourceVersion`; Runtime's schema projection
and host-side code are not a second online definition or execution engine.

Runtime mounts Report's Adapter and supplies host adapters; it must not copy
Report handlers, OpenAPI paths, endpoint policies, or application/domain
orchestration. This repository does not currently expose a standalone SaaS
server, so adding empty `cmd`, `assembly/saas`, or dialect directories would
imply a deployment mode that is not implemented.

## Verification

```sh
go test ./...
go test -race ./module ./internal/application/report ./internal/infrastructure/persistence/database/report
```

Until the protocol-v3 Report SDK changes are released under a module tag,
standalone `GOWORK=off` verification must use the release consumer's temporary
module/proxy flow. The repository intentionally does not add a local `replace`.
CI composes a temporary workspace from the exact protocol-v3 SDK and shared
contract source commits, then runs the full suite, the public Module/host-
database lifecycle, the persistence gate, and the race-sensitive application
boundaries. The temporary workspace can be removed once compatible Report SDK,
Foundation, Identity SDK, Notification SDK, and ORM tags are published.

The tests under `internal/adapter/reportsdk` prove the embedded HTTP Adapter's
route, governance, request/response, and OpenAPI mapping contracts. They are
not Remote E2E tests and do not establish SaaS parity. A Remote Report binding,
service authentication/token validation boundary, independently deployed HTTP
server, and deployment/runtime wiring do not exist in this repository today;
those are explicit blockers for claiming embedded/Remote parity.

Governed export clients should derive request prerequisites from Report's
`report.business` capability OpenAPI. Its
`x-domainry-operation-prerequisites` extension and standard header parameters
are generated from the same Action manifest mounted by Runtime. Go verification
clients can call `contract.ApplyExportPrepareHeaders` to set the stable
idempotency key, auditable reason, and exact confirmation value together.
