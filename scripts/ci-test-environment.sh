#!/bin/sh
# What CI's Go test results were produced under, kept with them in GOCACHE:
#   ci-test-environment.sh reuse   keep stored results made under this same environment
#   ci-test-environment.sh fresh   expire every stored result (main's runs)
# Go reuses a result only for the same test binary, flags, environment
# variables read and module files opened. It cannot see what a test reaches
# outside them: the images a test starts by a tag (deploy/ci-heavy/test-images.txt),
# the images CI builds before the tests, the Docker daemon, and the runner's
# tools. This writes those identities as a document; a stored document that
# differs, or none, expires every stored result (go clean -testcache: compiled
# packages stay), so a result is only reused under identical outside inputs.
# Run where go test runs, after the listed images are pulled: an image that
# is not there fails, rather than describing an environment the tests lack.
set -eu
mode=${1:-}
case "$mode" in reuse | fresh) ;; *) echo 'usage: ci-test-environment.sh reuse|fresh' >&2; exit 2 ;; esac
images=deploy/ci-heavy/test-images.txt
cache=$(go env GOCACHE)
[ -d "$cache" ] || mkdir -p "$cache"
stored=$cache/apptown-test-environment
current=$cache/apptown-test-environment.new
{
	# GitHub's hosted runner image; empty off a hosted runner.
	printf 'runner %s %s\n' "${ImageOS:-}" "${ImageVersion:-}"
	printf 'cpus %s\n' "$(getconf _NPROCESSORS_ONLN)"
	printf 'git %s\n' "$(git --version)"
	printf 'docker %s %s\n' "$(docker version --format '{{.Client.Version}}')" "$(docker version --format '{{.Server.Version}}')"
	# One inspect for every listed image; docker names any that is missing.
	refs=$(sed -e 's/#.*//' -e 's/[[:space:]]//g' "$images" | grep -v '^$')
	# Image references contain no whitespace or glob characters.
	set -f
	# shellcheck disable=SC2086
	ids=$(docker image inspect --format '{{.Id}}' $refs) || { echo "ci-test-environment: a listed image is not pulled ($images)" >&2; exit 1; }
	# shellcheck disable=SC2086
	set -- $refs
	for id in $ids; do
		printf 'image %s %s\n' "$1" "$id"
		shift
	done
	set +f
	# The images CI builds from this checkout before the heavy tests. A tier
	# that does not build them records that they are absent.
	for ref in apptown-agent apptown-ci-heavy; do
		if docker image inspect "$ref" >/dev/null 2>&1; then
			printf 'built %s %s\n' "$ref" "$(docker image inspect --format '{{.Id}}' "$ref")"
		else
			printf 'built %s absent\n' "$ref"
		fi
	done
} >"$current"
if [ "$mode" = fresh ]; then
	echo 'ci-test-environment: fresh: every stored test result expires'
	go clean -testcache
elif [ ! -f "$stored" ]; then
	echo 'ci-test-environment: no environment is recorded with the stored results; they expire'
	go clean -testcache
elif ! diff "$stored" "$current"; then
	echo 'ci-test-environment: the stored results were made under another environment (above); they expire'
	go clean -testcache
else
	echo 'ci-test-environment: unchanged; stored results under this environment may be reused'
fi
mv "$current" "$stored"
cat "$stored"
