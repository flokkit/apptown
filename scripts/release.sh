#!/bin/sh
# Build a release's artifacts from a clean checkout at its tag.
#
#   sh scripts/release.sh <version>        (or: make release VERSION=<version>)
#
# <version> is the tag without its v (1.0.0, 1.0.0-rc.1). The checkout must be
# clean and HEAD exactly the tag v<version>, so every artifact names a commit
# anyone can check out. Writes dist/release/v<version>/:
#   forge-v<version>-<os>-<arch>.tar.gz   for linux/amd64, linux/arm64,
#                                         darwin/arm64: forged, forge,
#                                         forge-worker, forge-linux (the CLI
#                                         forged mounts into containers, for
#                                         the containers' Linux), the agent
#                                         image's build context and the
#                                         example systemd units
#   SHA256SUMS                            over the archives
#   RELEASE-NOTES.md                      the release's notes and the build's facts
# Publishing them is a separate step. The order of a release:
# design/operations/releasing.md.
#
# The notes are releases/v<version>.md in the tagged tree, with a
# "## HTTP API changes" and a "## Deprecations" section ("None." when there
# are none), as the compatibility guide promises. A release without them,
# or whose API description changed since the previous release while its
# notes name no HTTP change, builds nothing.
#
# Before tagging, run `make upgrade-test` on the candidate commit: every
# earlier release's frozen forge (internal/forge/testdata/upgrade) upgrades
# to it and keeps its records. This script runs it again at the tag and
# builds nothing when it fails.
# After tagging, freeze the release's forge for the releases after it, at the
# tag, and commit the result on main:
#   go test ./internal/forge -run '^TestAnUpgradeKeepsWhatThePreviousReleaseRecorded$' -update-upgrade-fixture=<version>
set -eu
version=${1:?usage: sh scripts/release.sh <version>  (the tag without its v)}
tag=v$version
repo=$(git rev-parse --show-toplevel)
cd "$repo"

if [ -n "$(git status --porcelain)" ]; then
	echo "release: the checkout has changes; a release is built from a clean checkout" >&2
	git status --short >&2
	exit 1
fi
at=$(git describe --tags --exact-match HEAD 2>/dev/null || true)
if [ "$at" != "$tag" ]; then
	echo "release: HEAD is not the tag $tag (it is ${at:-untagged}); check out the tag first" >&2
	exit 1
fi
commit=$(git rev-parse HEAD)

notes=releases/$tag.md
if [ ! -f "$notes" ]; then
	echo "release: no release notes at $notes; write them before tagging (design/operations/releasing.md)" >&2
	exit 1
fi
for section in "## HTTP API changes" "## Deprecations"; do
	if ! grep -qx "$section" "$notes"; then
		echo "release: $notes has no \"$section\" section (write \"None.\" under it when there is nothing to say)" >&2
		exit 1
	fi
done
# The one omission a script can see: the API description changed since the
# previous release and the notes say there is no HTTP change.
previous=$(git describe --tags --abbrev=0 --match 'v[0-9]*' HEAD^ 2>/dev/null || true)
if [ -n "$previous" ] && ! git diff --quiet "$previous" HEAD -- stock/docs/api/openapi.json; then
	http=$(awk '/^## HTTP API changes$/ {on=1; next} /^## / {on=0} on && NF' "$notes")
	if [ -z "$http" ] || [ "$http" = "None." ]; then
		echo "release: the API description changed since $previous; name each HTTP change under \"## HTTP API changes\" in $notes" >&2
		exit 1
	fi
fi

# The web bundle is embedded in forged; built once for every platform, by
# the upgrade test's make target (Go embeds it there too), which must pass
# before anything is built.
(cd web && npm ci --silent)
if ! make upgrade-test; then
	echo "release: the upgrade test failed at $commit; nothing is built" >&2
	exit 1
fi

out=$repo/dist/release/$tag
rm -rf "$out"
mkdir -p "$out"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Every third-party licence, copyright and notice the binaries and the web
# bundle carry, verbatim, with the source address MPL code needs; one file
# for every archive.
python3 scripts/third-party-notices.py > "$work/THIRD_PARTY_NOTICES"

ldflags="-X flokkit.io/forge/internal/version.Release=$tag"
for platform in linux/amd64 linux/arm64 darwin/arm64; do
	os=${platform%/*} arch=${platform#*/}
	name=forge-$tag-$os-$arch
	dir=$work/$name
	mkdir -p "$dir/bin" "$dir/deploy"
	for cmd in forged forge forge-worker; do
		CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "$ldflags" -o "$dir/bin/$cmd" "./cmd/$cmd"
	done
	# Containers are Linux on the host's architecture.
	CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "$ldflags" -o "$dir/bin/forge-linux" ./cmd/forge
	cp -R deploy/forge-agent deploy/systemd "$dir/deploy/"
	# Apache-2.0 travels with every copy: the licence and the notice, and the
	# third-party notices beside them.
	cp LICENSE NOTICE "$work/THIRD_PARTY_NOTICES" "$dir/"
	# No macOS metadata in the archive: GNU tar warns about it on unpacking.
	COPYFILE_DISABLE=1 tar --no-xattrs -C "$work" -czf "$out/$name.tar.gz" "$name"
done
# Checksums: sha256sum (Linux) or shasum (macOS); one line per archive, or
# the release fails.
if command -v sha256sum >/dev/null 2>&1; then
	sum="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
	sum="shasum -a 256"
else
	echo "release: no sha256sum or shasum on this machine" >&2
	exit 1
fi
(cd "$out" && $sum *.tar.gz > SHA256SUMS)
archives=$(ls "$out"/*.tar.gz | wc -l)
if [ "$(grep -c '\.tar\.gz$' "$out/SHA256SUMS")" -ne "$archives" ]; then
	echo "release: SHA256SUMS does not name every archive" >&2
	exit 1
fi

facts=$(go run scripts/release-facts.go deploy/forge-agent)
worker_protocol=$(echo "$facts" | sed -n 's/^WORKER_PROTOCOL=//p')
cli_protocol=$(echo "$facts" | sed -n 's/^CLI_PROTOCOL=//p')
agent_image=$(echo "$facts" | sed -n 's/^AGENT_IMAGE=//p')
{
	echo "# Flokkit $tag"
	echo
	cat "$notes"
	echo
	echo "## This build"
	echo
	echo "- Commit: \`$commit\`"
	echo "- Worker protocol: $worker_protocol; CLI protocol: $cli_protocol (Running a forge, Versions)"
	echo "- Agent image recipe: \`$agent_image\` (\`deploy/forge-agent\` in the archive). Workers build it when they start. A forge that runs work itself builds \`forge-agent\` only when it is missing: on install, and on an upgrade whose recipe name changed, run \`docker build -t forge-agent deploy/forge-agent\` in the installation directory"
	echo "- Platforms: linux/amd64, linux/arm64, darwin/arm64; checksums in SHA256SUMS"
	echo "- Licence: Apache-2.0 (LICENSE, NOTICE)"
	echo
	echo "Install: Running a forge, Install. Upgrade: Running a forge, Upgrading."
} > "$out/RELEASE-NOTES.md"

echo "release $tag: $out"
ls -1 "$out"
