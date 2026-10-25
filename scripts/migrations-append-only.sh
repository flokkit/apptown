#!/bin/sh
# The migration lock only grows (internal/apptown/dbschema/migrations.lock,
# TestMigrationsMatchTheirLock): every line released on <base> (default
# origin/main) is still there, in order, so no released migration is
# renumbered, rewritten or removed. The one exception is a baseline
# collapse: a baseline can advance to a released version while preserving
# every released migration above it, in order and byte-for-byte. New
# migrations may follow that retained suffix.
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
# Advancing the floor retires only a released prefix. The retained suffix
# must remain verbatim; a collapse cannot hide edits to newer migrations.
floor=$(printf '%s\n' "$current" | sed -n '1s/^baseline \([0-9][0-9]*\)$/\1/p')
if [ -n "$floor" ]; then
	prefix=$(printf '%s\n' "$released" | awk -v floor="$floor" '
		NR == 1 { old = $2; if (floor == old) found = 1; next }
		$1 == floor { found = 1 }
		$1 > floor { suffix = suffix "\n" $0 }
		END { if (found && floor >= old) printf "baseline %d%s\n", floor, suffix }
	')
	if [ -n "$prefix" ]; then
		case $current in
		"$prefix"|"$prefix
"*) echo "migrations-append-only: baseline $floor with released suffix preserved: ok"; exit 0 ;;
		esac
	fi
fi
echo "migrations-append-only: $lock changes what $base released; a released migration keeps its number and statements (add a new one):" >&2
printf '%s\n' "$released" | diff - "$lock" >&2 || true
exit 1
