#!/bin/sh
# The migration lock only grows (internal/apptown/dbschema/migrations.lock,
# TestMigrationsMatchTheirLock): every line released on <base> (default
# origin/main) is still there, in order, so no released migration is
# renumbered, rewritten or removed. The one exception is a baseline
# collapse: a lock that is only "baseline N", where N is the last version
# <base> reaches, folds every released migration into the schema.
#
#   sh scripts/migrations-append-only.sh [base]
set -eu
base=${1:-origin/main}
lock=internal/apptown/dbschema/migrations.lock
cd "$(dirname "$0")/.."
git rev-parse --verify -q "$base^{commit}" >/dev/null || { echo "migrations-append-only: $base is not here to compare with; fetch it" >&2; exit 2; }
# The base's lock is found by its name, not by this tree's path, so a move
# of the package that holds it is still compared with what was released.
base_lock=$(git ls-tree -r --name-only "$base" | grep '/dbschema/migrations\.lock$' || :)
case $base_lock in
"") echo "migrations-append-only: $base has no lock yet: ok"; exit 0 ;;
*"
"*) echo "migrations-append-only: $base has more than one migrations.lock:" >&2; echo "$base_lock" >&2; exit 1 ;;
esac
released=$(git show "$base:$base_lock")
current=$(cat "$lock")
case $current in
"$released"|"$released
"*) echo "migrations-append-only: ok"; exit 0 ;;
esac
# A collapse: the lock is one baseline line at the last version released.
last=$(printf '%s\n' "$released" | awk 'NR == 1 {n = $2} NR > 1 {n = $1} END {print n}')
if [ "$current" = "baseline $last" ]; then
	echo "migrations-append-only: a baseline collapse at $last: ok"
	exit 0
fi
echo "migrations-append-only: $lock changes what $base released; a released migration keeps its number and statements (add a new one):" >&2
printf '%s\n' "$released" | diff - "$lock" >&2 || true
exit 1
