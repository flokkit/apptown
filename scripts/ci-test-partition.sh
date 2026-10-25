#!/bin/sh
# Exhaustive package partitions for hosted heavy jobs. Unknown packages go
# to rest; selection uses the same build tags as execution. No result cache.
set -eu
[ "$#" -ge 2 ] && [ "$#" -le 3 ] || { echo 'usage: ci-test-partition.sh race|integration group [test|list]' >&2; exit 2; }
tier=$1
wanted=$2
action=${3:-test}
case "$action" in test | list) ;; *) echo "unknown partition action" >&2; exit 2 ;; esac
selected_packages=
case "$tier/$wanted" in
race/root | race/backup | race/workflow | race/rest)
    packages=$(go list -race ./internal/... ./cmd/...)
    set -- -race -count=1 -timeout 30m
    ;;
integration/root | integration/dependency | integration/worker | integration/rest)
    packages=$(go list -tags=integration ./internal/...)
    set -- -tags=integration -run '^TestIntegration' -count=1 -timeout 60m -v
    ;;
*) echo "unknown CI partition: $tier/$wanted" >&2; exit 2 ;;
esac
root=github.com/flokkit/apptown/internal/apptown
selected=0
# Go import paths contain no whitespace or shell glob characters.
set -f
for package in $packages; do
    group=rest
    case "$tier/$package" in
    race/"$root" | integration/"$root") group=root ;;
    race/"$root"/instancebackup | race/"$root"/instancebackup/* | race/"$root"/dataprotection | race/"$root"/dataprotection/*) group=backup ;;
    race/"$root"/workflow | race/"$root"/workflow/* | race/"$root"/workflowexec | race/"$root"/workflowexec/*) group=workflow ;;
    integration/"$root"/dependency | integration/"$root"/dependency/*) group=dependency ;;
    integration/github.com/flokkit/apptown/internal/worker | integration/github.com/flokkit/apptown/internal/worker/*) group=worker ;;
    esac
    if [ "$group" = "$wanted" ]; then
        set -- "$@" "$package"
        selected=$((selected + 1))
        selected_packages="$selected_packages $package"
    fi
done
[ "$selected" -gt 0 ] || { echo "empty CI partition: $tier/$wanted" >&2; exit 1; }
if [ "$action" = list ]; then
    for package in $selected_packages; do printf '%s\n' "$package"; done
    exit 0
fi
printf 'CI partition %s/%s: %s packages\n' "$tier" "$wanted" "$selected"
exec go test "$@"
