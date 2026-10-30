# WordPress starter

This custom minimal starter follows the Roots Bedrock 1.31.7 layout, with
Composer-locked core, Human Made S3 Uploads (media in a private bucket,
served by the site) and an owned AppTown Site theme.

## Build and run

    docker build -t wordpress-starter .

Prepare a MariaDB 11.4.13 database and application user on a Docker network named
`site`. Supply DB_HOST, DB_PORT, DB_NAME, DB_USER and DB_PASSWORD. On AppTown the
`mariadb` need supplies them and retains its database volume across releases.
Uploads need a bucket: S3_BUCKET, AWS_REGION, AWS_ENDPOINT_URL_S3,
AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY; on AppTown the `s3` need supplies them.
Use uncommitted, permission-restricted env files. Put the shared DB, URL, S3
and KEY/SALT settings in `site.env`; put only `WP_ADMIN_PASSWORD` and optional
initial administrator/title settings in `install.env`. Run the same image for both:

    docker run --rm --network site --env-file site.env --env-file install.env wordpress-starter /app/bin/initialize
    docker run --rm --network site --env-file site.env -p 8080:8080 wordpress-starter

AppTown serves the site at its own address; elsewhere, and for WP-CLI runs that
write content or links, set WP_HOME to the public URL. The bucket stays
private and objects carry no ACL: media URLs are the site's own
(/app/uploads/...), and web/media.php reads each object from the bucket.

Supply all eight KEY/SALT values named in secretspec.toml. AppTown generates and
retains them per deployment with WP_ADMIN_PASSWORD, delivered only to init-install.
Outside AppTown generate these once and retain them privately. Never commit
credentials or put passwords in command arguments. Initial defaults are user
`editor`, email `editor@example.invalid` and title `WordPress`; set WP_ADMIN_USER,
WP_ADMIN_EMAIL and WP_SITE_TITLE in init's environment before first install.

Repeat init preserves users, passwords, content and options, updates the core
schema and activates the owned theme/plugin. No example content is installed.
Retrieve the initial password from its authorized binding. Preview's staging
profile excludes indexing without changing the database's production setting.

## Changes and checks

Edit web/app/themes/apptown-site. Change composer.json and resolve its lock with:

    docker run --rm -v "$PWD:/app" -w /app composer:2.10.3 update --no-dev --no-install
    docker build -t wordpress-starter .

Commit both manifests. Runtime source is unwritable by the non-root user;
dashboard code editing, installation and unattended updates are disabled. `/tmp`
is writable; this is not a read-only-rootfs claim. `/healthz.php` checks the web
process only. Check pages, login, uploads and thumbnails after web replacement.

## Data and recovery

On AppTown each deployment and preview has its own bucket, and backups copy its
objects and MariaDB, not as one snapshot. Elsewhere, arrange and test both. Core updates
do not run every plugin migration. Default request-driven cron serves one replica;
an external coordinated scheduler is a separate operational choice.
A deploy certifies no backwards compatibility and does no automatic data rollback.
A maintenance mode and multiple replicas are unavailable. See AppTown's WordPress
guide for deployment and recovery obligations.
