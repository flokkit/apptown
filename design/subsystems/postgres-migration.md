# Database initialization and upgrades

Forge uses PostgreSQL. Each owner maintains its schema fragment and named queries;
Dbschema embeds the generated schema bundle and applies central numbered migrations.
River manages its own tables/migrations through the jobs integration.

New databases are initialized from the assembled schema. Existing databases must
meet the supported baseline before normal migrations run. The baseline is
schema 147, the 1.0 baseline ([schema ownership](../architecture/schema-ownership.md));
normal startup refuses an older database rather than upgrading it. Forge refuses
SQLite data rather than silently starting a new database beside it.

`-db-url` or `APPTOWN_DB_URL` selects an external database. With neither configured,
startup manages the built-in PostgreSQL container. Database opening performs
initialization/migration even before the read-serving `-verify-only` mode begins.
That flag disables application recovery and workers, not schema changes.

Schema changes require an owner-fragment edit and the appropriate numbered
migration. Run the generation/check commands in [schema ownership](../architecture/schema-ownership.md).
Back up before an upgrade and use the specific rollback instructions for that
migration. An old binary is not automatically compatible with the new schema.

Normal startup then performs one application repair transaction, before provider
recovery, queue consumers or serving writes. It interrupts abandoned workflow
steps, settles dead-owner River jobs under their saved interruption policy,
reads the resulting queued-release ownership, and repairs orphaned releases and
reviews. Required job receipts commit with these changes; any failure rolls back
the whole transaction and prevents startup. Only committed counts are logged.
`-verify-only` forbids this repair. This requires the old daemon to have exited;
a systemd install's `systemctl restart apptownd` provides that ordering. Overlapping
daemons require a different ownership design. See [background jobs](jobs.md).

The original SQLite-to-PostgreSQL migration design and its deployment observations
are history, not current routine operating instructions.

## Implementation references

- [internal/forge/dbschema/schema.go](../../internal/forge/dbschema/schema.go)
- [internal/forge/db.go](../../internal/forge/db.go)
- [internal/forge/dbboot.go](../../internal/forge/dbboot.go)
- [cmd/apptownd/main.go](../../cmd/apptownd/main.go)
