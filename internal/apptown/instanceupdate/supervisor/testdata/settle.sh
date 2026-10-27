#!/bin/sh
# Shared-queue finalizer for exactly one disposable supervisor proof job.
# Pass the same CI_HEAVY_ID as the immutable test invocation. No prune/prefix scan.
set -eu
[ "$#" -eq 1 ] || exit 2
case $1 in ''|*[!a-zA-Z0-9_-]*) exit 2 ;; esac
for adapter in systemd docker; do
 name=apptown-u2-$1-$adapter
 observer=$(docker container ls -a --filter "name=^/$name-kernel$" --format '{{.ID}}')
 for id in $observer; do docker rm -f "$id" >/dev/null; done
 [ -z "$(docker container ls -a --filter "name=^/$name-kernel$" --format '{{.ID}}')" ] || exit 1
 ids=$(docker container ls -a --filter "name=^/$name$" --format '{{.ID}}')
 for id in $ids; do
  volume=$(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/u2"}}{{.Name}}{{end}}{{end}}' "$id")
  docker rm -f -v "$id" >/dev/null
  if [ -n "$volume" ]; then
   remaining=$(docker volume ls --filter "name=$volume" --format '{{.Name}}')
   for candidate in $remaining; do [ "$candidate" != "$volume" ] || exit 1; done
  fi
 done
 [ -z "$(docker container ls -a --filter "name=^/$name$" --format '{{.ID}}')" ] || exit 1
 if [ -n "$(docker image ls --filter "reference=$name:test" --format '{{.ID}}')" ]; then
  docker image rm "$name:test" >/dev/null
 fi
 [ -z "$(docker image ls --filter "reference=$name:test" --format '{{.ID}}')" ] || exit 1
done
