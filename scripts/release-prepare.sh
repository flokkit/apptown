#!/bin/sh
# What a release's notes change carries besides the notes:
#
#   sh scripts/release-prepare.sh <version>   (the tag without its v)
#
# run in a lane, after writing releases/v<version>.md. It adds the previous
# release's upgrade fixture (internal/apptown/testdata/upgrade/<previous>),
# which that release's candidate made and its publication attached to its
# GitHub Release as upgrade-fixture-<previous>.tar.gz, checked against the
# digest GitHub records for the asset, so the upgrade test covers it from
# here on; and checks the notes (scripts/release-notes-check.sh). Land the
# result as an ordinary change (design/operations/releasing.md). The
# previous release is the newest published (not draft) GitHub Release.
set -eu
version=${1:?usage: sh scripts/release-prepare.sh <version>  (the tag without its v)}
cd "$(git rev-parse --show-toplevel)"
repo=${APPTOWN_REPOSITORY:-flokkit/apptown}
previous=$(gh release list -R "$repo" --exclude-drafts --limit 1 --json tagName -q '.[0].tagName')
case $previous in v[0-9]*) ;; *) echo "release prepare: no published release to carry a fixture from ($previous)" >&2; exit 1 ;; esac
p=${previous#v}
dir=internal/apptown/testdata/upgrade/$p
if [ -d "$dir" ]; then
	echo "release prepare: $dir is already here"
else
	asset=upgrade-fixture-$p.tar.gz
	want=$(gh api "repos/$repo/releases/tags/$previous" -q ".assets[] | select(.name == \"$asset\") | .digest")
	case $want in sha256:*) ;; *) echo "release prepare: $previous has no $asset with a recorded digest" >&2; exit 1 ;; esac
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	gh release download "$previous" -R "$repo" -p "$asset" -D "$tmp"
	if command -v sha256sum >/dev/null 2>&1; then got=$(sha256sum "$tmp/$asset"); else got=$(shasum -a 256 "$tmp/$asset"); fi
	got=sha256:${got%% *}
	[ "$got" = "$want" ] || { echo "release prepare: $asset is $got; GitHub records $want" >&2; exit 1; }
	[ "$(tar -tzf "$tmp/$asset" | cut -d/ -f1 | sort -u)" = "$p" ] || { echo "release prepare: $asset holds more than $p/" >&2; exit 1; }
	tar -xzf "$tmp/$asset" -C internal/apptown/testdata/upgrade
	echo "release prepare: added $dir from $previous"
fi
sh scripts/release-notes-check.sh "$version"
