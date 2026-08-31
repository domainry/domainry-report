# Development rules

- Keep Report as an independent Go module; do not import Runtime implementation packages.
- Preserve the internal application/domain/adapter/assembly/infrastructure boundaries.
- Keep `module/module.go` as a thin facade over `internal/assembly/module`.
- Embedded Report uses the host database, transaction boundary, SQL dialect,
  migration lock, and the host-owned `_schema_migrations` ledger.
- Persistence DDL and DML must use `github.com/domainry/domainry-orm`; narrowly
  justified raw SQL requires dialect-focused tests.
- Runtime-facing compatibility packages may delegate to internal domain
  services, but must not contain Report business implementation.
