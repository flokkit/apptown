#!/bin/sh
# CI's Go test jobs, each a partition of one tier (ci.yml's test, heavy.yml's
# integration and race jobs):
#   ci-test-partition.sh unit|race|integration <group> [test|list]
# Every package of a tier is in exactly one group; one unknown to the case
# below goes to rest. The internal/apptown package is split by test name
# where one process holding all its tests is the longest job: each name is
# selected by exactly one group, and a new test falls to the last group.
# Results are reused from Go's test cache (ci-test-environment.sh says when
# they may be), except for the tests scripts/ci-uncached-tests.txt lists,
# which run with -count=1 every time: their outcome depends on what they
# download while they run.
set -eu
[ "$#" -ge 2 ] && [ "$#" -le 3 ] || { echo 'usage: ci-test-partition.sh unit|race|integration group [test|list]' >&2; exit 2; }
tier=$1
wanted=$2
action=${3:-test}
case "$action" in test | list) ;; *) echo "unknown partition action" >&2; exit 2 ;; esac
script_dir=$(cd "$(dirname "$0")" && pwd)
root=github.com/flokkit/apptown/internal/apptown
# The Makefile's split of the root package's unit tests (make check-test-root-a).
root_tests_a=$(sed -n 's/^ROOT_TESTS_A = //p' "$script_dir/../Makefile")
[ -n "$root_tests_a" ] || { echo 'ROOT_TESTS_A is not in the Makefile' >&2; exit 1; }
# The root package's integration tests, by name: the long serial ones in a
# and b, everything else (its parallel phase included) in c.
integration_root_a='^TestIntegration(ExternalSignInWithDisposableAuthentik|RestoredStartupCompletesLayoutAndIntent|DeployReconcileAndVHost)$'
integration_root_b='^TestIntegration(AChatOnAWorkerFromOpeningToItsEnd|AgentEnvironmentConsoleLaunch|StaticPreviewBoot|AnImplementationStepBuildsWithPrivateDockerOnAWorker)$'
run=
skip=
# The groups that split the root package by test name; none in the race tier.
split=
case "$tier/$wanted" in
unit/root-a | unit/root-b | unit/subsystems | unit/rest)
    packages=$(go list ./...)
    set --
    split='root-a root-b'
    case "$wanted" in root-a) run=$root_tests_a ;; root-b) skip=$root_tests_a ;; esac
    ;;
race/root | race/backup | race/workflow | race/rest)
    packages=$(go list -race ./internal/... ./cmd/...)
    set -- -race -timeout 30m
    ;;
integration/root-a | integration/root-b | integration/root-c | integration/dependency | integration/worker | integration/rest)
    packages=$(go list -tags=integration ./internal/...)
    set -- -tags=integration -timeout 60m -v
    split='root-a root-b root-c'
    case "$wanted" in
    root-a) run=$integration_root_a ;;
    root-b) run=$integration_root_b ;;
    *) run='^TestIntegration' ;;
    esac
    [ "$wanted" != root-c ] || skip="$integration_root_a|$integration_root_b"
    ;;
*) echo "unknown CI partition: $tier/$wanted" >&2; exit 2 ;;
esac
selected=0
selected_packages=
# Go import paths contain no whitespace or shell glob characters.
set -f
for package in $packages; do
    group=rest
    case "$tier/$package" in
    */"$root")
        group=root
        [ -z "$split" ] || case " $split " in *" $wanted "*) group=$wanted ;; *) group=none ;; esac
        ;;
    unit/"$root"/*) group=subsystems ;;
    race/"$root"/instancebackup | race/"$root"/instancebackup/* | race/"$root"/dataprotection | race/"$root"/dataprotection/*) group=backup ;;
    race/"$root"/workflow | race/"$root"/workflow/* | race/"$root"/workflowexec | race/"$root"/workflowexec/*) group=workflow ;;
    integration/"$root"/dependency | integration/"$root"/dependency/*) group=dependency ;;
    integration/github.com/flokkit/apptown/internal/worker | integration/github.com/flokkit/apptown/internal/worker/*) group=worker ;;
    esac
    if [ "$group" = "$wanted" ]; then
        selected=$((selected + 1))
        selected_packages="$selected_packages $package"
    fi
done
[ "$selected" -gt 0 ] || { echo "empty CI partition: $tier/$wanted" >&2; exit 1; }
if [ "$action" = list ]; then
    for package in $selected_packages; do printf '%s\n' "$package"; done
    exit 0
fi
# The listed tests of this tier in the selected packages (one line each:
# tier, package, test name, then the reason after #).
uncached=
uncached_packages=
while read -r line_tier line_package line_test _; do
    [ -n "$line_tier" ] || continue
    for package in $selected_packages; do
        [ "$line_tier" = "$tier" ] && [ "$line_package" = "$package" ] || continue
        uncached="$uncached${uncached:+|}$line_test"
        case " $uncached_packages " in *" $package "*) ;; *) uncached_packages="$uncached_packages $package" ;; esac
    done
done <<EOF
$(sed -e 's/#.*//' "$script_dir/ci-uncached-tests.txt")
EOF
if [ -n "$uncached" ]; then
    skip="${skip}${skip:+|}^($uncached)\$"
fi
printf 'CI partition %s/%s: %s packages\n' "$tier" "$wanted" "$selected"
status=0
go test "$@" ${run:+-run "$run"} ${skip:+-skip "$skip"} $selected_packages || status=$?
if [ -n "$uncached" ]; then
    printf 'CI partition %s/%s: uncached tests, run every time (scripts/ci-uncached-tests.txt)\n' "$tier" "$wanted"
    go test "$@" -count=1 -run "^($uncached)\$" $uncached_packages || status=$?
fi
exit "$status"
