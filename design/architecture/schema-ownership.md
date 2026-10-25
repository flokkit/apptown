# Table ownership and schema assembly

Each table is declared in its owner's `internal/apptown/<owner>/schema.sql`.
Operational SQL writes belong to that owner, including its private generated
query packages. The App composes operations and transaction participants; a
facade does not gain permission to write its participants' tables directly.

`internal/apptown/dbschema/fragments.json` lists the current DDL fragments in
foreign-key order. `dbschema/version.sql` owns only the schema stamp. The
assembler concatenates the fragments into the checked-in `dbschema/schema.sql`
bundle and supplies the same ordered schema list to each sqlc query entry.
This is data assembly: dbschema never imports a domain service to obtain DDL.
Normal Go builds embed the generated bundle and need no generation step.

To change the schema:

1. Edit the owning fragment and append the next central numbered migration
   in `dbschema/schema.go`, and its line in `dbschema/migrations.lock`
   (`TestMigrationsMatchTheirLock` prints it). Moving existing DDL without
   changing its meaning does not need a migration.
2. For a new owner, add its fragment to the manifest after its foreign-key
   dependencies. Keep table indexes and constraints beside their table.
3. Run `make generate-sql`. The generated bundle and sqlc schema lists are
   checked by `make check-generated`, which is part of `make check-go`.
4. Add the data/upgrade tests appropriate to the change and run the required
   Go, shape and reachability gates on the private test database.

The assembler checks missing, duplicated and unlisted fragments, duplicate
application tables, stale generated output and stale sqlc schema lists. Table
ownership is derived from the declaring fragment; there is no parallel table
registry to keep aligned. Current fragments use canonical unquoted, lowercase
public table declarations. Unsupported declaration syntax is refused rather
than silently excluded from ownership.

A new table's key is a UUIDv7 minted in Go
([mergeable apptown data](../decisions/mergeable-forge-data.md)), never an integer
identity: `TestNoNewIntegerIdentity` refuses one, and
`internal/apptown/dbschema/integer-identity-baseline.txt` is empty: no integer
identity remains.

Schema 167 (1.0.0-rc.7) is the oldest supported installation. Earlier
migrations are folded into the fragments; their history remains in Git.
Migrations above the baseline only append: a released migration keeps its
number and statements. `migrations.lock` records each,
`TestMigrationsMatchTheirLock` holds the chain to it, and
`make check-migrations` compares the lock with `origin/main` before landing.
A deliberate baseline advance may retire a released prefix, but must retain
every released migration above the new baseline verbatim and in order.
The baseline must itself be a released version; new migrations may follow
the retained suffix. The gate's isolated Git tests cover these boundaries.

The Go initializer and its supported migration baseline remain central. Startup
changes each migration's DDL, data and version together. River's own migration
and table lifecycle remain with the job library. `internal/dbtest` still takes
an initializer from its caller; the generic library does not import AppTown.

## What the write gate proves

`python3 scripts/schema-ownership.py` checks literal INSERT, UPDATE and DELETE
targets against the fragments. Go's parser reads string literals, so comments
and generated Go query copies are not counted as extra writes. Named
statements in every query file `sqlc.yaml` gives sqlc are attributed to their
enclosing owner, including
writes inside CTEs. Unknown tables and writes by another package fail. Tests
and testdata are excluded from this production check.

The only broad SQL exception is the numbered `migrations` declaration in the
central schema file. New runtime helpers in that package get no exemption.
The obsolete pre-migration key exception has been removed; there are currently
zero foreign operational literal-write exceptions.

`schema-write-exceptions.json` also inventories dynamic Exec arguments. Each
review records the source declaration's fingerprint, expression, possible owned
tables and reason. Changing that declaration or adding an unknown expression
fails the gate until the review is updated. A closed table-name list in a
maintenance participant is recorded this way, as are the database forwarding
adapters and test-database lifecycle calls. Stale reviews also fail.

This is intentionally bounded static analysis, not a SQL parser or a database
security boundary. It does not follow arbitrary dataflow through helpers, prove
all dynamically constructed queries, or enforce DDL/TRUNCATE/COPY targets.
Query/QueryRow calls containing literal DML are checked, but dynamic query calls
are not an exhaustively checked category. Review such changes explicitly;
do not claim a zero exception count proves every possible write is covered.

sqlc seeing another fragment does not grant its package write ownership. Keep
existing foreign keys and the bounded read/snapshot exceptions in architecture
§11, including Adoption's catalog reports and Work's workflow-source query.
Their tested one-statement snapshots and query counts survive this split.

## Tests

The central schema tests cover migration atomicity, refusal of unsupported
versions, the lock, and each migration after the baseline upgrading a baseline
database. Tests for migrations at or below the baseline are removed with the
migrations.

The Python assembler/ownership tests exercise missing/stale declarations,
foreign and unknown writes, CTEs, generated-query ownership, exact migration
exceptions and changed dynamic-write reviews. Existing package suites still
initialize the complete real schema and test their own operations.
