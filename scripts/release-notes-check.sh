#!/bin/sh
# The release notes a version needs, checked at the commit it is built from:
#
#   sh scripts/release-notes-check.sh <version>   (the tag without its v)
#
# releases/v<version>.md must exist with a "## HTTP API changes" and a
# "## Deprecations" section ("None." when there is nothing to say), as the
# compatibility guide promises; and when the API description
# (stock/docs/api/openapi.json) changed since the previous release tag, the
# HTTP section must name a change. It cannot check the rest: whether every
# change is named. candidate.yml runs it; so does scripts/release-prepare.sh
# before the notes land.
set -eu
version=${1:?usage: sh scripts/release-notes-check.sh <version>  (the tag without its v)}
cd "$(git rev-parse --show-toplevel)"
notes=releases/v$version.md
if [ ! -f "$notes" ]; then
	echo "release notes: no $notes (design/operations/releasing.md)" >&2
	exit 1
fi
for section in "## HTTP API changes" "## Deprecations"; do
	if ! grep -qx "$section" "$notes"; then
		echo "release notes: $notes has no \"$section\" section (write \"None.\" under it when there is nothing to say)" >&2
		exit 1
	fi
done
previous=$(git describe --tags --abbrev=0 --match 'v[0-9]*' HEAD 2>/dev/null || true)
if [ -n "$previous" ] && ! git diff --quiet "$previous" HEAD -- stock/docs/api/openapi.json; then
	http=$(awk '/^## HTTP API changes$/ {on=1; next} /^## / {on=0} on && NF' "$notes")
	if [ -z "$http" ] || [ "$http" = "None." ]; then
		echo "release notes: the API description changed since $previous; name each HTTP change under \"## HTTP API changes\" in $notes" >&2
		exit 1
	fi
fi
echo "release notes: $notes"
