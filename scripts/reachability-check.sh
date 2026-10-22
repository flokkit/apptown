#!/bin/sh
# What apptownd's main can never reach (architecture.md): the whole-program
# sweep the package-level unused check cannot do. A change may lower the
# count and may not raise it; a change that deletes what it superseded
# records the new number with --tighten in the same commit.
set -e
cd "$(dirname "$0")/.."
DEADCODE="${DEADCODE:-$(go env GOPATH)/bin/deadcode}"
[ -x "$DEADCODE" ] || { echo "deadcode not installed: run make tools"; exit 1; }
BASE=scripts/reachability-baseline
OUT=$(mktemp)
"$DEADCODE" ./cmd/apptownd ./cmd/apptown-worker ./cmd/apptown-gate > "$OUT" 2>/dev/null || true
COUNT=$(grep -c "unreachable func" "$OUT" || true)
if [ "$1" = "--tighten" ] || [ ! -f "$BASE" ]; then
  echo "$COUNT" > "$BASE"; echo "reachability baseline written: $COUNT"
fi
BASELINE=$(cat "$BASE")
if [ "$1" = "-v" ] || [ "$COUNT" -gt "$BASELINE" ]; then
  sed -E 's#^([^:]+/)?([a-z_]+)/[^:]+:.*#\2#' "$OUT" | sort | uniq -c | sort -rn
  [ "$1" = "-v" ] && cat "$OUT"
fi
echo "reachability unreachable $COUNT  baseline $BASELINE  $([ "$COUNT" -le "$BASELINE" ] && echo ok || echo WORSE)"
rm -f "$OUT"
[ "$COUNT" -le "$BASELINE" ]
