#!/bin/sh
# Settle one disposable Authentik proof job before releasing its queue lock.
# Pass the exact CI_HEAVY_ID used by the maintained runner. No prefix or prune.
set -eu
[ "$#" -eq 1 ] || exit 2
case $1 in ''|*[!a-zA-Z0-9_-]*) exit 2 ;; esac
project=apptown-oidc-proof-$1
inventory() {
 if [ "$1" = container ]; then
  docker container ls --all --quiet --filter "label=com.docker.compose.project=$project"
 else
  docker "$1" ls --quiet --filter "label=com.docker.compose.project=$project"
 fi
}
for kind in container volume network; do
 ids=$(inventory "$kind")
 for id in $ids; do
  if [ "$kind" = container ]; then
   docker container rm --force "$id" >/dev/null
  else
   docker "$kind" rm "$id" >/dev/null
  fi
 done
done
for kind in container volume network; do
 remaining=$(inventory "$kind")
 [ -z "$remaining" ] || exit 1
done
