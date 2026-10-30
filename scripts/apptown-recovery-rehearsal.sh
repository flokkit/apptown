#!/bin/bash
# Back up a whole AppTown with `apptownd backup`, restore the archive onto a
# spare host with `apptownd restore`, check what came back and start it,
# timing each step (the procedure is the guide's,
# stock/docs/guides/apptown-backups.md). With CANDIDATE, the restore and the
# start use a release archive's apptownd: the rehearsal is then that
# release's upgrade of the source's data (design/operations/releasing.md).
#
#   APPTOWN_URL=<source url> APPTOWN_TOKEN=<source CLI token> [CANDIDATE=<linux release archive on the target>] \
#   apptown-recovery-rehearsal.sh <source ssh> <source data dir> <target ssh>
#
# On the source, `apptownd backup` writes the archive on the source's disk;
# nothing is stopped and new work is held for the dump. With STORE=1 it is
# also copied to the source's backup store (backup -store). The archive is
# encrypted with the passphrase in PASSPHRASE_FILE, which is kept, or else
# with one made for the run and lost with it: STORE=1 needs PASSPHRASE_FILE.
# The archive goes from the source to the target through this machine's
# pipe, never written here, and is deleted on both when the run ends,
# however it ends. Before the backup, restore-check.py captures what the
# source answers (with APPTOWN_TOKEN, a CLI token made on the source).
#
# The target is cut off from everything a restored AppTown would act on
# (upstream Git, cloud APIs, attached hosts, model providers, registries)
# before apptownd starts there: an nftables table allowing only loopback,
# DNS and Docker's own networks, and /etc/hosts entries for BLOCK_NAMES.
# What the rehearsal makes on the target is known by what Docker holds before
# it starts: on every exit the restored AppTown is stopped, everything created
# since is removed, its data directory deleted, and only then is the cut
# lifted; if any of it remains, the cut stays. A lock file (/root/.lab-lock)
# keeps two runs, or other work on a shared target, apart. It checks: the key
# files the source has came across unchanged (a restore into the built-in
# database writes a new db.env when the source ran on -db-url); reads through
# the API with -verify-only (restore-check.py verify, a sample: activity
# history and recent changes per project); after a normal start, AppTown
# answers its health check (HTTP 200) and names the expected version (the candidate's, or the
# source's). Built-in apps are not checked: their data comes from their own
# deployment backups, and the cut target cannot pull their images.
#
# Environment: TARGET_DATA (/srv/apptown-restore/data), TARGET_PORT (47777),
# BLOCK_NAMES (space-separated host names sent to 127.0.0.1 on the target,
# e.g. the source AppTown's own name; none by default), RESET_TARGET=1
# (first clean up what an earlier run left, by that run's own record),
# SOURCE_BIN (the source's apptownd, /opt/apptown/bin/apptownd), SOURCE_IN (a
# command prefix every command on the source runs in, for a source in a
# container: `cd /opt/apptown && docker compose exec -T apptown`; none by
# default), CANDIDATE, STORE, PASSPHRASE_FILE.
set -euo pipefail
usage() { sed -n 2,46p "$0" >&2; exit 2; }
[ $# -eq 3 ] || usage
src=$1 srcdata=$2 tgt=$3
here=$(cd "$(dirname "$0")" && pwd)
srcbin=${SOURCE_BIN:-/opt/apptown/bin/apptownd}
srcin=${SOURCE_IN:-}
# on_src <command>: a shell command on the source, inside SOURCE_IN; standard
# input and output pass through.
on_src() { ssh "$src" "$srcin bash -c $(printf %q "$1")"; }
tdata=${TARGET_DATA:-/srv/apptown-restore/data}
port=${TARGET_PORT:-47777}
block=${BLOCK_NAMES:-}
# The archive is already on the target (the cut-off target cannot fetch it
# later, and this machine need not hold a copy).
candidate=${CANDIDATE:-}
[ -z "$candidate" ] || ssh "$tgt" "[ -f '$candidate' ]" || { echo "CANDIDATE $candidate is not a file on $tgt" >&2; exit 2; }
passfile=${PASSPHRASE_FILE:-}
[ -z "$passfile" ] || [ -s "$passfile" ] || { echo "PASSPHRASE_FILE $passfile is not a file with a passphrase" >&2; exit 2; }
store=
case ${STORE:-} in
"") ;;
1) store=-store; [ -n "$passfile" ] || { echo "STORE=1 keeps the archive beyond the run: give its passphrase in PASSPHRASE_FILE" >&2; exit 2; } ;;
*) echo "STORE is 1 or unset, not $STORE" >&2; exit 2 ;;
esac
lock="" srcdir="" srcarchive=""
work=$(mktemp -d)
t0=$(date +%s) last=$t0 current=""
step() { # names the next step and says how long the previous one took
	now=$(date +%s)
	[ -n "$current" ] && echo "   took $((now - last))s: $current (total $((now - t0))s)"
	echo "== $*"
	current=$* last=$now
}

keys="runtime-token-key secrets.env db.env instance-id"
sum() { # sum <ssh> <data dir> [<prefix>]: "<file> <sha256>" per key file, or "<file> missing"
	# ssh joins its arguments into one command line: the names go as words.
	ssh "$1" ${3:-} bash -s -- "$2" $keys <<'EOF'
cd "$1" && shift
for f in "$@" $(find ssh -type f 2>/dev/null | sort); do
	if [ -r "$f" ]; then echo "$f $(sha256sum < "$f" | cut -c1-64)"; else echo "$f missing"; fi
done
EOF
}

rehearse() {
	local tgthost=${tgt#*@}
	local turl=http://$tgthost:$port

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
processes=$(pgrep -x apptownd) || {
	status=$?
	[ "$status" = 1 ] || { echo "cannot list AppTown processes; reset refused" >&2; exit 1; }
}
for p in $processes; do [ "$(readlink /proc/$p/exe)" = /opt/forge-restore/apptownd ] && { echo "a rehearsal AppTown ($p) still runs" >&2; exit 1; }; done
rm -rf "$1" /opt/forge-restore
EOF
	fi

	step "prepare the target: record what Docker holds; pull the built-in database's image"
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
	dns=$(ssh "$tgt" "awk '/^nameserver/{print \$2; exit}' /etc/resolv.conf")
	ssh "$tgt" bash -s -- "$dns" "$block" <<'EOF'
set -euo pipefail
dns=$1 names=${2:-} # ssh drops an empty BLOCK_NAMES argument
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
	for probe in https://github.com https://api.digitalocean.com https://openrouter.ai https://ghcr.io; do
		if ssh "$tgt" curl -s -m 5 -o /dev/null "$probe"; then echo "the target still reaches $probe" >&2; exit 1; fi
	done
	echo "cut off"

	if [ -n "$candidate" ]; then
		step "the candidate restores and starts: $(basename "$candidate")"
		ssh "$tgt" "mkdir -p /opt/forge-restore/candidate && tar --no-same-owner -xzf '$candidate' -C /opt/forge-restore/candidate --strip-components=1"
		ssh "$tgt" "cp /opt/forge-restore/candidate/bin/apptownd /opt/forge-restore/apptownd && rm -rf /opt/forge-restore/updates && cp -R /opt/forge-restore/candidate/share/apptown/updates /opt/forge-restore/updates"
	else
		step "the source's apptownd restores and starts"
		on_src "cat '$srcbin'" | ssh "$tgt" "cat > /opt/forge-restore/apptownd && chmod 755 /opt/forge-restore/apptownd"
		# The stock and tool versions that apptownd reads at start, shipped
		# beside its bin/ folder.
		on_src "tar -C '$(dirname "$(dirname "$srcbin")")/share/apptown' -cf - updates" | ssh "$tgt" "rm -rf /opt/forge-restore/updates && tar -C /opt/forge-restore -xf -"
	fi
	expected=$(ssh "$tgt" /opt/forge-restore/apptownd -version | awk '{print $2}')
	[ -n "$expected" ] || { echo "the restoring apptownd names no version" >&2; exit 1; }
	echo "restores and starts as $expected"

	step "capture what the source holds (reads)"
	python3 "$here/apptown-restore-check.py" capture "$APPTOWN_URL" "$work/expected.json"
	sum "$src" "$srcdata" "$srcin" | sort > "$work/keys.src"

	step "back up the source with apptownd backup (nothing stops)"
	if [ -n "$passfile" ]; then
		cp "$passfile" "$work/passphrase"
	else
		openssl rand -hex 24 > "$work/passphrase"
	fi
	chmod 600 "$work/passphrase"
	# The running AppTown writes the archive (apptownd backup asks it over
	# its control socket), as the user it runs as: the demo's is not root,
	# so the archive goes in a folder of the data folder's owner.
	srcdir=$(on_src "d=\$(mktemp -d /var/tmp/apptown-rehearsal-XXXXXX) && chown \$(stat -c %u:%g '$srcdata') \"\$d\" && echo \"\$d\"")
	srcarchive=$srcdir/apptown.backup
	on_src "umask 077; cat > /root/.apptown-rehearsal-pass-$$" < "$work/passphrase"
	on_src "APPTOWN_BACKUP_PASSPHRASE_FILE=/root/.apptown-rehearsal-pass-$$ $srcbin -data '$srcdata' backup $store $srcarchive; s=\$?; rm -f /root/.apptown-rehearsal-pass-$$; exit \$s"

	step "carry the archive from the source to the target (through this machine's pipe, not its disk)"
	on_src "cat '$srcarchive'" | ssh "$tgt" "umask 077; cat > /opt/forge-restore/apptown.backup"
	ssh "$tgt" "umask 077; cat > /opt/forge-restore/passphrase" < "$work/passphrase"
	ssh "$tgt" ls -l /opt/forge-restore/apptown.backup

	step "apptownd restore: refuse an AppTown already there, unpack, check the key files, load the dump"
	ssh "$tgt" "APPTOWN_BACKUP_PASSPHRASE_FILE=/opt/forge-restore/passphrase /opt/forge-restore/apptownd -shipped /opt/forge-restore/updates -data '$tdata' -addr 0.0.0.0:$port -console-addr 127.0.0.1:$((port + 1)) -auth internal -public-host $turl restore /opt/forge-restore/apptown.backup < /dev/null"

	step "the source's key and secret files came across unchanged (names only; no contents)"
	sum "$tgt" "$tdata" | sort > "$work/keys.tgt"
	compare_keys "$work/keys.src" "$work/keys.tgt"

	start() { # start <flags…>: apptownd on the target, its pid in /opt/forge-restore/pid
		# stdin from /dev/null, or AppTown holds the ssh session open.
		ssh "$tgt" "cd /opt/forge-restore; nohup /opt/forge-restore/apptownd -shipped /opt/forge-restore/updates -data '$tdata' -addr 0.0.0.0:$port -console-addr 127.0.0.1:$((port + 1)) -auth internal -public-host $turl $* < /dev/null > apptownd.log 2>&1 & echo \$! > /opt/forge-restore/pid"
		for _ in $(seq 1 120); do curl -s -o /dev/null "$turl/" && return 0; sleep 2; done
		echo "apptownd did not start; its log:" >&2; ssh "$tgt" tail -20 /opt/forge-restore/apptownd.log >&2; exit 1
	}
	stop() { # AppTown process only; what it made stays until end_rehearsal
		ssh "$tgt" 'p=$(cat /opt/forge-restore/pid); [ "$(readlink /proc/$p/exe)" = /opt/forge-restore/apptownd ] && kill "$p"; while kill -0 "$p" 2>/dev/null; do sleep 1; done'
	}

	step "verify read-only (-verify-only): credentials, history, evidence, repositories"
	start -verify-only
	python3 "$here/apptown-restore-check.py" verify "$turl" "$work/expected.json" || { stop; exit 1; }
	stop

	step "start normally: AppTown is healthy and names $expected"
	start
	code=$(curl -s -o /dev/null -D "$work/health" -w '%{http_code}' "$turl/healthz") || code=none
	got=$(tr -d '\r' < "$work/health" | sed -n 's/^[Xx]-[Aa]pp[Tt]own-[Vv]ersion: //p')
	[ "$code" = 200 ] || { echo "the restored AppTown's health check answers $code" >&2; exit 1; }
	[ "$got" = "$expected" ] || { echo "the restored AppTown names version '${got:-none}', not $expected" >&2; exit 1; }
	echo "healthy, as $got"

	step "clean up: stop the restored AppTown, remove what it made and its data, lift the cut"
	end_rehearsal
	owned=""
	locked=""
	step "done: the restored AppTown, what it made and its data are removed; the network cut is lifted"
}

# compare_keys <source sums> <target sums>: each key file the source has is
# on the target unchanged. Only the comparison leaves the hosts' memory,
# never a value. AppTown's own keys must be on the source. A source on
# -db-url has no db.env; the restore writes the built-in database's own,
# which is the target's and not compared.
compare_keys() {
	if grep -E -q '^(runtime-token-key|instance-id) missing$' "$1"; then
		echo "the source's key files are missing or unreadable:" >&2
		grep -E '^(runtime-token-key|instance-id) missing$' "$1" | cut -d' ' -f1 >&2
		return 1
	fi
	local differ
	differ=$(grep -v ' missing$' "$1" | grep -F -x -v -f "$2" | cut -d' ' -f1 || true)
	if [ -n "$differ" ]; then
		echo "key or secret files differ on the target:" >&2
		echo "$differ" >&2
		return 1
	fi
	echo "same on both sides: $(grep -v ' missing$' "$1" | cut -d' ' -f1 | tr '\n' ' ')"
}

# end_rehearsal runs on the target, within this invocation's exclusive use:
# stop the restored AppTown (only a process whose binary is the rehearsal's),
# remove what was created since this run's record of Docker
# (/opt/forge-restore/before.*) and the restored data directory, and only
# when every observation succeeded
# and none of it remains, retire the record, lift the cut and release the
# lock. Unconfirmed ownership refuses all cleanup. Later cleanup failures
# require inspection: the cut may already be lifted when lock release fails.
end_rehearsal() {
	ssh "$tgt" "bash -s -- $(printf '%q %q' "$lock" "$tdata")" <<'EOF'
set -u
d=/opt/forge-restore
expected=$1 data=$2
held=$(cat /root/.lab-lock 2>/dev/null) || { echo "cleanup on the target: lock ownership could not be read; no cleanup attempted" >&2; exit 1; }
[ "$held" = "$expected" ] || { echo "cleanup on the target: this invocation does not own the lock; no cleanup attempted" >&2; exit 1; }
fail() { echo "cleanup on the target: $*; cleanup is incomplete: inspect the target before reuse, recover by hand" >&2; exit 1; }
listing() { case $1 in container) docker ps -aq --no-trunc;; network) docker network ls -q --no-trunc;; volume) docker volume ls -q;; esac; }
p=$(cat $d/pid 2>/dev/null || true)
if [ -n "$p" ] && [ "$(readlink /proc/$p/exe 2>/dev/null)" = "$d/apptownd" ]; then
	kill "$p"; for _ in $(seq 1 60); do kill -0 "$p" 2>/dev/null || break; sleep 1; done
fi
processes=$(pgrep -x apptownd) || {
	status=$?
	[ "$status" = 1 ] || fail "cannot list AppTown processes"
}
for p in $processes; do [ "$(readlink /proc/$p/exe)" = "$d/apptownd" ] && fail "the restored AppTown ($p) still runs"; done
for kind in container network volume; do
	[ -f $d/before.$kind ] || fail "no record of the target's $kind""s before the rehearsal"
	now=$(listing $kind) || fail "cannot list Docker $kind""s"
	created=$(printf '%s\n' "$now" | sed '/^$/d' | sort | comm -13 $d/before.$kind -)
	[ -z "$created" ] || printf '%s\n' "$created" | xargs docker $kind rm -f > /dev/null || fail "could not remove the $kind""s the rehearsal made"
	now=$(listing $kind) || fail "cannot list Docker $kind""s"
	[ -z "$(printf '%s\n' "$now" | sed '/^$/d' | sort | comm -13 $d/before.$kind -)" ] || fail "$kind""s the rehearsal made remain"
done
rm -f $d/before.container $d/before.network $d/before.volume $d/apptown.backup $d/passphrase
rm -rf "$data" $d/candidate || fail "could not remove the restored data directory $data"
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
	# The archive opens every secret of the source: it goes, however the run ends.
	if [ -n "$srcdir" ]; then
		on_src "rm -rf '$srcdir'" || { echo "the archive's folder $srcdir is still on $src: remove it by hand" >&2; status=1; }
	fi
	rm -rf "$work"
	exit $status
}
trap finish EXIT

rehearse
