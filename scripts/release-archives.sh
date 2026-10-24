#!/bin/sh
# The release archives, one per platform, and SHA256SUMS over them:
#
#   sh scripts/release-archives.sh <version> <revision> <out-dir>
#
# <version> is the tag without its v (1.0.0-rc.6), as the images are tagged;
# <revision> the commit. The release workflow (.github/workflows/images.yml)
# runs this on every v* tag; it runs the same on a Mac to try before a tag.
# apptownd embeds the web bundle, so web/dist must be built first.
#
# This script owns the archive names. The guides that name them (server-setup,
# upgrading, split-install) say apptown-<version>-<os>-<arch>.tar.gz; change
# both together.
#
# Each archive holds one folder, apptown-<version>-<os>-<arch>/, which the
# guides unpack with --strip-components=1 into /opt/apptown (or
# /opt/apptown-worker on a worker):
#   bin/apptownd, bin/apptown, bin/apptown-worker  for the platform
#   bin/apptown-linux   the CLI AppTown hands its containers: linux on the
#                       same architecture (a Mac's containers are linux too)
#   share/apptown/updates  the stock and tool versions the binaries read at
#                       start (scripts/stock-files.go)
#   deploy/apptown-agent, deploy/systemd   the agent image's recipe, units
#   scripts/apptown-db-backup.sh             the checked backup upgrading runs
#   LICENSE, NOTICE, README.md, THIRD_PARTY_NOTICES
set -eu
[ $# -eq 3 ] || { echo "usage: $0 <version> <revision> <out-dir>" >&2; exit 2; }
version=$1 revision=$2 out=$3
case $version in v*) echo "version without its v: ${version#v}" >&2; exit 2 ;; esac
repo=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo"
[ -f web/dist/index.html ] || { echo "web/dist is not built (cd web && npm ci && npm run build)" >&2; exit 1; }

platforms="linux/amd64 linux/arm64 darwin/arm64 darwin/amd64"
ld="-X github.com/flokkit/apptown/internal/version.Release=v$version -X github.com/flokkit/apptown/internal/version.Revision=$revision"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$out"

build() { # <os> <arch> <command> <output>
	CGO_ENABLED=0 GOOS=$1 GOARCH=$2 GOFLAGS=-buildvcs=false go build -trimpath -ldflags "$ld" -o "$4" "./cmd/$3"
}

# The linux CLIs, built once: each is apptown-linux in both archives of
# its architecture.
for arch in amd64 arm64; do
	build linux "$arch" apptown "$work/apptown-linux-$arch"
done

GOOS='' GOARCH='' python3 scripts/third-party-notices.py >"$work/THIRD_PARTY_NOTICES"
# The update files every archive carries, named with the release.
GOOS='' GOARCH='' go run -ldflags "$ld" scripts/stock-files.go -o "$work/updates" >/dev/null

for p in $platforms; do
	os=${p%/*} arch=${p#*/}
	name=apptown-$version-$os-$arch
	d=$work/$name
	mkdir -p "$d/bin" "$d/deploy" "$d/scripts" "$d/share/apptown"
	cp -R "$work/updates" "$d/share/apptown/updates"
	for c in apptownd apptown-worker; do
		build "$os" "$arch" "$c" "$d/bin/$c"
	done
	if [ "$os" = linux ]; then
		cp "$work/apptown-linux-$arch" "$d/bin/apptown"
	else
		build "$os" "$arch" apptown "$d/bin/apptown"
	fi
	cp "$work/apptown-linux-$arch" "$d/bin/apptown-linux"
	cp -R deploy/apptown-agent deploy/systemd "$d/deploy/"
	cp scripts/apptown-db-backup.sh "$d/scripts/"
	cp LICENSE NOTICE README.md "$work/THIRD_PARTY_NOTICES" "$d/"
	COPYFILE_DISABLE=1 tar -C "$work" -czf "$out/$name.tar.gz" "$name"
	echo "$out/$name.tar.gz"
done

(cd "$out" && for f in apptown-"$version"-*.tar.gz; do
	if command -v sha256sum >/dev/null; then sha256sum "$f"; else shasum -a 256 "$f"; fi
done) >"$out/SHA256SUMS"
echo "$out/SHA256SUMS"
