# domainry-report

Source-owned Report module for dataset planning, ObjectSQL planning, in-memory
report calculation, definition synchronization, and snapshot persistence.

## Layout

- `internal/application/report`: deployment-neutral use-case composition
- `internal/domain/report`: Report model vocabulary, repository ports, and query services
- `internal/adapter/reportsdk`: Report SDK binding adapter
- `internal/assembly/module`: embedded-module composition
- `internal/infrastructure/persistence/database`: Report stores, migrations, and schema DDL
- `module`: thin public embedded-module facade
- `contract`, `query`: compatibility facades used by current Runtime releases

Report is embedded through `domainry-report-sdk/modulehost.Host`. The host owns
the database connection, transaction selection, dialect, migration locking, and
the single `_schema_migrations` ledger. Report only submits source-owned
migration statements through the host registrar.

This repository does not currently expose a standalone SaaS server. Unlike
Party SDK, Report SDK has no remote HTTP transport contract yet; adding empty
`cmd`, `assembly/saas`, or dialect directories would imply a deployment mode
that is not implemented.

## Verification

```sh
go test ./...
```

Until the protocol-v2 Report SDK changes are released under a module tag,
standalone `GOWORK=off` verification must use the release consumer's temporary
module/proxy flow. The repository intentionally does not add a local `replace`.
