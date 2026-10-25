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
merge_group)
    case "${DECISION:-}" in
    reuse)
        for result do [ "$result" = skipped ] || { echo 'unexpected reuse execution result' >&2; exit 1; }; done
        echo 'Queue qualification reuses complete base execution; this is not release execution evidence.'
        ;;
    execute)
        for result do [ "$result" = success ] || { echo "required heavy job did not succeed: $result" >&2; exit 1; }; done
        echo 'All required heavy execution jobs succeeded.'
        ;;
    *) echo 'missing or unknown queue qualification decision' >&2; exit 1 ;;
    esac
    ;;
workflow_dispatch)
    [ "${DECISION:-}" = execute ] || { echo 'manual dispatch must execute' >&2; exit 1; }
    for result do [ "$result" = success ] || { echo "required heavy job did not succeed: $result" >&2; exit 1; }; done
    echo 'All required heavy execution jobs succeeded.'
    ;;
*) echo 'unsupported heavy event' >&2; exit 1 ;;
esac
