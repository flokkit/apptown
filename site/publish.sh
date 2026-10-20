#!/bin/sh
# Publishes apptown.dev: this folder's page, the try script and the
# one-server compose file, with the AppTown version they start, then
# refreshes CloudFront's cache.
#
#   sh site/publish.sh <version>
#
# The bucket and distribution are made by tf/products/apptown/site in the
# maintainers' infrastructure repository; neither is a secret. Publishing
# needs AWS credentials that can write the bucket and invalidate the
# distribution.
# <version> is an image tag on ghcr.io/flokkit/apptown, such as 1.0.0.
set -eu

version=${1:?usage: sh site/publish.sh <version>}
: "${SITE_BUCKET:=apptown-dev-site-078142186677}"
: "${SITE_DISTRIBUTION:=E1XMP2SIAVI7FR}"
here=$(cd "$(dirname "$0")" && pwd)

out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT
cp "$here/index.html" "$out/index.html"
sed "s/@VERSION@/$version/" "$here/try.sh" >"$out/try.sh"
# The one-server compose file (Running AppTown, One server), with the same
# version as its image's default.
sed "s/@VERSION@/$version/" "$here/../deploy/one-server/compose.yaml" >"$out/compose.yaml"
if grep -q '@VERSION@' "$out/try.sh" "$out/compose.yaml"; then
	echo "publish: the version was not set in try.sh or compose.yaml" >&2
	exit 1
fi

# Short cache times: the try script changes with each release.
aws s3 cp "$out/index.html" "s3://$SITE_BUCKET/index.html" \
	--content-type 'text/html; charset=utf-8' --cache-control 'max-age=300'
aws s3 cp "$out/try.sh" "s3://$SITE_BUCKET/try.sh" \
	--content-type 'text/plain; charset=utf-8' --cache-control 'max-age=300'
aws s3 cp "$out/compose.yaml" "s3://$SITE_BUCKET/compose.yaml" \
	--content-type 'text/yaml; charset=utf-8' --cache-control 'max-age=300'
# The tour (make tour; committed), as it is.
aws s3 cp "$here/tour/index.html" "s3://$SITE_BUCKET/tour/index.html" \
	--content-type 'text/html; charset=utf-8' --cache-control 'max-age=300'
# cp, not sync: sync lists the bucket first, and the publish role may only
# write objects (apptown-site-publisher: PutObject and CreateInvalidation).
aws s3 cp "$here/tour/" "s3://$SITE_BUCKET/tour/" --recursive --exclude '*' --include '*.png' \
	--content-type image/png --cache-control 'max-age=300'
aws cloudfront create-invalidation --distribution-id "$SITE_DISTRIBUTION" \
	--paths '/' '/index.html' '/try' '/try.sh' '/compose.yaml' '/tour/*' >/dev/null
echo "published apptown.dev with try.sh and compose.yaml starting $version"
