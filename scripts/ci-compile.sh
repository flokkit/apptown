#!/bin/sh
# Main's disposable caches: compile the test binaries, never execute them.
set -eu
script_dir=$(cd "$(dirname "$0")" && pwd)
case "${1:-}" in
race | integration)
    [ "$#" -eq 2 ] || exit 2
    packages=$(sh "$script_dir/ci-test-partition.sh" "$1" "$2" list)
    case "$1" in race) flag=-race ;; integration) flag=-tags=integration ;; esac
    ;;
journeys)
    [ "$#" -eq 1 ] || exit 2
    flag=-tags=journey
    packages=$(go list "$flag" ./internal/apptown/)
    ;;
opt-in-tiers)
    [ "$#" -eq 1 ] || exit 2
    flag=-tags=browser,pi,inference_docker
    packages=$(go list "$flag" ./internal/apptown/ ./internal/console/pi/)
    ;;
*) echo 'usage: ci-compile.sh race|integration group OR journeys|opt-in-tiers' >&2; exit 2 ;;
esac
[ -n "$packages" ] || { echo 'no packages to compile' >&2; exit 1; }
output=$(mktemp -d)
trap 'rm -rf "$output"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
set -f
printf 'Compilation only: no test binaries or TestMain execute\n'
for package in $packages; do
    go test -c "$flag" -o "$output/test" "$package"
    rm -f "$output/test"
done
