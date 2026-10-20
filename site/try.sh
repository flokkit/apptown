#!/bin/sh
# Try AppTown on your own computer:
#
#   curl -fsSL https://apptown.dev/try | sh
#
# It starts AppTown in one Docker container called "apptown", with its data
# in a Docker volume of the same name, and opens it in your browser. Read
# this script before you run it; it does nothing else.
#
#   curl -fsSL https://apptown.dev/try | sh -s -- stop       stop it (data kept)
#   curl -fsSL https://apptown.dev/try | sh -s -- uninstall  remove it and its data
#
# The trial has full control of your Docker: the agents and apps it runs are
# containers on this computer. It is for trying AppTown, not for anything
# other people use; for a server, see Running AppTown in the AppTown docs:
# https://github.com/flokkit/apptown/blob/main/stock/docs/guides/running-apptown.md
set -eu

# Replaced with the released version when the site is published.
VERSION=${APPTOWN_VERSION:-@VERSION@}
IMAGE=ghcr.io/flokkit/apptown:$VERSION
NAME=apptown
PORT=7777

say() { printf 'apptown: %s\n' "$*"; }
fail() {
	say "$*" >&2
	exit 1
}

open_link() {
	if command -v open >/dev/null 2>&1; then
		open "$1" >/dev/null 2>&1 || :
	elif command -v xdg-open >/dev/null 2>&1; then
		xdg-open "$1" >/dev/null 2>&1 || :
	fi
}

# Asks on the terminal even when this script arrives on standard input.
confirm() {
	printf 'apptown: %s [y/N] ' "$1"
	read -r answer </dev/tty || answer=''
	case $answer in y | Y | yes) return 0 ;; *) return 1 ;; esac
}

# present <docker inspect args>: 0 if Docker shows the thing, 1 if Docker says
# there is no such thing, and a failure with Docker's message when it cannot
# tell: an unanswered question is never taken for absence.
present() {
	if out=$(docker "$@" 2>&1); then
		return 0
	fi
	# Docker's own not-found answers, naming the thing asked about. A missing
	# socket also says "no such file or directory", which is not an answer.
	case $out in
	*"o such object: $NAME"* | *"o such container: $NAME"* | *"get $NAME: no such volume"*) return 1 ;;
	esac
	fail "could not ask Docker ($*): $out"
}

command -v docker >/dev/null 2>&1 || fail "Docker is not installed. Install Docker Desktop (Mac) or Docker Engine (Linux), then run this again."
docker info >/dev/null 2>&1 || fail "Docker is not running. Start Docker Desktop (or the Docker service), then run this again."

case ${1:-start} in
stop)
	if ! present inspect "$NAME"; then
		say "not installed; nothing to stop."
		exit 0
	fi
	if out=$(docker stop "$NAME" 2>&1); then
		say "stopped; your data is kept. Run the install command again to start it."
		exit 0
	fi
	fail "could not stop it, so it may still be running: $out"
	;;
uninstall)
	confirm "Remove the AppTown trial, its projects and all its data?" || {
		say "nothing removed."
		exit 0
	}
	left=''
	# Each step checks that its thing is there first, so only a real failure
	# to delete is reported, never something that was already gone.
	if present inspect "$NAME"; then
		out=$(docker rm -f "$NAME" 2>&1) || {
			say "could not remove the container: $out" >&2
			left="$left container"
		}
	fi
	if present volume inspect "$NAME"; then
		out=$(docker volume rm "$NAME" 2>&1) || {
			say "could not remove the data volume: $out" >&2
			left="$left data"
		}
	fi
	images=$(docker images --format '{{.Repository}}:{{.Tag}}' 2>&1) ||
		fail "could not list Docker's images, so its images were not removed: $images"
	for image in $(printf '%s\n' "$images" | grep '^ghcr.io/flokkit/apptown' || :); do
		out=$(docker rmi "$image" 2>&1) || {
			say "could not remove the image $image: $out" >&2
			left="$left image"
		}
	done
	if [ -n "$left" ]; then
		fail "not everything was removed (left:$left); fix the error above and run uninstall again."
	fi
	say "removed the container, its data and its image."
	exit 0
	;;
start) ;;
*) fail "unknown command '$1'. Use no argument to start, or stop or uninstall." ;;
esac

case $VERSION in @*) fail "this copy of the script names no version; set APPTOWN_VERSION." ;; esac

# Docker's own memory, not the computer's: Docker Desktop is given a share.
mem=$(docker info --format '{{.MemTotal}}' 2>/dev/null || echo 0)
if [ "$mem" -gt 0 ] && [ "$mem" -lt 7500000000 ]; then
	say "Docker has $((mem / 1073741824)) GB of memory; AppTown needs at least 8 GB (16 GB recommended)."
	say "In Docker Desktop: Settings > Resources > Memory. Continuing anyway."
fi

# Only this start's log counts: a stopped trial's log still holds its last ready line.
started=$(date -u +%Y-%m-%dT%H:%M:%SZ)
state=none
if present inspect "$NAME"; then
	state=$(docker inspect -f '{{.State.Status}}' "$NAME")
fi
case $state in
running)
	say "already running at http://localhost:$PORT"
	open_link "http://localhost:$PORT"
	exit 0
	;;
none)
	say "downloading and starting AppTown $VERSION (the first start takes a few minutes)"
	docker run -d --name "$NAME" --privileged -p "127.0.0.1:$PORT:$PORT" \
		-v "$NAME:/var/lib/apptown" "$IMAGE" >/dev/null
	;;
*)
	say "starting AppTown again"
	docker start "$NAME" >/dev/null
	;;
esac

# Wait for its ready line, which carries the link (with the one-time setup
# token the first time).
since=$(date +%s)
link=''
while [ -z "$link" ]; do
	if [ "$(docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null)" != true ]; then
		say "AppTown stopped while starting. Its log ends:"
		docker logs --tail 20 "$NAME" >&2
		exit 1
	fi
	if [ $(($(date +%s) - since)) -gt 900 ]; then
		fail "AppTown is not ready after 15 minutes. See: docker logs $NAME"
	fi
	link=$(docker logs --since "$started" "$NAME" 2>&1 | sed -n 's/^apptown: AppTown is ready\. Open \([^ ]*\) .*/\1/p; s/^apptown: AppTown is ready at \([^ ]*\)$/\1/p' | tail -1)
	[ -n "$link" ] || sleep 3
done

say "AppTown is ready: $link"
say "Stop it:      curl -fsSL https://apptown.dev/try | sh -s -- stop"
say "Remove it:    curl -fsSL https://apptown.dev/try | sh -s -- uninstall"
open_link "$link"
