#!/bin/sh
# The heavy tiers' jobs in .github/workflows/heavy.yml (integration, journeys,
# opt-in-tiers, race; design/architecture/testing.md), and the release's UI
# tour (images.yml's tour job):
#   ci-heavy.sh test <go test arguments...>
#   ci-heavy.sh test-partition integration <group>   (scripts/ci-test-partition.sh)
#   ci-heavy.sh tour           make tour, with site/tour and web/dist left in
#                              the checkout, owned by the caller
#   ci-heavy.sh cleanup        (the job's last step; also run on exit)
#   ci-heavy.sh pull           pull every image deploy/ci-heavy/test-images.txt
#                              lists (the step before the tests), four at once
# CI_HEAVY_ID names the job (unique on the Docker host). The tests' temporary
# folder is named from it, and tests read TMPDIR, so CI gives a job the same
# ID in every run (its name: each runner is one job's) for Go's test cache.
# With CI_TEST_RESULTS set (reuse or fresh), ci-test-environment.sh runs in
# the test image before go test, and decides which stored results stand;
# unset (a local run), go test runs as given.
# With CI_HEAVY_REQUIRE set to test names, the job fails unless go test's
# verbose output shows each of them passing: a skipped test, a renamed one
# ("no tests to run") or a missing prerequisite is not evidence.
# The tests' images are pulled before them by the pull step
# (deploy/ci-heavy/test-images.txt), so a registry error fails that step and
# not a test.
# The tests run as root in the apptown-ci-heavy image (deploy/ci-heavy)
# beside the host's daemon: the containers AppTown starts run as root and
# write into the folders it bind-mounts, and only root can remove what they
# leave, as on every AppTown that runs containers on Linux. Paths handed to
# the daemon are host paths, so the checkout and the tests' temporary folder
# are mounted at the same path inside; the checkout read-only, so nothing
# root-owned is left in it.
set -eu
images=deploy/ci-heavy/test-images.txt

# The list's references, without comments or blank lines.
listed() {
	sed -e 's/#.*//' -e 's/[[:space:]]//g' "$images" | grep -v '^$'
}

if [ "${1:-}" = pull ]; then
	# Four pulls at once: one at a time took 70-120 s of every job.
	if ! listed | CI_HEAVY_IMAGES=$images CI_HEAVY_PULL_PAUSE=${CI_HEAVY_PULL_PAUSE:-10} xargs -P 4 -n 1 sh -c '
		n=1
		until docker pull -q "$1" >/dev/null; do
			if [ "$n" -ge 3 ]; then
				echo "::error::could not pull $1 (3 attempts); the heavy tests need it ($CI_HEAVY_IMAGES)"
				exit 1
			fi
			n=$((n + 1))
			sleep "$CI_HEAVY_PULL_PAUSE"
		done' sh; then
		exit 1
	fi
	echo "ci-heavy: pulled $(listed | wc -l | tr -d ' ') images"
	exit 0
fi

name=apptown-ci-${CI_HEAVY_ID:?CI_HEAVY_ID names the job}
tmp=/tmp/$name
probe_server=
probe_client=
probe_image=busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e

cleanup_probes() {
	[ -z "$probe_client" ] || docker rm -f "$probe_client" >/dev/null 2>&1 || true
	[ -z "$probe_server" ] || docker rm -f "$probe_server" >/dev/null 2>&1 || true
	probe_client=
	probe_server=
}

cleanup() {
	cleanup_probes
	docker rm -f "$name" >/dev/null 2>&1 || true
	# What the tests' containers wrote is root's; only a root container
	# removes it. The pinned busybox (diskspace.DFImage) is one the
	# integration tests pull anyway.
	docker run --rm -v /tmp:/host-tmp busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e rm -rf "/host-tmp/$name"
}

case "${1:-}" in
cleanup)
	# A fresh shell has no create results from an interrupted preflight.
	probe_server=$name-network-server
	probe_client=$name-network-client
	cleanup
	exit 0
	;;
test | test-partition | tour)
	mode=$1
	shift
	;;
*)
	echo "usage: $0 test <go test arguments...> | test-partition integration <group> | tour | cleanup | pull" >&2
	exit 2
	;;
esac

case "${CI_TEST_RESULTS:-}" in
'' | reuse | fresh) ;;
*) echo "ci-heavy: CI_TEST_RESULTS must be reuse or fresh" >&2; exit 2 ;;
esac

# CI restores these host paths with actions/cache. Local runs keep named
# volumes; the two stores never contain database or Docker daemon state.
gomod=${CI_HEAVY_GOMOD:-apptown-ci-gomod}
gocache=${CI_HEAVY_GOCACHE:-apptown-ci-gocache}
for cache_path in "${CI_HEAVY_GOMOD:-}" "${CI_HEAVY_GOCACHE:-}"; do
	[ -n "$cache_path" ] || continue
	case "$cache_path" in
	/*) ;;
	*) echo "ci-heavy: cache path must be absolute" >&2; exit 2 ;;
	esac
	case "$cache_path" in
	/ | *:* | *','* | *'
'*) echo "ci-heavy: invalid cache path" >&2; exit 2 ;;
	esac
	mkdir -p "$cache_path"
done

cache_owner() {
	for cache_path in "${CI_HEAVY_GOMOD:-}" "${CI_HEAVY_GOCACHE:-}"; do
		[ -n "$cache_path" ] || continue
		docker run --rm -v "$cache_path:/cache" "$probe_image" \
			chown -R "$(id -u):$(id -g)" /cache || return $?
	done
}

# A Linux runner must reach the same published loopback ports as AppTown.
# Docker Desktop requires its opt-in host networking for this path. Prove it
# before building images or reporting a healthy preview as a journey failure.
trap cleanup_probes EXIT
trap 'exit 143' TERM INT
if ! probe_server=$(docker create --name "$name-network-server" \
	-p 127.0.0.1::8080 "$probe_image" sh -c \
	'mkdir -p /www && printf "%s" "$1" > /www/index.html && exec httpd -f -p 8080 -h /www' sh "$name"); then
	echo "ci-heavy: could not create the network preflight server" >&2
	exit 1
fi
if ! docker start "$probe_server" >/dev/null; then
	echo "ci-heavy: could not start the network preflight server" >&2
	exit 1
fi
if ! mapping=$(docker port "$probe_server" 8080/tcp); then
	echo "ci-heavy: could not read the network preflight port" >&2
	exit 1
fi
case "$mapping" in
127.0.0.1:*) port=${mapping#127.0.0.1:} ;;
*) echo "ci-heavy: network preflight is not published on loopback: $mapping" >&2; exit 1 ;;
esac
case "$port" in
'' | *[!0-9]*) echo "ci-heavy: invalid loopback mapping for network preflight: $mapping" >&2; exit 1 ;;
esac
if ! probe_client=$(docker create --name "$name-network-client" --network host "$probe_image" sh -c \
	'for n in 1 2 3 4 5; do
		if body=$(wget -q -T 2 -O - "$1"); then printf "%s" "$body"; exit 0; fi
		sleep 1
	done
	exit 1' sh "http://127.0.0.1:$port/"); then
	echo "ci-heavy: could not create the network preflight client" >&2
	exit 1
fi
if ! answer=$(docker start -a "$probe_client") || [ "$answer" != "$name" ]; then
	echo "ci-heavy: runner cannot reach its published localhost ports; on Docker Desktop enable Settings > Resources > Network > Enable host networking and apply/restart, then rerun the gate" >&2
	exit 1
fi
cleanup_probes

# The agent image the tests run steps in (built from this checkout, so a
# change to deploy/apptown-agent is what is tested), and the test image.
# A runner's Docker layer cache, where it has one, keeps both between jobs.
# The tour runs no step container, so it builds no agent image.
case "${CI_HEAVY_PREBUILT:-0}" in
0 | 1) ;;
*) echo "ci-heavy: CI_HEAVY_PREBUILT must be 0 or 1" >&2; exit 2 ;;
esac
if [ "${CI_HEAVY_PREBUILT:-0}" = 1 ] && [ "$mode" != tour ]; then
	# CI sets this only after current-checkout builds have loaded both tags
	# into this disposable runner. Missing images are errors, never skips.
	docker image inspect apptown-agent >/dev/null
	docker image inspect apptown-ci-heavy >/dev/null
else
	[ "$mode" = tour ] || docker build -q -t apptown-agent deploy/apptown-agent
	docker build -q -t apptown-ci-heavy \
		--build-arg PI_VERSION="$(sed -n 's/^ARG PI_VERSION=//p' deploy/apptown-agent/Dockerfile)" \
		- <deploy/ci-heavy/Dockerfile
fi

mkdir -p "$tmp"
trap cleanup EXIT
trap 'exit 143' TERM INT
# Host networking: the tests reach the containers they start on the host's
# loopback ports, and the harness's test PostgreSQL the same way.
if [ "$mode" = tour ]; then
	# make tour builds web/ and writes site/tour, so the checkout is mounted
	# read-write here; what it wrote is handed back to the caller, so no
	# root-owned file is left in the checkout.
	docker run --rm --name "$name" --init --network host \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-v "$PWD:$PWD" -w "$PWD" \
		-v "$tmp:$tmp" -e TMPDIR="$tmp" \
		-v "$gomod:/root/go/pkg/mod" -v "$gocache:/root/.cache/go-build" \
		-e TOUR_VERSION="${TOUR_VERSION:-}" -e TOUR_SOURCE="${TOUR_SOURCE:-}" \
		-e GOFLAGS="${GOFLAGS:-}" -e OWNER="$(id -u):$(id -g)" \
		apptown-ci-heavy sh -c 'status=0; make tour || status=$?; chown -R "$OWNER" web/node_modules web/dist site/tour 2>/dev/null; exit $status'
	exit
fi
case "$mode" in
 test) set -- go test "$@" ;;
 test-partition) set -- sh scripts/ci-test-partition.sh "$@" ;;
esac
[ -z "${CI_TEST_RESULTS:-}" ] || set -- sh -c 'sh scripts/ci-test-environment.sh "$CI_TEST_RESULTS" && exec "$@"' sh "$@"
status=0
{ docker run --rm --name "$name" --init --network host \
	-v /var/run/docker.sock:/var/run/docker.sock \
	-v "$PWD:$PWD:ro" -w "$PWD" \
	-v "$tmp:$tmp" -e TMPDIR="$tmp" \
	-v "$gomod:/root/go/pkg/mod" -v "$gocache:/root/.cache/go-build" \
	-e GOFLAGS="${GOFLAGS:-}" -e GOMAXPROCS="${GOMAXPROCS:-4}" \
	-e CI_TEST_RESULTS="${CI_TEST_RESULTS:-}" -e ImageOS="${ImageOS:-}" -e ImageVersion="${ImageVersion:-}" \
	apptown-ci-heavy "$@" || echo $? >"$tmp/.ci-heavy-status"; } | tee "$tmp/.ci-heavy-go-test.log"
[ -f "$tmp/.ci-heavy-status" ] && status=$(cat "$tmp/.ci-heavy-status")
missing=
for t in ${CI_HEAVY_REQUIRE:-}; do
	grep -q "^[[:space:]]*--- PASS: $t " "$tmp/.ci-heavy-go-test.log" || missing="$missing $t"
done
if [ -n "$missing" ]; then
	echo "ci-heavy: required tests did not pass (skipped, absent or failed):$missing" >&2
	[ "$status" -ne 0 ] || status=1
fi
cache_owner
exit "$status"
