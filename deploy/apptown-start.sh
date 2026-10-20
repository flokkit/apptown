#!/bin/sh
# The entrypoint of the apptown image (deploy/Dockerfile, target server).
# It runs one of three ways, each with its own defaults:
#
# With APPTOWN_NO_LOCAL_DOCKER=true it is a server with a worker: apptownd
# runs everything on the worker (Split install, Workers and running without
# local Docker) and this script only execs it.
#
# With APPTOWN_ONE_SERVER=true it is one server (Running AppTown, One
# server; deploy/one-server/compose.yaml): apptownd runs work on the host's
# Docker through its mounted socket. The containers it starts mount its
# folders by path and are dialled at 127.0.0.1, so this container must share
# the host's network and hold its data folder at the same path inside and
# out; the script checks both, then execs apptownd.
#
# Otherwise it is the desktop trial (README, Desktop trial): the container
# runs its own Docker daemon, so it needs --privileged, and apptownd runs on
# it exactly as on a Linux host. Paths it mounts into containers are this
# container's paths, 127.0.0.1 is this container, and host.docker.internal
# from its containers is this container too.
#
# Every default below is set only when the variable is unset, so `docker
# run -e` overrides any of them.
#
# The trial prints a link carrying the one-time setup token once apptownd
# answers. That puts the first administrator's credential in this
# container's log until the administrator is created: a trial-only
# exception, accepted for an AppTown on one's own computer
# (private/design/plans/one-command-trial-2026-10-19.md, section 3). The
# other branches print none.
set -eu

say() { echo "apptown: $*"; }

# The agent image: ghcr.io/flokkit/apptown-agent of this image's version,
# or, for a build with no release version, apptown-agent, which AppTown
# builds from /opt/apptown/deploy/apptown-agent.
default_agent_image() {
	agent_tag=$(cat /opt/apptown/agent-tag 2>/dev/null || :)
	if [ -n "$agent_tag" ]; then
		: "${APPTOWN_AGENT_IMAGE:=ghcr.io/flokkit/apptown-agent:$agent_tag}"
	else
		: "${APPTOWN_AGENT_IMAGE:=apptown-agent}"
	fi
	export APPTOWN_AGENT_IMAGE
}

if [ "${APPTOWN_ONE_SERVER:-}" = true ]; then
	: "${APPTOWN_DATA:=/var/lib/apptown}"
	refuse() {
		say "$*"
		say "Start it with the shipped compose file (Running AppTown, One server)."
		exit 1
	}
	docker version >/dev/null 2>&1 || refuse "the host's Docker cannot be reached: mount /var/run/docker.sock."
	# This container's ID, from the /etc/hostname file Docker mounts into
	# it. APPTOWN_MOUNTINFO moves the file for this script's tests.
	self=$(sed -n 's#.*/containers/\([0-9a-f]\{64\}\)/hostname /etc/hostname .*#\1#p' "${APPTOWN_MOUNTINFO:-/proc/self/mountinfo}" | head -n 1)
	[ -n "$self" ] || refuse "this does not look like a Docker container: its ID is not in /proc/self/mountinfo."
	[ "$(docker inspect -f '{{.HostConfig.NetworkMode}}' "$self")" = host ] ||
		refuse "it needs the host's network (network_mode: host): previews are reached at 127.0.0.1, and model access listens on docker0."
	[ "$(docker inspect -f "{{range .Mounts}}{{if eq .Destination \"$APPTOWN_DATA\"}}{{.Type}} {{.Source}}{{end}}{{end}}" "$self")" = "bind $APPTOWN_DATA" ] ||
		refuse "mount the data folder at the same path inside and out ($APPTOWN_DATA:$APPTOWN_DATA): the containers AppTown starts mount it by that path."
	# Temporary folders and the CLI given to containers are mounted into
	# them too, so both live under the data folder. The CLI is refreshed at
	# every start: an upgrade brings its own. APPTOWN_CLI_SOURCE moves it
	# for this script's tests.
	mkdir -p "$APPTOWN_DATA/tmp" "$APPTOWN_DATA/bin"
	cp "${APPTOWN_CLI_SOURCE:-/opt/apptown/bin/apptown-linux}" "$APPTOWN_DATA/bin/apptown-linux.new"
	mv "$APPTOWN_DATA/bin/apptown-linux.new" "$APPTOWN_DATA/bin/apptown-linux"
	: "${TMPDIR:=$APPTOWN_DATA/tmp}"
	: "${APPTOWN_CONSOLE_CLI:=$APPTOWN_DATA/bin/apptown-linux}"
	# The web listener on loopback: the proxy in front serves the public
	# address. The console listener on the default bridge's gateway, which
	# only this machine and its containers reach. Model access is
	# apptownd's own default on docker0 (Setting up a server).
	: "${APPTOWN_ADDR:=127.0.0.1:7777}"
	if [ -z "${APPTOWN_CONSOLE_ADDR:-}" ]; then
		gateway=$(docker network inspect bridge -f '{{range .IPAM.Config}}{{.Gateway}} {{end}}' | tr ' ' '\n' | grep -E '^[0-9.]+$' | head -n 1 || :)
		[ -n "$gateway" ] || refuse "the host's Docker has no default bridge address for the console listener."
		APPTOWN_CONSOLE_ADDR=$gateway:7778
	fi
	: "${APPTOWN_AUTH:=internal}"
	default_agent_image
	export APPTOWN_DATA TMPDIR APPTOWN_CONSOLE_CLI APPTOWN_ADDR APPTOWN_CONSOLE_ADDR APPTOWN_AUTH
	exec apptownd "$@"
fi

if [ "${APPTOWN_NO_LOCAL_DOCKER:-}" = true ]; then
	: "${APPTOWN_DATA:=/var/lib/apptown}"
	export APPTOWN_DATA
	exec apptownd "$@"
fi

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

default_agent_image
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
export APPTOWN_DATA APPTOWN_AUTH APPTOWN_LOCAL_TLS APPTOWN_PUBLIC_HOST APPTOWN_TIER_DOMAIN \
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
