#!/bin/sh
# The stranger test for the desktop trial (README, Desktop trial): run the
# apptown image the way the README says and reach a first preview.
#
#   sh scripts/trial-check.sh <version> [--published]
#
# <version> is an image tag without the v (1.0.0-rc.6). Without --published
# it first builds ghcr.io/flokkit/apptown:<version> and
# ghcr.io/flokkit/apptown-agent:<version> here, with the Dockerfiles and tags
# images.yml uses, so the trial can be checked before the images are
# published; the agent image is then loaded into the trial's own Docker
# (which cannot pull an unpublished image) and the trial restarted so its
# start-up check sees it. With --published it pulls both and only restarts.
#
# Then: the agent image is present in /setup, a container on the trial's
# Docker gets the memory and process limits it asks for, and a project from a
# one-page app gets a change whose preview the host fetches at the address
# AppTown reports. No model key: a chat and an agent-made app are checked
# by hand. Each step's time is printed, the first and second start's
# included. The trial's container and volume are removed at the end;
# nothing else is touched. PORT (default 7777) is the host port.
set -eu

version=${1:?usage: sh scripts/trial-check.sh <version> [--published]}
published=${2:-}
port=${PORT:-7777}
image=ghcr.io/flokkit/apptown:$version
agent=ghcr.io/flokkit/apptown-agent:$version
name=apptown-check-$$
here=$(cd "$(dirname "$0")/.." && pwd)
start=$(date +%s)
step() { echo "== $(( $(date +%s) - start ))s: $*"; }
fail() { echo "FAIL: $*"; docker logs "$name" 2>&1 | tail -30; exit 1; }

if [ "$published" = --published ]; then
	step "pull $image and $agent"
	docker pull -q "$image" >/dev/null
	docker pull -q "$agent" >/dev/null
else
	step "build $image and $agent here"
	docker build -q -f "$here/deploy/Dockerfile" --target server --build-arg "VERSION=v$version" -t "$image" "$here" >/dev/null
	docker build -q -t "$agent" "$here/deploy/apptown-agent" >/dev/null
fi

trap 'docker rm -f "$name" >/dev/null 2>&1; docker volume rm "$name" >/dev/null 2>&1 || :' EXIT
origin=http://localhost:$port
# ready <n>: wait for the n-th ready line and print the link in it.
ready() {
	t0=$(date +%s)
	until [ "$(docker logs "$name" 2>&1 | grep -c 'AppTown is ready')" -ge "$1" ]; do
		[ "$(docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null)" = true ] || fail "the trial stopped"
		[ $(( $(date +%s) - t0 )) -lt 900 ] || fail "not ready after 15 minutes"
		sleep 2
	done
	echo "   ready after $(( $(date +%s) - t0 ))s"
}

step "the README command (first start)"
docker run -d --name "$name" --privileged -p "127.0.0.1:$port:7777" -v "$name:/var/lib/apptown" \
	-e "APPTOWN_PUBLIC_HOST=$origin" -e "APPTOWN_TIER_DOMAIN=preview=http://preview.localhost:$port,main=http://main.localhost:$port" \
	"$image" >/dev/null
ready 1

if [ "$published" != --published ]; then
	step "load the unpublished agent image into the trial's Docker"
	docker save "$agent" | docker exec -i "$name" docker load -q >/dev/null
fi
step "restart (second start)"
docker restart "$name" >/dev/null
ready 2

step "the first administrator, from the printed link"
token=$(docker logs "$name" 2>&1 | sed -n 's/.*?setup=\([0-9a-f]*\) .*/\1/p' | tail -1)
[ -n "$token" ] || fail "the ready line carried no setup token"
jar=$(mktemp)
password=$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')
curl -fsS -c "$jar" -H 'Content-Type: application/json' \
	-d "{\"username\":\"admin\",\"password\":\"$password\",\"setupToken\":\"$token\"}" "$origin/api/v1/auth/bootstrap" >/dev/null
cli=$(curl -fsS -b "$jar" -H 'Content-Type: application/json' -d '{"name":"trial-check","purpose":"cli"}' \
	"$origin/api/v1/tokens" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
rm -f "$jar"
[ -n "$cli" ] || fail "no CLI token"

step "the agent image is present"
# Ready means apptownd answers, not that a first pull has finished: wait for
# the agent image to settle.
t0=$(date +%s)
while :; do
	setup=$(curl -fsS -H "Authorization: Bearer $cli" "$origin/api/v1/setup")
	case $setup in
	*'"agentImage":"present"'*) break ;;
	*'"agentImage":"pulling"'* | *'"agentImage":"building"'*) ;;
	*) fail "agent image: $setup" ;;
	esac
	[ $(( $(date +%s) - t0 )) -lt 1800 ] || fail "the agent image was not present after 30 minutes: $setup"
	sleep 5
done
echo "   present after $(( $(date +%s) - t0 ))s"

step "a container on the trial's Docker gets its limits"
limits=$(docker exec "$name" docker run --rm --memory 1024m --memory-swap 1024m --pids-limit 512 --cpus 1 \
	--entrypoint sh "$agent" -c 'cat /sys/fs/cgroup/memory.max /sys/fs/cgroup/pids.max' | tr '\n' ' ')
[ "$limits" = "1073741824 512 " ] || fail "limits inside the container: $limits"

step "a project from a one-page app"
docker exec -e APPTOWN_URL=http://127.0.0.1:7777 -e "APPTOWN_TOKEN=$cli" "$name" sh -eu -c '
	app=$(mktemp -d)/hello
	mkdir -p "$app/.apptown" "$app/site"
	printf "app:\n  web: {static: site, public: true}\n" > "$app/.apptown/app.yaml"
	printf "<!doctype html><title>hello</title><p>Hello from the trial.</p>\n" > "$app/site/index.html"
	git -C "$app" init -q -b main
	git -C "$app" add -A
	git -C "$app" -c user.name=trial -c user.email=trial@example.com commit -q -m "a page"
	git -C "$app" bundle create "$app.bundle" --all 2>/dev/null
	apptown new hello --from "$app.bundle" >/dev/null'

step "a change, and its preview"
change=$(curl -fsS -H "Authorization: Bearer $cli" -H 'Content-Type: application/json' \
	-d '{"title":"first look","type":"feature","body":"","workflow":""}' "$origin/api/v1/projects/hello/changes" |
	python3 -c '
import json, sys, uuid
value = json.load(sys.stdin)["change"]["id"]
if not isinstance(value, str) or str(uuid.UUID(value)) != value.lower():
    raise ValueError("not a canonical change UUID")
print(value)') || fail "the change response carried no change UUID"
url=$(docker exec -e APPTOWN_URL=http://127.0.0.1:7777 -e "APPTOWN_TOKEN=$cli" "$name" apptown preview "$change" |
	sed -n 's/.*is up[^:]*: \(http[^ ]*\).*/\1/p' | tail -1)
case $url in
http://change-*.preview.localhost:$port*) ;;
*) fail "preview address: '$url'" ;;
esac
host=${url#http://}
host=${host%%/*}
step "the page at $url"
curl -fsS --resolve "$host:127.0.0.1" "$url" | grep -q "Hello from the trial." || fail "the preview did not serve the page"

step "done"
