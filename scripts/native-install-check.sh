#!/bin/sh
# A release's macOS archive installed natively starts, and starts again on
# its own data:
#
#   sh scripts/native-install-check.sh <apptown-<version>-darwin-<arch>.tar.gz>
#
# on a Mac with Docker (the built-in database runs there). It unpacks the
# archive into a folder of its own, starts bin/apptownd with a new data
# directory and internal sign-in on a loopback port, waits for its health
# check to answer 200 and name the archive's version (X-AppTown-Version),
# stops it, starts it
# again on the same data directory and checks the same, and stops it. Its
# built-in database gets a container and volume named for this run (written
# into the data directory's db.env before the first start), so an AppTown
# already on the Mac is not touched. On every exit it stops the AppTown it
# started and removes what it made: that container and volume, and its
# folder.
set -eu
archive=${1:?usage: sh scripts/native-install-check.sh <apptown-<version>-darwin-<arch>.tar.gz>}
name=$(basename "$archive" .tar.gz)
version=$(printf '%s\n' "$name" | sed -n 's/^apptown-\(.*\)-darwin-[a-z0-9]*$/v\1/p')
[ -n "$version" ] || { echo "native check: $archive is not a darwin release archive" >&2; exit 2; }
port=${NATIVE_CHECK_PORT:-47900}
work=$(mktemp -d)
pid=""
cleanup() {
	status=$?
	[ -z "$pid" ] || { kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; }
	if [ -f "$work/data/db.env" ]; then
		container=$(sed -n 's/^APPTOWN_DB_CONTAINER=//p' "$work/data/db.env")
		volume=$(sed -n 's/^APPTOWN_DB_VOLUME=//p' "$work/data/db.env")
		if docker container inspect "$container" > /dev/null 2>&1; then
			docker rm -f "$container" > /dev/null || { echo "native check: could not remove $container" >&2; status=1; }
		fi
		if docker volume inspect "$volume" > /dev/null 2>&1; then
			docker volume rm "$volume" > /dev/null || { echo "native check: could not remove $volume" >&2; status=1; }
		fi
	fi
	rm -rf "$work"
	exit $status
}
trap cleanup EXIT
# A cancelled check is not a passed one.
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir -p "$work/opt" "$work/data"
run=apptown-native-check-$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')
umask 077
printf 'APPTOWN_DB_URL=postgres://postgres:native-check-only@127.0.0.1:54321/apptown?sslmode=disable\nAPPTOWN_DB_PASSWORD=native-check-only\nAPPTOWN_DB_CONTAINER=%s\nAPPTOWN_DB_VOLUME=%s-data\n' "$run" "$run" > "$work/data/db.env"
umask 022
tar -xzf "$archive" -C "$work/opt" --strip-components=1
start() { # start <which>: apptownd on the data directory; answers and names $version
	"$work/opt/bin/apptownd" -data "$work/data" -shipped "$work/opt/share/apptown/updates" -addr "127.0.0.1:$port" \
		-console-addr "127.0.0.1:$((port + 1))" -auth internal > "$work/apptownd-$1.log" 2>&1 &
	pid=$!
	i=0
	# Until it answers at all; then its answer must be 200.
	until code=$(curl -s -o /dev/null -D "$work/health" -w '%{http_code}' "http://127.0.0.1:$port/healthz"); do
		i=$((i + 1))
		kill -0 "$pid" 2>/dev/null && [ "$i" -lt 120 ] || { echo "native check: the $1 start did not answer; its log:" >&2; tail -20 "$work/apptownd-$1.log" >&2; exit 1; }
		sleep 1
	done
	[ "$code" = 200 ] || { echo "native check: the $1 start's health check did not answer 200 ($code); its log:" >&2; tail -20 "$work/apptownd-$1.log" >&2; exit 1; }
	got=$(tr -d '\r' < "$work/health" | sed -n 's/^[Xx]-[Aa]pp[Tt]own-[Vv]ersion: //p')
	[ "$got" = "$version" ] || { echo "native check: the $1 start names $got, not $version" >&2; exit 1; }
	echo "native check: the $1 start answers as $got"
}
stop() {
	kill "$pid"
	wait "$pid" 2>/dev/null || true
	pid=""
}
start first
stop
start second
stop
echo "native check: $name starts, and starts again on its own data"
