#!/bin/sh
# A PR check is admission to queue execution, never heavy execution evidence.
set -eu
[ "${REPORT_RESULT:-}" = success ] || { echo 'qualification report failed' >&2; exit 1; }
set -f
set -- ${RESULTS:-}
[ "$#" -eq 3 ] || { echo 'missing heavy job results' >&2; exit 1; }
case "${GITHUB_EVENT_NAME:-}" in
pull_request)
    for result do [ "$result" = skipped ] || { echo 'unexpected PR execution result' >&2; exit 1; }; done
    echo 'PR admission only: full heavy execution is required in the merge queue.'
    ;;
merge_group | workflow_dispatch)
    for result do [ "$result" = success ] || { echo "required heavy job did not succeed: $result" >&2; exit 1; }; done
    echo 'All required heavy execution jobs succeeded.'
    ;;
*) echo 'unsupported heavy event' >&2; exit 1 ;;
esac
