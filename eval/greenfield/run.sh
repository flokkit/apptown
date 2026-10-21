#!/bin/sh
# Greenfield gym harness: boot a candidate implementation COLD and run the
# private suite against it. Usage:
#
#   run.sh <app> <workspace-dir> [stage]
#
# app ∈ ledger|clip|roomie|qkue|mdlite. Everything executes inside one
# apptown-agent container: toolchains via the workspace's mise.toml, HTTP
# apps launched via their .apptown/app.json contract (build && start), CLI
# apps built via the contract's build command (contract may omit port).
# The controllable clock runs before the app so CLOCK_URL is live at boot.
#
# Output: the suite's JSON scorecard on stdout (last line), symptoms on
# stderr. Exit 0 iff all checks pass. The caller turns failures into
# symptom-only rework feedback — never test source.
set -e
app=$1; ws=$2; stage=${3:-1}
here=$(cd "$(dirname "$0")" && pwd)
[ -d "$ws" ] || { echo "no workspace $ws" >&2; exit 2; }
[ -f "$here/tests/$app.py" ] || { echo "unknown app $app" >&2; exit 2; }

case "$app" in
  ledger|roomie|qkue) mode=http;;
  clip|mdlite) mode=cli;;
esac

docker run --rm \
  -v "$ws":/work -w /work \
  -v "$here":/gym:ro \
  -v apptown-cache:/apptown-cache \
  -e npm_config_cache=/apptown-cache/npm -e GOMODCACHE=/apptown-cache/gomod -e GOCACHE=/apptown-cache/gobuild \
  apptown-agent sh -c '
set -e
export PATH="$MISE_DATA_DIR/shims:$PATH"
[ -f mise.toml ] && mise install -y -q 2>/dev/null || true
mode='"$mode"'; app='"$app"'; stage='"$stage"'
contract() { python3 -c "import json,sys
try: print(json.load(open(\".apptown/app.json\")).get(sys.argv[1]) or \"\")
except Exception: print(\"\")" "$1"; }
build=$(contract build); start=$(contract start); port=$(contract port)
python3 /gym/kit.py 18099 &            # the clock, before anything else
export CLOCK_URL=http://127.0.0.1:18099
if [ -n "$build" ]; then sh -c "$build" >/tmp/build.log 2>&1 || { echo "BUILD FAILED:" >&2; tail -20 /tmp/build.log >&2; exit 3; }; fi
if [ "$mode" = http ]; then
  [ -n "$start" ] || { echo "no start in .apptown/app.json" >&2; exit 3; }
  port=${port:-8080}
  PORT=$port DATA_DIR=/tmp/data sh -c "mkdir -p /tmp/data; $start" >/tmp/app.log 2>&1 &
  i=0; until python3 -c "import socket;socket.create_connection((\"127.0.0.1\",$port),1)" 2>/dev/null; do
    i=$((i+1)); [ $i -gt 60 ] && { echo "app never came up:" >&2; tail -20 /tmp/app.log >&2; exit 3; }; sleep 1
  done
  BASE_URL=http://127.0.0.1:$port python3 /gym/tests/$app.py $stage
else
  bin=$(ls ./'"$app"' 2>/dev/null || true)
  [ -n "$bin" ] || bin=$(find . -maxdepth 1 -type f -perm -u+x -name "'"$app"'*" | head -1)
  [ -n "$bin" ] || { echo "no binary ./'"$app"' after build" >&2; exit 3; }
  BIN=$bin python3 /gym/tests/$app.py $stage
fi'
