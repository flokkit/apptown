#!/bin/sh
# The entrypoint of the apptown image (deploy/Dockerfile, target server).
#
# With FORGE_NO_LOCAL_DOCKER=true it is a server: forged runs everything on
# a worker (Running a forge, Workers and running without local Docker) and
# this script only execs it.
#
# Otherwise it is the desktop trial (README, Desktop trial): the container
# runs its own Docker daemon, so it needs --privileged, and forged runs on
# it exactly as on a Linux host. Paths it mounts into containers are this
# container's paths, 127.0.0.1 is this container, and host.docker.internal
# from its containers is this container too. Every default below is set
# only when the variable is unset, so `docker run -e` overrides any of them.
#
# The trial prints a link carrying the one-time setup token once forged
# answers. That puts the first administrator's credential in this
# container's log until the administrator is created: a trial-only
# exception, accepted for a forge on one's own computer
# (design/plans/one-command-trial-2026-10-19.md, section 3). The server
# branch prints none.
set -eu

if [ "${FORGE_NO_LOCAL_DOCKER:-}" = true ]; then
	: "${FORGE_DATA:=/var/lib/forge}"
	export FORGE_DATA
	exec forged "$@"
fi

say() { echo "apptown: $*"; }
# APPTOWN_ROOT and APPTOWN_CGROUP move the volume and the cgroup tree this
# script checks, for its own tests (scripts/apptown_start_test.py).
root=${APPTOWN_ROOT:-/var/lib/apptown}

# A container that is not privileged has a read-only cgroup tree and cannot
# run a Docker daemon.
if [ ! -w "${APPTOWN_CGROUP:-/sys/fs/cgroup}" ]; then
	say "this container cannot run its own Docker. Start it with --privileged (README, Desktop trial),"
	say "or set FORGE_NO_LOCAL_DOCKER=true to run it as a server with a worker (Running a forge)."
	exit 1
fi

# Ctrl-C and docker stop end the trial at any point: forged first, if it
# has started, then Docker. A signal lands between commands, so each wait
# below is short or is a wait the signal interrupts.
dockerd_pid='' forged_pid='' stopping=''
# shellcheck disable=SC2329 # called by the trap below
on_signal() {
	stopping=1
	if [ -n "$forged_pid" ]; then kill -TERM "$forged_pid" 2>/dev/null || :; fi
}
trap on_signal INT TERM
stop_docker() {
	if [ -n "$dockerd_pid" ]; then
		kill -TERM "$dockerd_pid" 2>/dev/null || :
		wait "$dockerd_pid" 2>/dev/null || :
	fi
}

mkdir -p "$root/docker" "$root/forge"
say "starting Docker inside this container"
# Docker's own dind wrapper prepares cgroup nesting (so containers get their
# CPU, memory and process limits), mount propagation and securityfs. The
# daemon listens on its Unix socket only. Its fixed address pools let the
# inference settings below be fixed.
dind dockerd --host=unix:///var/run/docker.sock --data-root "$root/docker" \
	--bip 10.213.0.1/24 --default-address-pool base=10.213.0.0/16,size=24 \
	>"$root/dockerd.log" 2>&1 &
dockerd_pid=$!
i=0
until docker version >/dev/null 2>&1; do
	if [ -n "$stopping" ]; then
		stop_docker
		exit 0
	fi
	i=$((i + 1))
	if [ "$i" -gt 120 ] || ! kill -0 "$dockerd_pid" 2>/dev/null; then
		say "Docker did not start inside this container; its log ends:"
		tail -20 "$root/dockerd.log" >&2
		stop_docker
		exit 1
	fi
	sleep 0.5
done

agent_tag=$(cat /opt/forge/agent-tag 2>/dev/null || :)
if [ -n "$agent_tag" ]; then
	: "${FORGE_AGENT_IMAGE:=ghcr.io/flokkit/apptown-agent:$agent_tag}"
else
	# A build with no release version: the forge builds forge-agent from
	# /opt/forge/deploy/forge-agent.
	: "${FORGE_AGENT_IMAGE:=forge-agent}"
fi
: "${FORGE_DATA:=$root/forge}"
: "${FORGE_AUTH:=internal}"
: "${FORGE_PUBLIC_HOST:=http://localhost:7777}"
: "${FORGE_TIER_DOMAIN:=preview=http://preview.localhost:7777,main=http://main.localhost:7777}"
: "${FORGE_INFERENCE_ADDR:=10.213.0.1:7779}"
: "${FORGE_INFERENCE_URL:=http://host.docker.internal:7779}"
: "${FORGE_INFERENCE_NETWORKS:=10.213.0.0/16}"
export FORGE_AGENT_IMAGE FORGE_DATA FORGE_AUTH FORGE_PUBLIC_HOST FORGE_TIER_DOMAIN \
	FORGE_INFERENCE_ADDR FORGE_INFERENCE_URL FORGE_INFERENCE_NETWORKS

say "starting AppTown; the first start takes a few minutes"
forged "$@" &
forged_pid=$!
if [ -n "$stopping" ]; then kill -TERM "$forged_pid" 2>/dev/null || :; fi

port=${FORGE_ADDR:-:7777}
port=${port##*:}
until curl -fsS -o /dev/null "http://127.0.0.1:$port/healthz" 2>/dev/null; do
	if [ -n "$stopping" ] || ! kill -0 "$forged_pid" 2>/dev/null; then break; fi
	sleep 1
done
if [ -z "$stopping" ] && kill -0 "$forged_pid" 2>/dev/null; then
	if [ -s "$FORGE_DATA/setup-token" ]; then
		say "AppTown is ready. Open $FORGE_PUBLIC_HOST/?setup=$(tr -d '[:space:]' <"$FORGE_DATA/setup-token") to create the administrator."
	else
		say "AppTown is ready at $FORGE_PUBLIC_HOST"
	fi
fi

# forged's status is the container's. wait gives it even when forged has
# already ended; a signal interrupts wait, so wait again while it runs.
status=0
while :; do
	if wait "$forged_pid"; then status=0; else status=$?; fi
	kill -0 "$forged_pid" 2>/dev/null || break
done
stop_docker
exit "$status"
