# Historical source and migration support

Status: current implementation. Checked on 18 September 2026.

Current startup accepts the native application contract and the supported
PostgreSQL schema baseline. It does not carry a general compatibility layer for
all former application formats or historical database versions.

Historical applications need valid packaging at their selected revision. The
one-time native-source cutover
normalized the recorded estate and retained original evidence. That maintenance
operation is not part of every application launch and must not be replayed blindly.
Application bugs and unavailable external dependencies can still prevent an old
version running.

Database upgrades use checked numbered migrations from a supported baseline.
Earlier backups require the explicitly documented maintenance path; see
[database support](postgres-migration.md). A new runtime must not guess missing
source or attempt inputs from current project head.

Versioned runtime capsules, broad historical interpreters and automatic semantic
application migrations in the earlier proposal are not implemented guarantees.
Future compatibility work needs an explicit supported range and a concrete
migration/recovery procedure.

## Implementation references

- [internal/deploy/appcontract/parse.go](../../internal/deploy/appcontract/parse.go)
- [internal/forge/dbschema/schema.go](../../internal/forge/dbschema/schema.go)
- [internal/forge/deployment/settlement_inputs.go](../../internal/forge/deployment/settlement_inputs.go)
