# Earlier revisions and database versions

Startup accepts the native application contract and the supported PostgreSQL
schema baseline. There is no general compatibility layer for former application
formats or older database versions.

An earlier revision of an application boots only if its own revision carries
valid packaging. Application bugs and unavailable external dependencies can
still stop an old version running. A runtime never guesses missing source or
attempt inputs from the project's current head.

Database upgrades use checked numbered migrations from the supported baseline;
see [database support](postgres-migration.md).

Versioned runtime capsules, interpreters for former formats and automatic
semantic migration of applications are not built. Compatibility work beyond the
baseline needs an explicit supported range and a concrete migration and
recovery procedure.

## Implementation references

- [internal/deploy/appcontract/parse.go](../../internal/deploy/appcontract/parse.go)
- [internal/apptown/dbschema/schema.go](../../internal/apptown/dbschema/schema.go)
- [internal/apptown/deployment/settlement_inputs.go](../../internal/apptown/deployment/settlement_inputs.go)
