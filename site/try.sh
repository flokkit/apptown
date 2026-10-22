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
#   curl -fsSL https://apptown.dev/try | sh -s -- trust      trust its certificate
#   curl -fsSL https://apptown.dev/try | sh -s -- uninstall  remove it, its data
#                                                            and that trust
#
# AppTown serves itself and your apps at https://...localhost addresses, with
# a certificate from its own certificate authority. That authority can vouch
# only for localhost names, never for a real website. On macOS, starting
# asks before it adds the authority to your login keychain, and uninstall
# takes it away again; on Linux it prints the commands for your system and
# browser stores. A copy is kept in ~/.apptown.
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
CA_DIR=${APPTOWN_HOME:-$HOME/.apptown}
CA_FILE=$CA_DIR/apptown-local-ca.pem
CA_NAME="AppTown local CA"
KEYCHAIN=$HOME/Library/Keychains/login.keychain-db

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

fingerprint() {
	openssl x509 -noout -fingerprint -sha1 -in "$1" | sed 's/.*=//; s/://g'
}

# keychain_has <file>: 0 if the login keychain holds this certificate, 1 if
# it does not, and a failure when the keychain cannot be read: an unreadable
# keychain is never taken for one without it.
keychain_has() {
	listing=$(security find-certificate -a -Z "$KEYCHAIN" 2>&1) ||
		fail "could not read your login keychain: $listing"
	case $listing in *"SHA-1 hash: $(fingerprint "$1")"*) return 0 ;; esac
	return 1
}

# untrust_macos <file>: removes the certificate and the trust settings
# add-trusted-cert made for it (-t). Reports and returns 1 when it cannot.
untrust_macos() {
	keychain_has "$1" || return 0
	if ! out=$(security delete-certificate -t -Z "$(fingerprint "$1")" "$KEYCHAIN" 2>&1); then
		say "could not remove AppTown's certificate from your login keychain: $out" >&2
		return 1
	fi
}

# On Linux every distribution and browser keeps its own store, so the script
# points to the one place that lists the steps instead of keeping a copy.
GUIDE=https://github.com/flokkit/apptown/blob/main/stock/docs/guides/trial.md#trust-the-trials-certificate

linux_trust_steps() {
	say "To trust it on Linux, follow \"Trust the trial's certificate\" (system store, Chrome and Firefox):"
	say "  $GUIDE"
}

linux_untrust_steps() {
	say "AppTown's certificate may still be trusted on this computer. To remove it, see"
	say "  $GUIDE"
	say "then delete $CA_DIR."
}

explain_trust() {
	say ""
	say "AppTown serves itself and your apps at https://...localhost addresses on this"
	say "computer, with a certificate from its own certificate authority. To show them"
	say "without a browser warning, this computer has to trust that authority."
	say "It can vouch only for localhost names, never for a real website."
	say "A copy is in $CA_FILE."
}

# trust_ca: copies the running trial's authority out, explains, and on macOS
# asks and adds it; on Linux prints the commands. Declining is fine: the
# browser then warns at each address.
trust_ca() {
	mkdir -p "$CA_DIR"
	new=$(mktemp)
	# The data folder is data; a trial volume made before 1.0 has it as forge.
	if ! docker exec "$NAME" sh -c 'cat /var/lib/apptown/data/local-tls/ca.pem 2>/dev/null || cat /var/lib/apptown/forge/local-tls/ca.pem' >"$new" 2>/dev/null || [ ! -s "$new" ]; then
		rm -f "$new"
		say "could not read AppTown's certificate from the container; your browser will warn. Try: ... | sh -s -- trust"
		return 0
	fi
	os=$(uname -s)
	# A new trial makes a new authority: on macOS the old one's trust goes first.
	if [ -f "$CA_FILE" ] && ! cmp -s "$new" "$CA_FILE" && [ "$os" = Darwin ]; then
		untrust_macos "$CA_FILE" || fail "remove the previous trial's certificate (\"$CA_NAME\") in Keychain Access, then run this again."
	fi
	mv "$new" "$CA_FILE"
	case $os in
	Darwin)
		keychain_has "$CA_FILE" && return 0
		explain_trust
		say "macOS will ask for your password to add it to your login keychain; uninstall removes it."
		if ! confirm "Trust AppTown's certificate on this computer?"; then
			say "not trusted; your browser will warn at each address. To trust it later: ... | sh -s -- trust"
			return 0
		fi
		security add-trusted-cert -r trustRoot -k "$KEYCHAIN" "$CA_FILE" ||
			fail "macOS did not add the certificate; nothing else was changed."
		say "trusted. Restart your browser if it is open."
		;;
	Linux)
		explain_trust
		linux_trust_steps
		;;
	*)
		say "this script can help trust the certificate on macOS and Linux only; your browser will warn at each address."
		;;
	esac
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
	trust_note=''
	if [ -f "$CA_FILE" ]; then
		case $(uname -s) in
		Darwin)
			if untrust_macos "$CA_FILE"; then
				rm -f "$CA_FILE"
			else
				left="$left certificate"
			fi
			;;
		*)
			linux_untrust_steps
			trust_note=' Remove the certificate as shown above.'
			;;
		esac
	fi
	if [ -n "$left" ]; then
		fail "not everything was removed (left:$left); fix the error above and run uninstall again."
	fi
	if [ -n "$trust_note" ]; then
		say "removed the container, its data and its image.$trust_note"
	else
		say "removed the container, its data, its image and the trust in its certificate."
	fi
	exit 0
	;;
trust)
	present inspect "$NAME" || fail "not installed; start it first."
	trust_ca
	exit 0
	;;
start) ;;
*) fail "unknown command '$1'. Use no argument to start, or stop, trust or uninstall." ;;
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
	trust_ca
	say "already running at https://localhost:$PORT"
	open_link "https://localhost:$PORT"
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

trust_ca
say "AppTown is ready: $link"
say "Stop it:      curl -fsSL https://apptown.dev/try | sh -s -- stop"
say "Remove it:    curl -fsSL https://apptown.dev/try | sh -s -- uninstall"
open_link "$link"
