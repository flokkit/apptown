#!/bin/sh
# with-deadline.sh SECONDS COMMAND...: run COMMAND, and if it has not ended
# after SECONDS, stop it and fail, saying so. A browser test that hangs then
# fails with its own output in the log, instead of holding a CI job until the
# job is cancelled and its log is lost.
set -u
limit=$1; shift
"$@" &
pid=$!
( sleep "$limit"; if kill -0 "$pid" 2>/dev/null; then echo "with-deadline: $* did not end within ${limit}s; stopping it" >&2; kill -TERM "$pid" 2>/dev/null; sleep 5; kill -KILL "$pid" 2>/dev/null; fi ) &
watch=$!
wait "$pid"; code=$?
kill "$watch" 2>/dev/null
exit "$code"
