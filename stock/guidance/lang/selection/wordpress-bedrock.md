---
name: lang/selection/wordpress-bedrock
tags: lang,php,wordpress,storage
checks: [{"name":"runtime-stage-not-build-image","expects":"the final Docker stage runs PHP/Apache, not the Composer dependency installer","severity":"medium","evaluator":"sh","sh":"[ -f Dockerfile ] || exit 0; image=$(awk 'toupper($1)==\"FROM\"{v=$2} END{print v}' Dockerfile); case \"$image\" in composer:*|composer@*|docker.io/library/composer:*|docker.io/library/composer@*) echo 'Composer is the dependency build image, not the web runtime'; exit 1;; esac","title":"Keep the dependency installer out of the runtime base"},{"name":"uploads-through-s3-need","expects":"S3 Uploads writes to the deployment's own s3 need: .forge/app.yaml declares it, and secretspec.toml declares none of the variables the need gives the app nor S3_PUBLIC_URL","severity":"high","evaluator":"sh","sh":"grep -q '\"humanmade/s3-uploads\"' composer.json 2>/dev/null || exit 0; bad=0; sed -n '/^needs:/,/^[^ ]/p' .forge/app.yaml 2>/dev/null | grep -Eq '^  s3:' || { echo '.forge/app.yaml declares no s3 need: uploads belong in the deployment'\"'\"'s own object storage'; bad=1; }; for n in $(grep -Eo '^(S3_BUCKET|AWS_REGION|AWS_ENDPOINT_URL_S3|AWS_ACCESS_KEY_ID|AWS_SECRET_ACCESS_KEY|S3_PUBLIC_URL)[[:space:]]*=' secretspec.toml 2>/dev/null | tr -d ' ='); do if [ \"$n\" = S3_PUBLIC_URL ]; then echo 'secretspec.toml declares S3_PUBLIC_URL: the s3 need gives no public address, so media is served by the site (web/media.php)'; else echo \"secretspec.toml declares $n, which the s3 need gives the app\"; fi; bad=1; done; [ \"$bad\" = 0 ]","occurrences":"lines","title":"Keep uploads in the deployment's s3 need"}]
---
Use the seeded Bedrock layout: Composer's committed `composer.lock` installs
WordPress core in `web/wp`, plugins in `web/app/plugins`, and the owned theme
lives in `web/app/themes/forge-site`. The public document root is `web`.
Code changes, core/plugin upgrades and theme changes are reviewed image builds.
Never install code from the dashboard or edit a running container. The runtime
is non-root and cannot write application source; `/tmp` remains writable.

Build and test with the Docker recipes in README.md. PHP and Composer are not
promised on the agent host. Raise dependency versions deliberately with the
pinned Composer tool image, commit the resulting lock, then rebuild and test.
Do not fetch an unpinned WordPress archive in a release entrypoint.

The declared needs are MariaDB and `s3`. The app reads their injected env:
`DB_*`, and `S3_BUCKET`, `AWS_REGION`, `AWS_ENDPOINT_URL_S3`,
`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`.
The site's address is the deployment's own: `config/application.php` takes the
request's Host when it matches an origin in `FORGE_ORIGINS` (the front door
routes by Host and passes it on) and refuses any other Host with 400. Never use
`FORGE_ORIGINS` itself as the URL. A bound `WP_HOME` overrides it with one
canonical address. WP-CLI has no request: run a WP-CLI command that creates
content or writes links with `WP_HOME=<the deployment's URL>` in its
environment, or what it stores names `http://localhost`. Nobody enters an
S3 setting: never declare the need's variables or `S3_PUBLIC_URL` in
`secretspec.toml`. AppTown generates stable signing keys/salts and the initial administrator
password. Only initialization receives the password; it never appears in argv.

Initialization installs an absent database, updates the core schema of an existing
installation and activates the owned theme/S3 plugin. The bucket exists
before init: init makes no bucket. It does not reset users,
passwords, posts or options. Plugin migrations may need a separate reviewed
procedure. Demo content is a deliberate one-time site operation, not init data.

Uploads go through Human Made S3 Uploads to the `s3` need's bucket, with
the need's endpoint and path-style addressing. The need gives no public
address, so media URLs are the site's own: `S3_UPLOADS_BUCKET_URL` is
`WP_CONTENT_URL`, so an upload is at `/app/uploads/<path>`. Apache rewrites
`^app/uploads/` to `web/media.php`, which reads the object `uploads/<path>`
with the need's key: GET and HEAD only, nothing outside `uploads/`, 404 for a
missing object, 502 when the store does not answer, `nosniff` always, and
anything but raster images, audio, video and PDF sent as a download. Keep
that boundary when changing it; it is the only way to the bucket. Objects
carry no ACL: `web/app/mu-plugins/s3-compatible.php` removes `ACL` from
every command the plugin's client sends, and `S3_UPLOADS_OBJECT_ACL` is
`private` as a fallback. Never make an object or the bucket public, presign
a URL for a browser, or point media URLs at the store. Uploads on local disk
or a data directory are not a fallback.

An existing project from the earlier starter (six S3 settings in
`secretspec.toml`, `S3_PUBLIC_URL`, `public-read`) moves to this: add
`s3: {}` under `needs:` in `.forge/app.yaml`; remove `S3_BUCKET`,
`AWS_REGION`, `AWS_ENDPOINT_URL_S3`, `S3_PUBLIC_URL`, `AWS_ACCESS_KEY_ID` and
`AWS_SECRET_ACCESS_KEY` from `secretspec.toml`'s profile and scopes; in
`config/application.php` set `S3_UPLOADS_BUCKET_URL` to `WP_CONTENT_URL` and
`S3_UPLOADS_OBJECT_ACL` to `private`; add the ACL-removing middleware to the
mu-plugin, `web/media.php`, and its rewrite in `config/apache.conf`, as the
current starter has them. Media already in an external bucket is not moved
by this; copying it into the need is a separate, reviewed data operation.

The fast `/healthz.php` endpoint proves PHP/Apache readiness without database or
S3 access. Separately test front page, login, database writes, upload, thumbnail
URLs fetched through the site's front door, and replacement-container persistence. WordPress cron runs on requests for
this single replica. A coordinated external scheduler is a separate operational
choice. Replicas above one, protected maintenance, automatic data restoration and
coordinated database/S3 backups are unavailable. See the shipped WordPress guide
for migration, backup and restore obligations.

For a deployment-owned need, inspect `GET /api/v1/dependencies` for catalogue
`sizes`. Select an offered `needs.<kind>.size` or a profile's need `size`; the
catalogue supplies matched memory and startup settings. Only MariaDB `small`
(128M) ships now, for local containers. Do not invent names or assume PostgreSQL,
managed instances or shared databases support them. Do not combine a selected
size and explicit need resources, including inherited profile values. An operator
cap override is separately recorded and does not retune the database. A changed
command on real/unknown data requires reviewed maintenance before deployment;
AppTown refuses it before resizing. Measure normal requests and maintenance jobs.
