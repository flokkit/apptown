#!/bin/sh
# The stranger test: install a release the way the guide says, on a machine
# that knows nothing of the author's network, and reach a first preview.
#
#   sh scripts/stranger-install.sh <forge-vX-linux-ARCH.tar.gz>
#
# Run it on a Linux machine with Docker that may run a privileged container.
# Everything happens inside one disposable Docker daemon (docker:27-dind) that
# this run starts and owns: there a fresh debian container, given that
# daemon, its network and one working folder, follows Running a forge,
# Install: install Git and the Docker CLI, unpack the release, start forged
# with internal sign-in, wait for /healthz, create the first administrator
# with the setup token, make a CLI token, start a project from a one-page app,
# open a change and preview it, and fetch the page. No model key, no worker,
# nothing from the author's network: Docker Hub, Debian and the public
# internet only. Each step's time is printed; over fifteen minutes fails.
#
# It shows an install and a first preview. It does not show model access, a
# chat, a landing or the agent image's build.
# At the end the run's own daemon is removed with everything in it; nothing
# else on the host is touched.
set -eu

if [ "${1:-}" != --inside ]; then
	archive=${1:?usage: sh scripts/stranger-install.sh <release archive>}
	here=$(cd "$(dirname "$0")" && pwd)
	dind=$(docker run -d --privileged docker:27-dind)
	trap 'docker rm -f -v "$dind" >/dev/null 2>&1' EXIT
	i=0
	until docker exec "$dind" docker version >/dev/null 2>&1; do
		i=$((i + 1)); [ "$i" -lt 60 ] || { echo "the run's Docker daemon did not start"; docker logs "$dind" 2>&1 | tail -20; exit 1; }
		sleep 2
	done
	docker exec "$dind" mkdir -p /work
	docker cp "$archive" "$dind:/work/"
	docker cp "$here/stranger-install.sh" "$dind:/work/"
	docker exec "$dind" docker run --rm --network host -v /var/run/docker.sock:/var/run/docker.sock -v /work:/work -w /work \
		debian:stable-slim sh /work/stranger-install.sh --inside "/work/$(basename "$archive")"
	exit $?
fi

archive=$2
work=$(dirname "$archive")
start=$(date +%s)
step() { echo "== $(( $(date +%s) - start ))s: $*"; }
budget=900

step "prepare the host: Git, the Docker CLI, curl, Python3"
apt-get update -qq >/dev/null
apt-get install -y -qq git curl ca-certificates python3 docker-cli >/dev/null 2>&1 || apt-get install -y -qq git curl ca-certificates python3 docker.io >/dev/null
docker version --format 'docker {{.Server.Version}}'

step "unpack the release"
mkdir -p "$work/opt" && tar -xzf "$archive" -C "$work/opt"
root=$(ls -d "$work"/opt/forge-*)
export PATH="$root/bin:$PATH"
forged -version
forge version 2>/dev/null | head -1 || true

step "start forged: internal sign-in, its own PostgreSQL"
addr=127.0.0.1:47777
export FORGE_URL=http://$addr
cd "$root"
FORGE_AUTH=internal FORGE_ADDR=$addr FORGE_CONSOLE_ADDR= FORGE_DATA="$work/data" \
	forged > "$work/forged.log" 2>&1 &
forged_pid=$!
trap 'kill "$forged_pid" 2>/dev/null; wait "$forged_pid" 2>/dev/null || true' EXIT
until curl -fsS "$FORGE_URL/healthz" >/dev/null 2>&1; do
	kill -0 "$forged_pid" 2>/dev/null || { echo "forged exited:"; tail -30 "$work/forged.log"; exit 1; }
	[ $(( $(date +%s) - start )) -lt $budget ] || { echo "over budget waiting for /healthz"; tail -30 "$work/forged.log"; exit 1; }
	sleep 2
done
curl -fsS "$FORGE_URL/healthz"

step "the first administrator, with the setup token"
token=$(cat "$work/data/setup-token")
password=$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')
curl -fsS -c "$work/cookies" -H 'Content-Type: application/json' \
	-d "{\"username\":\"admin\",\"password\":\"$password\",\"setupToken\":\"$token\"}" "$FORGE_URL/api/v1/auth/bootstrap" >/dev/null
[ ! -e "$work/data/setup-token" ] || { echo "the setup token outlived setup"; exit 1; }

step "a CLI token"
FORGE_TOKEN=$(curl -fsS -b "$work/cookies" -H 'Content-Type: application/json' \
	-d '{"name":"stranger","purpose":"cli"}' "$FORGE_URL/api/v1/tokens" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
export FORGE_TOKEN
[ -n "$FORGE_TOKEN" ] || { echo "no token"; exit 1; }
forge projects >/dev/null

step "a project from a one-page app"
app=$work/hello
mkdir -p "$app/.forge" "$app/site"
printf 'app:\n  web: {static: site, public: true}\n' > "$app/.forge/app.yaml"
printf '<!doctype html><title>hello</title><p>Hello from a stranger.</p>\n' > "$app/site/index.html"
git -C "$app" init -q -b main
git -C "$app" add -A
git -C "$app" -c user.name=stranger -c user.email=stranger@example.com commit -q -m "a page"
git -C "$app" bundle create "$work/hello.bundle" --all 2>/dev/null
forge new hello --from "$work/hello.bundle"

step "a change, and its preview"
change_response=$(curl -fsS -H "Authorization: Bearer $FORGE_TOKEN" -H 'Content-Type: application/json' \
	-d '{"title":"first look","type":"feature","body":"","workflow":""}' "$FORGE_URL/api/v1/projects/hello/changes")
change=$(printf '%s\n' "$change_response" | python3 -c '
import json, sys, uuid
value = json.load(sys.stdin)["change"]["id"]
if not isinstance(value, str) or len(value) != 36 or str(uuid.UUID(value)) != value.lower():
    raise ValueError("not a canonical change UUID")
print(value)
' 2>/dev/null) || { echo "change response did not contain a valid change ID"; exit 1; }
forge preview "$change" | tee "$work/preview.txt"
url=$(sed -n 's/.*is up[^:]*: \(http[^ ]*\).*/\1/p' "$work/preview.txt" | tail -1)
[ -n "$url" ] || { echo "no preview address"; exit 1; }

step "the page"
curl -fsS "$url" | tee "$work/page.html" | grep -q "Hello from a stranger." || { echo "the preview did not serve the page"; exit 1; }

total=$(( $(date +%s) - start ))
step "done in ${total}s (budget ${budget}s)"
[ "$total" -le "$budget" ]
