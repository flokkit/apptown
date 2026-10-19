# The record-store style: how a domain package talks to its tables

This is the house style for every domain package's SQL. It rules out one
hand-written statement per question, column lists kept in Go constants,
filters assembled from string fragments, and a scan function per shape.

## The idea in one paragraph

A table is a record store. The package reads whole rows and writes whole
rows, through named queries that sqlc compiles into Go. It accepts that a
read often brings back columns the caller does not use this time; the
rest of the row is usually wanted a moment later anyway, and one shape per
table keeps Postgres's caches warm instead of feeding them thirty
near-identical statements. The struct that comes back is handed around as
the record. The few questions that are genuinely not "give me the row",
such as counts, joins across the domain's tables and summaries for a page,
are written as named report queries and sit beside the record queries in
the same file. No SQL is built at run time.

## Where the SQL lives

Each domain package owns one generated package underneath it, private to
it by Go's `internal` rule:

    internal/wow/internal/wowsql/queries.sql      the SQL, one file
    internal/wow/internal/wowsql/*.go             generated, never edited

`sqlc.yaml` at the repository root has one block per generated package;
the block is the only place the mapping is declared. `make generate-sql`
regenerates; `make check-generated` (part of `make check`) fails when the
generated code and the SQL disagree. sqlc is pinned to v1.28.0.

Only the owning package imports its generated package. Forge, pages and
other domains never see generated types; they call the package's doors and
get the package's words. A generated package that already exists at the
top level (`releasesql`, `projectsql`) stays where it is until its last
outside importer is gone; new ones go under their owner.

## The four kinds of query

**1. Reads by identity, whole row.** One per way of finding a record.

    -- name: PackByName :one
    SELECT * FROM packs WHERE name = $1;

    -- name: PacksEnabled :many
    SELECT * FROM packs WHERE enabled ORDER BY name;

    -- name: AdoptionsForProject :many
    SELECT * FROM project_adoptions WHERE project_id = $1 ORDER BY revision DESC;

`SELECT *` is the point. sqlc generates one struct per table, and every
read returns it. Do not write a second query for a subset of columns
because a page only shows two of them. The exception is a column known to
be large (a guidance block's body, an artifact's body, a definition's
YAML): a list that would drag megabytes gets a `…Meta` query that leaves
that column out, with a comment on the query saying why.

**2. Reads that lock.** A read-then-decide-then-write operation reads
its row with the lock on the query, and the name says so.

    -- name: PackByNameForUpdate :one
    SELECT * FROM packs WHERE name = $1 FOR UPDATE;

    -- name: LockPacks :exec
    LOCK TABLE packs IN SHARE ROW EXCLUSIVE MODE;

    -- name: LockSecretValue :exec
    SELECT pg_advisory_xact_lock(hashtextextended(jsonb_build_array($1::text, $2::text)::text, 0));

Row locks, table locks and advisory locks are all plain statements and
all live in the query file. Which lock to take before which read is still
the calling function's job, in the order it always was; sqlc gives the
statements, not the choreography. See `postgres-native.md` for when each
kind of lock is the right one, and prefer a unique constraint over an
advisory lock whenever the rule is "one row per identity".

**3. Writes, named for the operation.** An insert returns the whole row,
so the caller holds the record with its defaults and timestamps filled in.
An update sets the columns that operation changes and is named for the
operation, not for the table.

    -- name: InsertPack :one
    INSERT INTO packs (name, spec_json, enabled) VALUES ($1, $2, $3) RETURNING *;

    -- name: SetPackEnabled :one
    UPDATE packs SET enabled = $2 WHERE name = $1 RETURNING *;

    -- name: RevokeGrant :one
    UPDATE grants SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL RETURNING *;

    -- name: DeletePackMembershipsForProject :execrows
    DELETE FROM project_packs WHERE project_id = $1;

A whole-row `SavePack` that sets every editable column is fine when the
operation really is "the person edited the form", and the caller holds
the row lock. What is not fine is one generic update that every caller
uses with different columns nulled out.

**4. Reports, on the side.** Anything that is not a row: a count, a join
across the domain's tables, a summary for a page, a search.

    -- name: RunCountsByWorkflow :many
    SELECT workflow_name, count(*)::bigint AS runs FROM runs WHERE project_id = $1 GROUP BY workflow_name;

    -- name: BlocksMatching :many
    SELECT b.*, length(v.body_md)::bigint AS body_bytes
    FROM guidance_blocks b JOIN guidance_block_versions v ON v.block_id = b.id AND v.version = b.latest_version
    WHERE ($1::text = '' OR b.name LIKE '%' || $1 || '%' OR v.body_md LIKE '%' || $1 || '%')
      AND ($2::text = '' OR (',' || b.tags || ',') LIKE '%,' || $2 || ',%')
    ORDER BY b.name;

sqlc generates a row struct for each report. Reports join only the
domain's own tables (plus `projects` for identity); a report that needs
another domain's table asks that domain for its records and joins in Go.

## Filters: never assembled

A filter with optional parts becomes one of two things. If the table is
small (packs, policies, targets, groups, integrations), read the rows and
filter in Go; that is the over-read, and it is cheaper than a family of
statements. If the table is large (grants, runs, audit lines, guidance
versions), write one query where each optional part is guarded by its
parameter, as `BlocksMatching` above does with `($1::text = '' OR …)`.
Either way there is no `strings.Builder`, no `conds = append(conds, …)`,
no `where` variable, and no column list held in a Go constant.

## Types at the door

The package keeps its exported words (`wow.Pack`, `access.Grant`). Where
a word is exactly the table row, the conversion from the generated struct
is one line, and it is fine for the word to gain fields it did not carry
before; that is the over-read arriving at the door. Where the word is a
different shape (a parsed spec, a joined view), one `record(r wowsql.Pack)
Pack` function per table does the conversion, next to the type. Generated
types do not appear in an exported signature.

Nullable columns: prefer `COALESCE(col, '')::text AS col` in the query to
`sql.NullString` in the word, as the existing query files do. Timestamps
come back as `time.Time` in UTC through the pool's codec.

## Transactions and participants

Nothing changes in shape. A door opens the transaction; a participant
takes `tx *database.Tx`; both bind the generated queries with
`wowsql.New(tx.SQL())` or `wowsql.New(s.db.SQL())`. `SQL()` returns the
handle that still carries the test-only statement fault, so a test that
arms a fault on a fragment of the SQL text still sees the generated
statement fail, and rollback tests keep working. Receipts are written by
`audit.At(now).RecordIn`, stamped by the owner's clock, in the same transaction.
An operation reads its clock once: the rows it writes and its receipt carry
that one reading, passed to helpers that write for it, so a record and the
receipt describing it never disagree by a second.

## What a package looks like

- `queries.sql` reads top to bottom as the package's contract with its
  tables: records first, grouped by table; locks; writes; reports last.
- The Go files hold rules and words, not SQL. `grep -c 'Exec(\`\|QueryRow(\`\|Query(\`\|InsertID(\`' *.go` is zero
  outside tests. `database.QueryRecords` and `RowScanner` are gone from
  the package.
- A place where the over-read is deliberately refused (a body column) says
  why at the site.

## What not to do

- No new `database.QueryRecords` calls, no `…Cols` constants, no scan
  helpers per shape.
- No `?` placeholders in a query file; sqlc's Postgres engine wants `$n`
  or `sqlc.arg(name)`. Name the arguments when there are more than two.
- No editing of generated files; regenerate.
- No reaching across domains inside a query. If the join wants another
  domain's table, that is a door on the other domain.
- No shared database-wide query object, no generated package imported by
  more than its owner.

## The ratchet

`scripts/shape-check.py` counts every statement a package still writes by
hand (`handwritten`), baselined in `scripts/shape-baseline.json`. It may
only go down. Three places are exempt because raw SQL is correct there:
`dbschema` writes the migrations, `database` is the driver layer, and
`dbtest` creates and drops the databases a test runs in.

`complaint` is the worked example of this style: a query file, a
`queries.go` binding it to a pool or transaction, a `record` function turning
the generated row into the package's own words, and no SQL left in the
package's Go.

