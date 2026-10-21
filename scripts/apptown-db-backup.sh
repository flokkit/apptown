#!/bin/bash
# Dump AppTown's database to a gzipped SQL file and refuse to call it a
# backup unless it is one. A deploy script can run this on AppTown's host
# before it restarts AppTown (piped over ssh, so the host runs this
# checkout's copy); apptown-recovery-rehearsal.sh does the same on its source.
#
#   apptown-db-backup.sh <container> <user> <database> <directory> <label>
#
# Writes <directory>/apptown-pre-<label>-<UTC stamp>.sql.gz and prints its path
# and size. Exits non-zero, leaving no file behind, when pg_dump or gzip fails
# or the dump is not a whole AppTown database. A pipeline without pipefail once
# produced a 20-byte "backup" (gzip of nothing) and nobody noticed.
set -euo pipefail

if [ $# -ne 5 ] || [ -z "$5" ]; then
	echo "usage: apptown-db-backup.sh <container> <user> <database> <directory> <label>" >&2
	exit 2
fi
container=$1 user=$2 db=$3 dir=$4 label=$5

# What a whole AppTown dump contains. pg_dump writes the "dump complete" footer
# only after the last table, so a truncated dump has none. An AppTown database
# has about a hundred tables; an empty or wrong database dumps cleanly with a
# footer but few or no COPY sections, so a minimum table count catches it. A
# size floor would not: an empty AppTown schema is small, but so is a young
# forge's whole database. schema_version is AppTown's own table, so its COPY
# says this is AppTown's database rather than some other.
min_tables=50

stamp=$(date -u +%Y%m%dT%H%M%SZ)
out=$dir/apptown-pre-$label-$stamp.sql.gz
partial=$out.partial
mkdir -p "$dir"
trap 'rm -f "$partial"' EXIT

fail() {
	echo "error: backup failed: $*" >&2
	exit 1
}

docker exec "$container" pg_dump -U "$user" -d "$db" | gzip >"$partial" ||
	fail "pg_dump of $db in $container as $user did not succeed"
gzip -t "$partial" || fail "$partial is not a valid gzip file"

# A plain assignment keeps the pipeline's status (read < <(...) would drop
# it), so a decompression error after the markers still fails the backup.
counts=$(gzip -dc "$partial" | awk '
	/^COPY /                                   { tables++ }
	/^COPY public\.schema_version /            { versioned = 1 }
	/^-- PostgreSQL database dump complete/    { complete = 1 }
	END { print tables + 0, versioned + 0, complete + 0 }') ||
	fail "could not read $partial back"
read -r tables versioned complete <<<"$counts"

[ "$complete" = 1 ] || fail "the dump has no 'dump complete' footer: it is truncated"
[ "$tables" -ge "$min_tables" ] ||
	fail "the dump has $tables tables, fewer than the $min_tables an AppTown database has"
[ "$versioned" = 1 ] || fail "the dump has no schema_version table: it is not an AppTown database"

mv "$partial" "$out"
echo "backup: $out ($(du -h "$out" | cut -f1), $tables tables)"
