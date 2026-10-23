#!/bin/sh
# The heavy tiers' jobs in .github/workflows/heavy.yml (integration, journeys,
# opt-in-tiers, race; design/architecture/testing.md), and the release's UI
# tour (images.yml's tour job):
#   ci-heavy.sh test <go test arguments...>
#   ci-heavy.sh tour           make tour, with site/tour and web/dist left in
#                              the checkout, owned by the caller
#   ci-heavy.sh cleanup        (the job's last step; also run on exit)
#   ci-heavy.sh pull           pull every image deploy/ci-heavy/test-images.txt
#                              lists (the step before the tests)
# CI_HEAVY_ID names the job (unique on the Docker host).
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
	failed=
	for ref in $(listed); do
		n=1
		until docker pull -q "$ref" >/dev/null; do
			if [ "$n" -ge 3 ]; then
				echo "::error::could not pull $ref (3 attempts); the heavy tests need it ($images)"
				failed=1
				break
			fi
			n=$((n + 1))
			sleep "${CI_HEAVY_PULL_PAUSE:-10}"
		done
	done
	[ -z "$failed" ] || exit 1
	echo "ci-heavy: pulled $(listed | wc -l | tr -d ' ') images"
	exit 0
fi

name=apptown-ci-${CI_HEAVY_ID:?CI_HEAVY_ID names the job}
tmp=/tmp/$name

cleanup() {
	docker rm -f "$name" >/dev/null 2>&1 || true
	# What the tests' containers wrote is root's; only a root container
	# removes it. The pinned busybox (diskspace.DFImage) is one the
	# integration tests pull anyway.
	docker run --rm -v /tmp:/host-tmp busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e rm -rf "/host-tmp/$name"
}

case "${1:-}" in
cleanup)
	cleanup
	exit 0
	;;
test | tour)
	mode=$1
	shift
	;;
*)
	echo "usage: $0 test <go test arguments...> | tour | cleanup | pull" >&2
	exit 2
	;;
esac

# The agent image the tests run steps in (built from this checkout, so a
# change to deploy/apptown-agent is what is tested), and the test image.
# A runner's Docker layer cache, where it has one, keeps both between jobs.
# The tour runs no step container, so it builds no agent image.
[ "$mode" = tour ] || docker build -q -t apptown-agent deploy/apptown-agent
docker build -q -t apptown-ci-heavy \
	--build-arg PI_VERSION="$(sed -n 's/^ARG PI_VERSION=//p' deploy/apptown-agent/Dockerfile)" \
	- <deploy/ci-heavy/Dockerfile

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
		-v apptown-ci-gomod:/root/go/pkg/mod -v apptown-ci-gocache:/root/.cache/go-build \
		-e GOFLAGS="${GOFLAGS:-}" -e OWNER="$(id -u):$(id -g)" \
		apptown-ci-heavy sh -c 'status=0; make tour || status=$?; chown -R "$OWNER" web/node_modules web/dist site/tour 2>/dev/null; exit $status'
	exit
fi
status=0
{ docker run --rm --name "$name" --init --network host \
	-v /var/run/docker.sock:/var/run/docker.sock \
	-v "$PWD:$PWD:ro" -w "$PWD" \
	-v "$tmp:$tmp" -e TMPDIR="$tmp" \
	-v apptown-ci-gomod:/root/go/pkg/mod -v apptown-ci-gocache:/root/.cache/go-build \
	-e GOFLAGS="${GOFLAGS:-}" \
	apptown-ci-heavy go test "$@" || echo $? >"$tmp/.ci-heavy-status"; } | tee "$tmp/.ci-heavy-go-test.log"
[ -f "$tmp/.ci-heavy-status" ] && status=$(cat "$tmp/.ci-heavy-status")
missing=
for t in ${CI_HEAVY_REQUIRE:-}; do
	grep -q "^[[:space:]]*--- PASS: $t " "$tmp/.ci-heavy-go-test.log" || missing="$missing $t"
done
if [ -n "$missing" ]; then
	echo "ci-heavy: required tests did not pass (skipped, absent or failed):$missing" >&2
	[ "$status" -ne 0 ] || status=1
fi
exit "$status"
