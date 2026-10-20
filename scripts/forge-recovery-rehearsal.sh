#!/bin/bash
# Restore a whole forge onto a replacement host and check it, timing each step
# (the procedure is the guide's, stock/docs/guides/running-apptown.md
# "The forge's own backups").
#
#   FORGE_URL=<source url> FORGE_TOKEN=<source CLI token> \
#   forge-recovery-rehearsal.sh <source ssh> <source data dir> <target ssh> <redeploy slug/deployment>
#
# Read-only on the source: a database dump with forge-db-backup.sh (what every
# deploy does), read-only queries, a tar of the data directory, a copy of the
# running apptownd binary.
#
# The target is cut off from everything a restored forge would act on (upstream
# Git, cloud APIs, attached hosts, model providers) except the one registry the
# redeploy pulls from, before apptownd starts there. What the rehearsal makes on
# the target is known by what Docker holds before it starts: on every exit the
# restored forge is stopped, everything created since is removed, and only then
# is the cut lifted; if any of it remains, the cut stays. A target that already
# holds a forge's containers is refused. A lock file (/root/.lab-lock) keeps two
# runs, or other work on a shared target, apart.
#
# Environment: REGISTRY (required: the one registry host the target may still
# reach, e.g. registry.example.com), TARGET_DATA (/srv/forge-restore/data),
# TARGET_PORT (47777), BLOCK_NAMES (space-separated host names sent to
# 127.0.0.1 on the target, e.g. the source forge's own name and its Git,
# error and telemetry hosts; none by default), RESET_TARGET=1 (first clean up
# what an earlier run left, by that run's own record).
set -euo pipefail
usage() { sed -n 2,27p "$0" >&2; exit 2; }
[ $# -eq 4 ] || usage
[ -n "${REGISTRY:-}" ] || { echo "REGISTRY is not set: name the registry the redeploy pulls from" >&2; usage; }
src=$1 srcdata=$2 tgt=$3 redeploy=$4
slug=${redeploy%/*} depname=${redeploy#*/}
here=$(cd "$(dirname "$0")" && pwd)
tdata=${TARGET_DATA:-/srv/forge-restore/data}
port=${TARGET_PORT:-47777}
registry=$REGISTRY
block=${BLOCK_NAMES:-}
work=$(mktemp -d)
tgthost=${tgt#*@}
turl=http://$tgthost:$port
t0=$(date +%s) last=$t0 current=""
step() { # names the next step and says how long the previous one took
	now=$(date +%s)
	[ -n "$current" ] && echo "   took $((now - last))s: $current (total $((now - t0))s)"
	echo "== $*"
	current=$* last=$now
}

# The lock names this invocation; only it removes its own lock.
lock="forge-recovery-rehearsal $$ from $(hostname) at $(date -u +%FT%TZ)"
ssh "$tgt" "set -o noclobber; echo '$lock' > /root/.lab-lock" 2>/dev/null ||
	{ echo "the target is in use: $(ssh "$tgt" cat /root/.lab-lock)" >&2; exit 1; }

# end_rehearsal runs on the target, within this invocation's exclusive use:
# stop the restored forge (only a process whose binary is the rehearsal's),
# remove what was created since this run's record of Docker
# (/opt/forge-restore/before.*), and only when every observation succeeded
# and none of it remains, retire the record, lift the cut and release the
# lock. Unconfirmed ownership refuses all cleanup. Later cleanup failures
# require inspection: the cut may already be lifted when lock release fails.
end_rehearsal() {
	ssh "$tgt" "bash -s -- $(printf '%q' "$lock")" <<'EOF'
set -u
d=/opt/forge-restore
expected=$1
held=$(cat /root/.lab-lock 2>/dev/null) || { echo "cleanup on the target: lock ownership could not be read; no cleanup attempted" >&2; exit 1; }
[ "$held" = "$expected" ] || { echo "cleanup on the target: this invocation does not own the lock; no cleanup attempted" >&2; exit 1; }
fail() { echo "cleanup on the target: $*; cleanup is incomplete: inspect the target before reuse, recover by hand" >&2; exit 1; }
listing() { case $1 in container) docker ps -aq --no-trunc;; network) docker network ls -q --no-trunc;; volume) docker volume ls -q;; esac; }
p=$(cat $d/pid 2>/dev/null || true)
if [ -n "$p" ] && [ "$(readlink /proc/$p/exe 2>/dev/null)" = "$d/apptownd" ]; then
	kill "$p"; for _ in $(seq 1 60); do kill -0 "$p" 2>/dev/null || break; sleep 1; done
fi
for p in $(pgrep -x apptownd || true); do [ "$(readlink /proc/$p/exe)" = "$d/apptownd" ] && fail "the restored forge ($p) still runs"; done
for kind in container network volume; do
	[ -f $d/before.$kind ] || fail "no record of the target's $kind""s before the rehearsal"
	now=$(listing $kind) || fail "cannot list Docker $kind""s"
	created=$(printf '%s\n' "$now" | sed '/^$/d' | sort | comm -13 $d/before.$kind -)
	[ -z "$created" ] || printf '%s\n' "$created" | xargs docker $kind rm -f > /dev/null || fail "could not remove the $kind""s the rehearsal made"
	now=$(listing $kind) || fail "cannot list Docker $kind""s"
	[ -z "$(printf '%s\n' "$now" | sed '/^$/d' | sort | comm -13 $d/before.$kind -)" ] || fail "$kind""s the rehearsal made remain"
done
rm -f $d/before.container $d/before.network $d/before.volume
# The cut is lifted only on a successful reading of the firewall that no
# longer holds the table: an unreadable firewall is not an absent table.
nft delete table inet forge_rehearsal 2>/dev/null
tables=$(nft list tables) || fail "cannot read the firewall"
case "$tables" in *"inet forge_rehearsal"*) fail "the firewall table remains";; esac
sed -i '/# forge-recovery-rehearsal/d' /etc/hosts
# Other cleanup may have lifted the cut; do not claim it stays on a late failure.
lock_failure() { echo "cleanup on the target: lock release was not confirmed ($*); inspect by hand" >&2; exit 1; }
held=$(cat /root/.lab-lock 2>/dev/null) || lock_failure "cannot read the lock"
[ "$held" = "$expected" ] || lock_failure "this invocation no longer owns the lock"
rm /root/.lab-lock || lock_failure "could not remove the owned lock"
exit 0
EOF
}
# owned says this invocation recorded the target's Docker state, so its
# cleanup may remove what was created since; before that, an exit only
# releases its own lock.
owned=""
finish() {
	status=$?
	if [ -n "$owned" ]; then
		end_rehearsal || status=1
	else
		ssh "$tgt" "[ \"\$(cat /root/.lab-lock 2>/dev/null)\" = '$lock' ] && rm -f /root/.lab-lock" || true
	fi
	rm -rf "$work"
	exit $status
}
trap finish EXIT

step "capture what the source holds (reads)"
python3 "$here/forge-restore-check.py" capture "$FORGE_URL" "$work/expected.json"
# The redeployed app is proved against its recorded release: the revision
# and artifact its deployment's current generation ran on the source.
read -r rev art < <(ssh "$src" "docker exec forge-db psql -U postgres -d forge -At -F ' ' -c \"SELECT i.revision, i.artifact_hash FROM deployments d JOIN projects p ON p.id=d.project_id JOIN deployment_attempts a ON a.deployment_id=d.id AND a.generation=d.desired_generation JOIN deployment_attempt_inputs i ON i.attempt_id=a.id WHERE p.slug='$slug' AND d.name='$depname'\"")
[ -n "${art:-}" ] || { echo "$redeploy has no recorded rebuild inputs on the source: pick another deployment" >&2; exit 1; }
echo "$redeploy runs revision $rev, artifact $art"

step "copy the source's apptownd and dump its database"
version=$(ssh "$src" /opt/apptown/bin/apptownd -version)
ssh "$src" cat /opt/apptown/bin/apptownd > "$work/apptownd"
dump=$(ssh "$src" bash -s -- forge-db postgres forge /root/backups recovery-rehearsal < "$here/forge-db-backup.sh" | sed -n 's/^backup: \([^ ]*\) .*/\1/p')
[ -n "$dump" ] || { echo "the dump did not complete" >&2; exit 1; }
[ "$(ssh "$src" /opt/apptown/bin/apptownd -version)" = "$version" ] || { echo "the source was redeployed during the dump; run again" >&2; exit 1; }
echo "source runs $version; dump $dump"

if [ "${RESET_TARGET:-}" = 1 ]; then
	step "RESET_TARGET=1: remove an earlier run's files (it finished its cleanup)"
	# An earlier run that cleaned up retired its record of Docker; one that
	# did not leaves the record and, perhaps, its work. Its record cannot say
	# what is whose after others have used the target, so it is not used:
	# the operator recovers that by hand.
	ssh "$tgt" bash -s -- "$tdata" <<'EOF'
set -u
if ls /opt/forge-restore/before.* > /dev/null 2>&1; then
	echo "an earlier rehearsal did not finish its cleanup (/opt/forge-restore/before.* remain): recover by hand" >&2; exit 1
fi
for p in $(pgrep -x apptownd || true); do [ "$(readlink /proc/$p/exe)" = /opt/forge-restore/apptownd ] && { echo "a rehearsal forge ($p) still runs" >&2; exit 1; }; done
rm -rf "$1" /opt/forge-restore
EOF
fi

step "prepare the target: refuse a forge's existing state; pull images a fresh host lacks"
ssh "$tgt" bash -s -- "$tdata" <<'EOF'
set -euo pipefail
others=$(docker ps -a --format '{{.Names}}' | grep -E '^(forge-db|forge-env-|forge-svc-|forge-init-|forge-preview-)' || true)
[ -z "$others" ] || { echo "the target already holds a forge's containers: $others" >&2; exit 1; }
[ ! -e /opt/forge-restore ] && { [ ! -e "$1" ] || [ -z "$(ls -A "$1")" ]; } || { echo "an earlier rehearsal's files are on the target; run with RESET_TARGET=1" >&2; exit 1; }
mkdir -p /opt/forge-restore "$1"
docker image inspect postgres:16-alpine > /dev/null 2>&1 || docker pull -q postgres:16-alpine
docker ps -aq --no-trunc | sort > /opt/forge-restore/before.container
docker network ls -q --no-trunc | sort > /opt/forge-restore/before.network
docker volume ls -q | sort > /opt/forge-restore/before.volume
EOF
owned=yes

step "isolate the target before anything of the forge starts there"
ip=$(ssh "$tgt" getent ahostsv4 "$registry" | awk 'NR==1{print $1}')
dns=$(ssh "$tgt" "awk '/^nameserver/{print \$2; exit}' /etc/resolv.conf")
ssh "$tgt" bash -s -- "$ip" "$dns" "$block" <<'EOF'
set -euo pipefail
ip=$1 dns=$2 names=${3:-} # ssh drops an empty BLOCK_NAMES argument
sed -i '/# forge-recovery-rehearsal/d' /etc/hosts
for n in $names; do echo "127.0.0.1 $n # forge-recovery-rehearsal" >> /etc/hosts; done
nft delete table inet forge_rehearsal 2>/dev/null || true
nft -f - <<NFT
table inet forge_rehearsal {
  chain outbound {
    type filter hook output priority filter; policy accept;
    oifname "lo" accept
    ct state established,related accept
    oifname "docker0" accept
    oifname "br-*" accept
    ip daddr $dns udp dport 53 accept
    ip daddr $dns tcp dport 53 accept
    ip daddr $ip tcp dport 443 accept
    counter drop
  }
  chain forwarded {
    type filter hook forward priority filter; policy accept;
    ct state established,related accept
    iifname "docker0" oifname "docker0" accept
    iifname "br-*" oifname "br-*" accept
    iifname "docker0" oifname "br-*" accept
    iifname "br-*" oifname "docker0" accept
    iifname "docker0" counter drop
    iifname "br-*" counter drop
  }
}
NFT
EOF
for probe in https://github.com https://api.digitalocean.com https://openrouter.ai; do
	if ssh "$tgt" curl -s -m 5 -o /dev/null "$probe"; then echo "the target still reaches $probe" >&2; exit 1; fi
done
ssh "$tgt" curl -s -m 5 -o /dev/null -w '%{http_code}' "https://$registry/v2/" | grep -q 401 || { echo "the target cannot reach $registry" >&2; exit 1; }
echo "cut off; $registry reachable"

step "copy the data directory (without pgdata and cache) and the dump"
# GNU tar exits 1 when a file changed while it was read: on a live forge that
# is expected, and is the recovery point's gap the guide names.
ssh "$src" "tar -C '$srcdata' --exclude=./pgdata --exclude=./cache -cf - . ; s=\$?; [ \$s -le 1 ]" | ssh "$tgt" "tar -C '$tdata' -xf -"
ssh "$src" cat "$dump" | ssh "$tgt" "cat > /opt/forge-restore/forge.sql.gz"
ssh "$tgt" "cat > /opt/forge-restore/apptownd && chmod 755 /opt/forge-restore/apptownd" < "$work/apptownd"
ssh "$tgt" du -sh "$tdata"

start() { # start <flags…>: apptownd on the target, its pid in /opt/forge-restore/pid
	# stdin from /dev/null, or the forge holds the ssh session open.
	ssh "$tgt" "cd /opt/forge-restore; nohup /opt/forge-restore/apptownd -data '$tdata' -addr 0.0.0.0:$port -console-addr 127.0.0.1:$((port + 1)) -auth internal -public-host $turl $* < /dev/null > apptownd.log 2>&1 & echo \$! > /opt/forge-restore/pid"
	for _ in $(seq 1 120); do curl -s -o /dev/null "$turl/" && return 0; sleep 2; done
	echo "apptownd did not start; its log:" >&2; ssh "$tgt" tail -20 /opt/forge-restore/apptownd.log >&2; exit 1
}
stop() { # the forge process only; what it made stays until end_rehearsal
	ssh "$tgt" 'p=$(cat /opt/forge-restore/pid); [ "$(readlink /proc/$p/exe)" = /opt/forge-restore/apptownd ] && kill "$p"; while kill -0 "$p" 2>/dev/null; do sleep 1; done'
}

step "make the built-in database, then load the dump into it"
start -verify-only
stop
ssh "$tgt" bash -s <<'EOF'
set -euo pipefail
docker exec forge-db dropdb -U postgres forge
docker exec forge-db createdb -U postgres forge
gunzip -c /opt/forge-restore/forge.sql.gz | docker exec -i forge-db psql -q -v ON_ERROR_STOP=1 -U postgres -d forge > /dev/null
EOF

step "the key and secret files came across unchanged (names only; no contents)"
# Normal startup would create a missing runtime-token-key and carry on, so a
# missing or unreadable one fails here, on either side. Each side hashes the
# files; only the comparison leaves this machine's memory, never a value.
keys="runtime-token-key secrets.env db.env instance-id"
sum() { # sum <ssh> <data dir>: "<file> <sha256>" per key file, or "<file> missing"
	# ssh joins its arguments into one command line: the names go as words.
	ssh "$1" bash -s -- "$2" $keys <<'EOF'
cd "$1" && shift
for f in "$@" $(find ssh -type f 2>/dev/null | sort); do
	if [ -r "$f" ]; then echo "$f $(sha256sum < "$f" | cut -c1-64)"; else echo "$f missing"; fi
done
EOF
}
sum "$src" "$srcdata" | sort > "$work/keys.src"
sum "$tgt" "$tdata" | sort > "$work/keys.tgt"
if grep -q ' missing$' "$work/keys.src" "$work/keys.tgt"; then
	echo "required key or secret files are missing or unreadable:" >&2
	grep -h ' missing$' "$work/keys.src" "$work/keys.tgt" | cut -d' ' -f1 | sort -u >&2
	exit 1
fi
if ! cmp -s "$work/keys.src" "$work/keys.tgt"; then
	echo "key or secret files differ on the target:" >&2
	diff "$work/keys.src" "$work/keys.tgt" | sed -n 's/^[<>] \([^ ]*\) .*/\1/p' | sort -u >&2
	exit 1
fi
echo "same on both sides: $(cut -d' ' -f1 "$work/keys.src" | tr '\n' ' ')"

step "verify read-only (-verify-only): credentials, history, evidence, repositories"
start -verify-only
python3 "$here/forge-restore-check.py" verify "$turl" "$work/expected.json" || { stop; exit 1; }
stop

step "start normally; $redeploy runs again from its recorded release and answers"
start
# Three witnesses on the target, not the restored record's word: the restored
# forge's own newest attempt for the deployment is a rebuild of the source's
# recorded revision and artifact, settled done; the container runs; it answers
# HTTP on its published port.
ok=""
for _ in $(seq 1 60); do
	attempt=$(ssh "$tgt" "docker exec forge-db psql -U postgres -d forge -At -F ' ' -c \"SELECT a.status, i.rebuild, i.revision, i.artifact_hash FROM deployments d JOIN projects p ON p.id=d.project_id JOIN deployment_attempts a ON a.deployment_id=d.id JOIN deployment_attempt_inputs i ON i.attempt_id=a.id WHERE p.slug='$slug' AND d.name='$depname' ORDER BY a.id DESC LIMIT 1\"" 2>/dev/null || true)
	hostport=$(ssh "$tgt" "docker port forge-env-$slug-$depname 2>/dev/null | sed -n 's/.*127.0.0.1:\([0-9]*\)$/\1/p' | head -1" || true)
	code=""
	[ -n "$hostport" ] && code=$(ssh "$tgt" "curl -s -m 5 -o /dev/null -w '%{http_code}' http://127.0.0.1:$hostport/" || true)
	if [ "$attempt" = "done t $rev $art" ] && [[ "$code" =~ ^[23] ]]; then
		echo "rebuilt from revision $rev, artifact $art; answers HTTP $code on 127.0.0.1:$hostport"
		ok=yes
		break
	fi
	sleep 10
done
[ -n "$ok" ] || { echo "$redeploy did not run again from its recorded release within 10 minutes (last attempt: ${attempt:-none}; HTTP ${code:-none})" >&2; exit 1; }

step "clean up: stop the restored forge, remove what it made, lift the cut"
end_rehearsal
owned=""
step "done: the restored forge and what it made are removed, its data kept under $tdata; the network cut is lifted"
