#!/bin/sh
# The entrypoint of the apptown image (deploy/Dockerfile, target server).
#
# With APPTOWN_NO_LOCAL_DOCKER=true it is a server: apptownd runs everything on
# a worker (Split install, Workers and running without local Docker) and
# this script only execs it.
#
# Otherwise it is the desktop trial (README, Desktop trial): the container
# runs its own Docker daemon, so it needs --privileged, and apptownd runs on
# it exactly as on a Linux host. Paths it mounts into containers are this
# container's paths, 127.0.0.1 is this container, and host.docker.internal
# from its containers is this container too. Every default below is set
# only when the variable is unset, so `docker run -e` overrides any of them.
#
# The trial prints a link carrying the one-time setup token once apptownd
# answers. That puts the first administrator's credential in this
# container's log until the administrator is created: a trial-only
# exception, accepted for an AppTown on one's own computer
# (private/design/plans/one-command-trial-2026-10-19.md, section 3). The server
# branch prints none.
set -eu

if [ "${APPTOWN_NO_LOCAL_DOCKER:-}" = true ]; then
	: "${APPTOWN_DATA:=/var/lib/apptown}"
	export APPTOWN_DATA
	exec apptownd "$@"
fi

say() { echo "apptown: $*"; }
# APPTOWN_ROOT and APPTOWN_CGROUP move the volume and the cgroup tree this
# script checks, for its own tests (scripts/apptown_start_test.py).
root=${APPTOWN_ROOT:-/var/lib/apptown}

# A container that is not privileged has a read-only cgroup tree and cannot
# run a Docker daemon.
if [ ! -w "${APPTOWN_CGROUP:-/sys/fs/cgroup}" ]; then
	say "this container cannot run its own Docker. Start it with --privileged (README, Desktop trial),"
	say "or set APPTOWN_NO_LOCAL_DOCKER=true to run it as a server with a worker (Split install)."
	exit 1
fi

# Ctrl-C and docker stop end the trial at any point: apptownd first, if it
# has started, then Docker. A signal lands between commands, so each wait
# below is short or is a wait the signal interrupts.
dockerd_pid='' apptownd_pid='' stopping=''
# shellcheck disable=SC2329 # called by the trap below
on_signal() {
	stopping=1
	if [ -n "$apptownd_pid" ]; then kill -TERM "$apptownd_pid" 2>/dev/null || :; fi
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

agent_tag=$(cat /opt/apptown/agent-tag 2>/dev/null || :)
if [ -n "$agent_tag" ]; then
	: "${APPTOWN_AGENT_IMAGE:=ghcr.io/flokkit/apptown-agent:$agent_tag}"
else
	# A build with no release version: AppTown builds apptown-agent from
	# /opt/apptown/deploy/apptown-agent.
	: "${APPTOWN_AGENT_IMAGE:=apptown-agent}"
fi
: "${APPTOWN_DATA:=$root/forge}"
: "${APPTOWN_AUTH:=internal}"
# HTTPS from AppTown's own certificate authority (apptownd -local-tls), so
# every app sees on a laptop what it sees on a server: requests arrive as
# HTTPS (private/design/plans/trial-https-2026-10-20.md).
: "${APPTOWN_LOCAL_TLS:=true}"
: "${APPTOWN_PUBLIC_HOST:=https://localhost:7777}"
: "${APPTOWN_TIER_DOMAIN:=preview=https://preview.localhost:7777,main=https://main.localhost:7777}"
: "${APPTOWN_INFERENCE_ADDR:=10.213.0.1:7779}"
: "${APPTOWN_INFERENCE_URL:=http://host.docker.internal:7779}"
: "${APPTOWN_INFERENCE_NETWORKS:=10.213.0.0/16}"
export APPTOWN_AGENT_IMAGE APPTOWN_DATA APPTOWN_AUTH APPTOWN_LOCAL_TLS APPTOWN_PUBLIC_HOST APPTOWN_TIER_DOMAIN \
	APPTOWN_INFERENCE_ADDR APPTOWN_INFERENCE_URL APPTOWN_INFERENCE_NETWORKS

say "starting AppTown; the first start takes a few minutes"
apptownd "$@" &
apptownd_pid=$!
if [ -n "$stopping" ]; then kill -TERM "$apptownd_pid" 2>/dev/null || :; fi

port=${APPTOWN_ADDR:-:7777}
port=${port##*:}
ca="$APPTOWN_DATA/local-tls/ca.pem"
answers() {
	if [ "$APPTOWN_LOCAL_TLS" = true ]; then
		[ -s "$ca" ] && curl -fsS -o /dev/null --cacert "$ca" "https://localhost:$port/healthz" 2>/dev/null
	else
		curl -fsS -o /dev/null "http://127.0.0.1:$port/healthz" 2>/dev/null
	fi
}
until answers; do
	if [ -n "$stopping" ] || ! kill -0 "$apptownd_pid" 2>/dev/null; then break; fi
	sleep 1
done
if [ -z "$stopping" ] && kill -0 "$apptownd_pid" 2>/dev/null; then
	if [ "$APPTOWN_LOCAL_TLS" = true ]; then
		say "AppTown serves itself and your apps at https://…localhost addresses on this computer,"
		say "using its own certificate authority. Until this computer trusts it, your browser warns"
		say "about each address. It can vouch only for localhost names, never for a real website."
		say "Trust it once: copy it out with"
		say "  docker cp $(hostname):$ca apptown-local-ca.pem"
		say "and follow \"Trust the trial's certificate\" in The desktop trial (docs/guides/trial)."
	fi
	if [ -s "$APPTOWN_DATA/setup-token" ]; then
		say "AppTown is ready. Open $APPTOWN_PUBLIC_HOST/?setup=$(tr -d '[:space:]' <"$APPTOWN_DATA/setup-token") to create the administrator."
	else
		say "AppTown is ready at $APPTOWN_PUBLIC_HOST"
	fi
fi

# apptownd's status is the container's. wait gives it even when apptownd has
# already ended; a signal interrupts wait, so wait again while it runs.
status=0
while :; do
	if wait "$apptownd_pid"; then status=0; else status=$?; fi
	kill -0 "$apptownd_pid" 2>/dev/null || break
done
stop_docker
exit "$status"
