#!/bin/sh
# The heavy tiers' jobs in .github/workflows/heavy.yml (integration, journeys,
# opt-in-tiers, race; design/architecture/testing.md):
#   ci-heavy.sh test <go test arguments...>
#   ci-heavy.sh cleanup        (the job's last step; also run on exit)
# CI_HEAVY_ID names the job (unique on the Docker host).
# With CI_HEAVY_REQUIRE set to test names, the job fails unless go test's
# verbose output shows each of them passing: a skipped test, a renamed one
# ("no tests to run") or a missing prerequisite is not evidence.
# The tests run as root in the apptown-ci-heavy image (deploy/ci-heavy)
# beside the host's daemon: the containers AppTown starts run as root and
# write into the folders it bind-mounts, and only root can remove what they
# leave, as on every AppTown that runs containers on Linux. Paths handed to
# the daemon are host paths, so the checkout and the tests' temporary folder
# are mounted at the same path inside; the checkout read-only, so nothing
# root-owned is left in it.
set -eu
name=apptown-ci-${CI_HEAVY_ID:?CI_HEAVY_ID names the job}
tmp=/tmp/$name

cleanup() {
	docker rm -f "$name" >/dev/null 2>&1 || true
	# What the tests' containers wrote is root's; only a root container
	# removes it. busybox:1.37 is one the integration tests pull anyway.
	docker run --rm -v /tmp:/host-tmp busybox:1.37 rm -rf "/host-tmp/$name"
}

case "${1:-}" in
cleanup)
	cleanup
	exit 0
	;;
test)
	shift
	;;
*)
	echo "usage: $0 test <go test arguments...> | cleanup" >&2
	exit 2
	;;
esac

# The agent image the tests run steps in (built from this checkout, so a
# change to deploy/apptown-agent is what is tested), and the test image.
# A runner's Docker layer cache, where it has one, keeps both between jobs.
docker build -q -t apptown-agent deploy/apptown-agent
docker build -q -t apptown-ci-heavy \
	--build-arg PI_VERSION="$(sed -n 's/^ARG PI_VERSION=//p' deploy/apptown-agent/Dockerfile)" \
	- <deploy/ci-heavy/Dockerfile

mkdir -p "$tmp"
trap cleanup EXIT
trap 'exit 143' TERM INT
# Host networking: the tests reach the containers they start on the host's
# loopback ports, and the harness's test PostgreSQL the same way.
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
