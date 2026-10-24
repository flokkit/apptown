#!/bin/bash
# Back up a whole AppTown with `apptownd backup`, restore the archive onto a
# replacement host with `apptownd restore`, and check what came back, timing
# each step (the procedure is the guide's, stock/docs/guides/apptown-backups.md).
#
#   APPTOWN_URL=<source url> APPTOWN_TOKEN=<source CLI token> \
#   apptown-recovery-rehearsal.sh backup <source ssh> <source data dir> <slug/deployment> <out dir>
#   apptown-recovery-rehearsal.sh restore <out dir> <target ssh>
#   apptown-recovery-rehearsal.sh all <source ssh> <source data dir> <slug/deployment> <target ssh>
#
# backup writes on the source: `apptownd backup` takes a set of each built-in
# deployment's data with its app stopped for its copy, and holds new work for
# the dump. It copies the archive (encrypted with a passphrase made for the
# run), the passphrase, the source's apptownd and the update files it reads
# its stock from (share/apptown/updates beside its bin/), the key files' digests and
# what the source answers (restore-check.py capture; DATA_QUERY) into <out
# dir>, and removes the archive from the source.
#
# restore cuts the target off from everything a restored AppTown would act
# on (upstream Git, cloud APIs, attached hosts, model providers) except the
# one registry the rebuild pulls from, before apptownd starts there. What
# the rehearsal makes on the target is known by what Docker holds before it
# starts: on every exit the restored AppTown is stopped, everything created
# since is removed, and only then is the cut lifted; if any of it remains,
# the cut stays. A lock file (/root/.lab-lock) keeps two runs, or other work
# on a shared target, apart. It checks: the key files came across unchanged
# (apptownd restore checks them too, against the archive's manifest); reads
# through the API with -verify-only (restore-check.py verify); after a normal
# start, the deployment was rebuilt with its data filled before its app
# started, answers HTTP, and DATA_QUERY answers what it answered on the source.
#
# restore-check.py reads APPTOWN_TOKEN, a CLI token made on the source
# before the backup, on both sides.
# Environment: REGISTRY (required for restore: the registry hosts the target
# may still reach, space-separated, every address of each, e.g.
# registry.example.com, or for Docker Hub "registry-1.docker.io
# auth.docker.io production.cloudflare.docker.com"), TARGET_DATA
# (/srv/apptown-restore/data), TARGET_PORT (47777), BLOCK_NAMES
# (space-separated host names sent to 127.0.0.1 on the target, e.g. the
# source AppTown's own name and its Git, error and telemetry hosts; none by
# default), RESET_TARGET=1 (first clean up what an earlier run left, by that
# run's own record), SOURCE_BIN (the source's apptownd,
# /opt/apptown/bin/apptownd), DATA_QUERY (SQL whose answer must come back
# unchanged, run in the deployment's DATA_NEED container, postgres by default).
set -euo pipefail
usage() { sed -n 2,43p "$0" >&2; exit 2; }
cmd=${1:-}
[ $# -gt 0 ] && shift
case "$cmd" in
backup) [ $# -eq 4 ] || usage ;;
restore) [ $# -eq 2 ] || usage ;;
all) [ $# -eq 4 ] || usage ;;
*) usage ;;
esac
if [ "$cmd" != backup ] && [ -z "${REGISTRY:-}" ]; then
	echo "REGISTRY is not set: name the registry the rebuild pulls from" >&2
	usage
fi
here=$(cd "$(dirname "$0")" && pwd)
srcbin=${SOURCE_BIN:-/opt/apptown/bin/apptownd}
tdata=${TARGET_DATA:-/srv/apptown-restore/data}
port=${TARGET_PORT:-47777}
need=${DATA_NEED:-postgres}
block=${BLOCK_NAMES:-}
tgt="" lock="" allout=""
work=$(mktemp -d)
t0=$(date +%s) last=$t0 current=""
step() { # names the next step and says how long the previous one took
	now=$(date +%s)
	[ -n "$current" ] && echo "   took $((now - last))s: $current (total $((now - t0))s)"
	echo "== $*"
	current=$* last=$now
}

# needs <ssh> <slug> <deployment>: the deployment's need container, by the
# labels standing deployments carry for people and the need's name.
need_container() {
	ssh "$1" "docker ps --filter label=apptown.project=$2 --filter label=apptown.deployment.name=$3 --format '{{.Names}}'" | grep -- "-$need\$" | head -1
}
# query <ssh> <container>: DATA_QUERY's answer in the need's own database.
query() {
	# ssh joins its arguments into one command line: each is quoted for the
	# remote shell, so the whole query arrives as one word.
	ssh "$1" "bash -s -- $(printf '%q %q' "$2" "$DATA_QUERY")" <<'EOF'
docker exec "$1" sh -c 'psql -U "$POSTGRES_USER" -d "${POSTGRES_DB:-$POSTGRES_USER}" -At -c "$1"' _ "$2"
EOF
}
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

do_backup() { # do_backup <source ssh> <source data dir> <slug/deployment> <out dir>
	local src=$1 srcdata=$2 redeploy=$3 out=$4
	local slug=${redeploy%/*} depname=${redeploy#*/}
	mkdir -p "$out"
	[ -z "$(ls -A "$out")" ] || { echo "$out is not empty" >&2; exit 1; }
	echo "$redeploy" > "$out/deployment"
	# The source's built-in database, as the db.env in its data directory
	# records it: the container (APPTOWN_DB_CONTAINER) and the database
	# APPTOWN_DB_URL names (an install made before 1.0 keeps the names it was
	# made with). Only the two names leave the source, never the URL's password.
	srcdb=$(ssh "$src" "sed -n 's/^APPTOWN_DB_CONTAINER=//p' '$srcdata/db.env'")
	dbname=$(ssh "$src" "sed -n 's|^APPTOWN_DB_URL=.*/\\([^/?]*\\).*|\\1|p' '$srcdata/db.env'")
	[ -n "$srcdb" ] && [ -n "$dbname" ] || { echo "$srcdata/db.env on $src names no built-in database container and database" >&2; exit 1; }
	# end of the source's database

	step "capture what the source holds (reads)"
	python3 "$here/apptown-restore-check.py" capture "$APPTOWN_URL" "$out/expected.json"
	# The deployment is rebuilt from its recorded release: a deployment
	# whose release recorded no rebuild inputs cannot be.
	art=$(ssh "$src" "docker exec $srcdb psql -U postgres -d $dbname -At -c \"SELECT i.artifact_hash FROM deployments d JOIN projects p ON p.id=d.project_id JOIN deployment_attempts a ON a.deployment_id=d.id AND a.generation=d.observed_generation JOIN deployment_attempt_inputs i ON i.attempt_id=a.id WHERE p.slug='$slug' AND d.name='$depname' AND a.status='done'\"")
	[ -n "$art" ] || { echo "$redeploy has no recorded rebuild inputs on the source: pick another deployment" >&2; exit 1; }
	sum "$src" "$srcdata" | sort > "$out/keys.src"
	if [ -n "${DATA_QUERY:-}" ]; then
		c=$(need_container "$src" "$slug" "$depname")
		[ -n "$c" ] || { echo "$redeploy has no $need container on the source" >&2; exit 1; }
		query "$src" "$c" > "$out/data.src"
		echo "DATA_QUERY on the source ($c): $(wc -l < "$out/data.src") lines"
	fi

	step "back up the source with apptownd backup"
	openssl rand -hex 24 > "$out/passphrase"
	chmod 600 "$out/passphrase"
	remote=/root/apptown-rehearsal-$$.backup
	ssh "$src" "umask 077; cat > /root/.apptown-rehearsal-pass-$$" < "$out/passphrase"
	ssh "$src" "APPTOWN_BACKUP_PASSPHRASE_FILE=/root/.apptown-rehearsal-pass-$$ $srcbin -data '$srcdata' backup $remote; s=\$?; rm -f /root/.apptown-rehearsal-pass-$$; exit \$s"
	ssh "$src" cat "$remote" > "$out/apptown.backup"
	ssh "$src" cat "$srcbin" > "$out/apptownd"
	# The stock and tool versions that apptownd reads at start, shipped
	# beside its bin/ folder.
	ssh "$src" tar -C "$(dirname "$(dirname "$srcbin")")/share/apptown" -cf - updates > "$out/updates.tar"
	ssh "$src" rm -f "$remote"
	ls -l "$out/apptown.backup"
}

do_restore() { # do_restore <out dir> <target ssh>
	local out=$1
	tgt=$2
	local redeploy slug depname
	redeploy=$(cat "$out/deployment")
	slug=${redeploy%/*} depname=${redeploy#*/}
	local tgthost=${tgt#*@}
	local turl=http://$tgthost:$port
	registry=$REGISTRY

	# The lock names this invocation; only it removes its own lock.
	lock="apptown-recovery-rehearsal $$ from $(hostname) at $(date -u +%FT%TZ)"
	ssh "$tgt" "set -o noclobber; echo '$lock' > /root/.lab-lock" 2>/dev/null ||
		{ echo "the target is in use: $(ssh "$tgt" cat /root/.lab-lock)" >&2; exit 1; }
	locked=yes

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
for p in $(pgrep -x apptownd || true); do [ "$(readlink /proc/$p/exe)" = /opt/forge-restore/apptownd ] && { echo "a rehearsal AppTown ($p) still runs" >&2; exit 1; }; done
rm -rf "$1" /opt/forge-restore
EOF
	fi

	step "prepare the target: record what Docker holds; pull what a fresh host lacks"
	ssh "$tgt" bash -s -- "$tdata" <<'EOF'
set -euo pipefail
[ ! -e /opt/forge-restore ] && { [ ! -e "$1" ] || [ -z "$(ls -A "$1")" ]; } || { echo "an earlier rehearsal's files are on the target; run with RESET_TARGET=1" >&2; exit 1; }
mkdir -p /opt/forge-restore
docker image inspect postgres:16-alpine > /dev/null 2>&1 || docker pull -q postgres:16-alpine
docker ps -aq --no-trunc | sort > /opt/forge-restore/before.container
docker network ls -q --no-trunc | sort > /opt/forge-restore/before.network
docker volume ls -q | sort > /opt/forge-restore/before.volume
EOF
	owned=yes

	step "isolate the target before anything of AppTown starts there"
	# Every address of every registry host: a name with several answers is
	# reached at any of them.
	ip=$(for h in $registry; do ssh "$tgt" getent ahostsv4 "$h" | awk '{print $1}'; done | sort -u | paste -sd, -)
	[ -n "$ip" ] || { echo "no address for $registry" >&2; exit 1; }
	dns=$(ssh "$tgt" "awk '/^nameserver/{print \$2; exit}' /etc/resolv.conf")
	ssh "$tgt" bash -s -- "$ip" "$dns" "$block" <<'EOF'
set -euo pipefail
ip=$1 dns=$2 names=${3:-} # ssh drops an empty BLOCK_NAMES argument
sed -i '/# apptown-recovery-rehearsal/d' /etc/hosts
for n in $names; do echo "127.0.0.1 $n # apptown-recovery-rehearsal" >> /etc/hosts; done
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
    ip daddr { $ip } tcp dport 443 accept
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
	first=${registry%% *}
	ssh "$tgt" curl -s -m 5 -o /dev/null -w '%{http_code}' "https://$first/v2/" | grep -q 401 || { echo "the target cannot reach $first" >&2; exit 1; }
	echo "cut off; $registry reachable"

	step "copy the archive, its passphrase and the source's apptownd"
	ssh "$tgt" "umask 077; cat > /opt/forge-restore/apptown.backup" < "$out/apptown.backup"
	ssh "$tgt" "umask 077; cat > /opt/forge-restore/passphrase" < "$out/passphrase"
	ssh "$tgt" "cat > /opt/forge-restore/apptownd && chmod 755 /opt/forge-restore/apptownd" < "$out/apptownd"
	ssh "$tgt" "rm -rf /opt/forge-restore/updates && tar -C /opt/forge-restore -xf -" < "$out/updates.tar"

	step "apptownd restore: refuse an AppTown already there, unpack, check the key files, load the dump"
	ssh "$tgt" "APPTOWN_BACKUP_PASSPHRASE_FILE=/opt/forge-restore/passphrase /opt/forge-restore/apptownd -shipped /opt/forge-restore/updates -data '$tdata' -addr 0.0.0.0:$port -console-addr 127.0.0.1:$((port + 1)) -auth internal -public-host $turl restore /opt/forge-restore/apptown.backup < /dev/null"

	step "the key and secret files came across unchanged (names only; no contents)"
	# Each side hashes the files; only the comparison leaves this machine's
	# memory, never a value.
	sum "$tgt" "$tdata" | sort > "$work/keys.tgt"
	# AppTown's keys and its database's bootstrap must be there; secrets.env
	# is written only once something is set in it, and may be absent on both
	# sides alike.
	if grep -E -q '^(runtime-token-key|db.env|instance-id) missing$' "$out/keys.src" "$work/keys.tgt"; then
		echo "required key files are missing or unreadable:" >&2
		grep -h -E '^(runtime-token-key|db.env|instance-id) missing$' "$out/keys.src" "$work/keys.tgt" | cut -d' ' -f1 | sort -u >&2
		exit 1
	fi
	if ! cmp -s "$out/keys.src" "$work/keys.tgt"; then
		echo "key or secret files differ on the target:" >&2
		diff "$out/keys.src" "$work/keys.tgt" | sed -n 's/^[<>] \([^ ]*\) .*/\1/p' | sort -u >&2
		exit 1
	fi
	echo "same on both sides: $(cut -d' ' -f1 "$work/keys.tgt" | tr '\n' ' ')"

	start() { # start <flags…>: apptownd on the target, its pid in /opt/forge-restore/pid
		# stdin from /dev/null, or AppTown holds the ssh session open.
		ssh "$tgt" "cd /opt/forge-restore; nohup /opt/forge-restore/apptownd -shipped /opt/forge-restore/updates -data '$tdata' -addr 0.0.0.0:$port -console-addr 127.0.0.1:$((port + 1)) -auth internal -public-host $turl $* < /dev/null > apptownd.log 2>&1 & echo \$! > /opt/forge-restore/pid"
		for _ in $(seq 1 120); do curl -s -o /dev/null "$turl/" && return 0; sleep 2; done
		echo "apptownd did not start; its log:" >&2; ssh "$tgt" tail -20 /opt/forge-restore/apptownd.log >&2; exit 1
	}
	stop() { # AppTown process only; what it made stays until end_rehearsal
		ssh "$tgt" 'p=$(cat /opt/forge-restore/pid); [ "$(readlink /proc/$p/exe)" = /opt/forge-restore/apptownd ] && kill "$p"; while kill -0 "$p" 2>/dev/null; do sleep 1; done'
	}

	if [ -s "$out/expected.json" ]; then
		step "verify read-only (-verify-only): credentials, history, evidence, repositories"
		start -verify-only
		python3 "$here/apptown-restore-check.py" verify "$turl" "$out/expected.json" || { stop; exit 1; }
		stop
	fi

	step "start normally; $redeploy is rebuilt with its data put back before its app starts"
	start
	local db dbname
	db=$(ssh "$tgt" "sed -n 's/^APPTOWN_DB_CONTAINER=//p' '$tdata/db.env'")
	dbname=$(ssh "$tgt" "sed -n 's|^APPTOWN_DB_URL=.*/\\([^/?]*\\).*|\\1|p' '$tdata/db.env'")
	# Witnesses on the target, not the restored record's word alone: the
	# restored AppTown's fill of the deployment ended filled, its container
	# runs and answers HTTP, and DATA_QUERY answers as it did on the source.
	ok="" fill="" code=""
	for _ in $(seq 1 60); do
		fill=$(ssh "$tgt" "docker exec $db psql -U postgres -d $dbname -At -c \"SELECT f.state || ' ' || f.detail FROM restore_fills f JOIN deployments d ON d.id=f.deployment_id JOIN projects p ON p.id=d.project_id WHERE p.slug='$slug' AND d.name='$depname'\"" 2>/dev/null || true)
		case "$fill" in failed*) break ;; esac
		container=$(ssh "$tgt" "docker exec $db psql -U postgres -d $dbname -At -c \"SELECT d.container FROM deployments d JOIN projects p ON p.id=d.project_id WHERE p.slug='$slug' AND d.name='$depname'\"" 2>/dev/null || true)
		hostport=$(ssh "$tgt" "docker port ${container:-none} 2>/dev/null | sed -n 's/.*127.0.0.1:\([0-9]*\)$/\1/p' | head -1" || true)
		code=""
		[ -n "$hostport" ] && code=$(ssh "$tgt" "curl -s -m 5 -o /dev/null -w '%{http_code}' http://127.0.0.1:$hostport/" || true)
		if [ "${fill%% *}" = filled ] && [[ "$code" =~ ^[23] ]]; then
			echo "rebuilt with its data; answers HTTP $code on 127.0.0.1:$hostport"
			ok=yes
			break
		fi
		sleep 10
	done
	[ -n "$ok" ] || { echo "$redeploy did not come back with its data within 10 minutes (fill: ${fill:-none}; HTTP ${code:-none})" >&2; exit 1; }
	if [ -n "${DATA_QUERY:-}" ]; then
		c=$(need_container "$tgt" "$slug" "$depname")
		[ -n "$c" ] || { echo "$redeploy has no $need container on the target" >&2; exit 1; }
		query "$tgt" "$c" > "$work/data.tgt"
		if ! cmp -s "$out/data.src" "$work/data.tgt"; then
			echo "DATA_QUERY answers differently on the target:" >&2
			diff "$out/data.src" "$work/data.tgt" >&2 || true
			exit 1
		fi
		echo "DATA_QUERY answers as on the source ($(wc -l < "$work/data.tgt") lines)"
	fi

	step "clean up: stop the restored AppTown, remove what it made, lift the cut"
	end_rehearsal
	owned=""
	locked=""
	step "done: the restored AppTown and what it made are removed, its data kept under $tdata; the network cut is lifted"
}

# end_rehearsal runs on the target, within this invocation's exclusive use:
# stop the restored AppTown (only a process whose binary is the rehearsal's),
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
for p in $(pgrep -x apptownd || true); do [ "$(readlink /proc/$p/exe)" = "$d/apptownd" ] && fail "the restored AppTown ($p) still runs"; done
for kind in container network volume; do
	[ -f $d/before.$kind ] || fail "no record of the target's $kind""s before the rehearsal"
	now=$(listing $kind) || fail "cannot list Docker $kind""s"
	created=$(printf '%s\n' "$now" | sed '/^$/d' | sort | comm -13 $d/before.$kind -)
	[ -z "$created" ] || printf '%s\n' "$created" | xargs docker $kind rm -f > /dev/null || fail "could not remove the $kind""s the rehearsal made"
	now=$(listing $kind) || fail "cannot list Docker $kind""s"
	[ -z "$(printf '%s\n' "$now" | sed '/^$/d' | sort | comm -13 $d/before.$kind -)" ] || fail "$kind""s the rehearsal made remain"
done
rm -f $d/before.container $d/before.network $d/before.volume $d/apptown.backup $d/passphrase
# The cut is lifted only on a successful reading of the firewall that no
# longer holds the table: an unreadable firewall is not an absent table.
nft delete table inet forge_rehearsal 2>/dev/null
tables=$(nft list tables) || fail "cannot read the firewall"
case "$tables" in *"inet forge_rehearsal"*) fail "the firewall table remains";; esac
sed -i '/# apptown-recovery-rehearsal/d' /etc/hosts
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
owned="" locked=""
finish() {
	status=$?
	if [ -n "$owned" ]; then
		end_rehearsal || status=1
	elif [ -n "$locked" ]; then
		ssh "$tgt" "[ \"\$(cat /root/.lab-lock 2>/dev/null)\" = '$lock' ] && rm -f /root/.lab-lock" || true
	fi
	rm -rf "$work" ${allout:+"$allout"}
	exit $status
}
trap finish EXIT

case "$cmd" in
backup) do_backup "$1" "$2" "$3" "$4" ;;
restore) do_restore "$1" "$2" ;;
all)
	# The archive and its passphrase together open every secret: they go
	# with the run, however it ends.
	allout=$work/run
	do_backup "$1" "$2" "$3" "$allout"
	do_restore "$allout" "$4"
	;;
esac
