# WordPress with reviewed code and its own media storage

Choose the `wordpress-bedrock` pack when creating a project. Its starter is an
ordinary Bedrock application: Composer locks WordPress and its plugins, the custom
theme lives in Git, and Docker builds the runnable application. PHP and Composer
run in containers; this pack does not promise a host PHP toolchain. The starter's
README owns its build commands and exact environment variables.

## What changes where

Change themes, plugins and WordPress versions in the repository. Update the
Composer lock, build and test the image, review a preview, then land and ship it.
The dashboard cannot install, update or edit executable code, and unattended code
updates are disabled. The app runs as an unprivileged user that cannot write its
source tree. Temporary files are writable; the root filesystem is not mounted
read-only by this declaration.

Editors can use the normal WordPress dashboard to write articles, edit pages and
upload media. MariaDB holds users, content and settings. The app declares a local
`mariadb` need with a deployment-owned database directory; replacing the web
container preserves that database. This declaration does not provision a managed
MariaDB service. Database connection variables are injected from the need.

Uploaded media lives in the deployment's own object storage: the app
declares the `s3` need, and S3 Uploads writes to the bucket the forge makes
for it, with the key the forge gives it. Nobody enters an S3 setting. A
preview gets its own empty bucket and never main's key.

The need gives no public address for objects, so the site serves its own
media. An upload's URL is the site's, `/app/uploads/2026/10/photo.png`, as
it would be on local disk; Apache hands those paths to `web/media.php`,
which reads that one object from the bucket. It answers GET and HEAD only,
reaches nothing outside `uploads/`, passes on byte ranges and conditional
requests, answers 404 for a missing object and 502 when the store does not
answer. Media shares the site's origin, so only raster images, audio, video
and PDF are shown in the browser; anything else, HTML and SVG included, is
a download. No object or bucket is public, and no object carries an ACL.
Every media byte goes through PHP, with the two workers the starter's memory
budget allows: right for an institution's site, not for a media-heavy one.

The deployment's backups copy the bucket with the database: each object's
bytes, content type and other headers, through the S3 API. A restore puts
them back, and the site serves them again. The two are copied one after the
other, not as one snapshot.

This pack runs on a Docker host and on Portainer, where both needs are
local services beside the app. App Platform fulfils `s3` but refuses
MariaDB, so the pack does not run there. Standalone use supplies the same
five variables from any S3-compatible store.

## Installation and deployment

Initialization installs a fresh site once, using the deployment's generated
administrator password, then activates the supplied theme and S3 plugin.
The bucket exists before initialization, so it makes none. It runs
core database updates for an existing installation without resetting users or
content. It does not automatically seed demonstration content. Existing plugin
migrations require that plugin's own documented procedure.

The site's address is the address each deployment is served at, so nobody
supplies it: the starter takes a request's Host when it matches an origin in
`FORGE_ORIGINS` (where the forge's front door routes the deployment) and refuses
any other Host with 400. `FORGE_ORIGINS` may list several origins or wildcards
and is never used as the URL itself. To pin one canonical address, bind
`WP_HOME` for that deployment; a preview `WP_HOME` binding is shared by all of
a project's previews, so leave it unbound there. WP-CLI has no request, so
without `WP_HOME` it sees the placeholder `http://localhost`. First
initialization removes what the install stores under that placeholder (the
administrator's website and WordPress's sample post and pages); if it stops
between the install and that cleanup, delete the sample content in the
dashboard. Give any later WP-CLI run that creates content or writes links
(seeding, import, search-replace) the deployment's address for that run with
`WP_HOME=<its URL>` in its environment; otherwise what it stores keeps the
placeholder. Post GUIDs from such a run are identifiers WordPress does not use
as links. Readiness uses
a separate fast endpoint; it does not prove database or object-store health.
Functional checks must render a page and upload/read actual media, including
thumbnail creation and media access after replacing the web container.

The current runtime supports one replica. Larger `replicas` values are rejected.
The starter uses WordPress's normal request-triggered cron. Before moving to a
coordinated external scheduler, disable request-triggered cron and arrange exactly
one scheduler for the deployment. Object storage alone does not make the whole
application safe to scale: cron, caches, sessions and plugins need review too.

## Sizes and environments

The MariaDB dependency catalogue offers `small`: a 128 MiB dedicated container
with matched low-concurrency database settings. Inspect `GET /api/v1/dependencies`
for the sizes each kind actually offers. Select a supported name; do not copy
server flags or guess that every engine offers the same sizes.

```yaml
needs:
  mariadb:
    version: "11.4.13"
profiles:
  preview:
    mariadb: {size: small}
    web: {resources: {memory: 192M}}
```

This keeps the starter's current unsized main database at 256M. Main may select
small explicitly too. A need selects either `size` or explicit `resources`,
including values inherited by a profile. A named size is available for local
containers (Docker/Portainer), not managed database instances or shared databases.
There are no PostgreSQL presets or expert database tuning overrides yet.

The stock WordPress image passed bounded page, login, media upload and media
regeneration checks at 192M; 128M OOMed during regeneration. MariaDB small passed
the same fixture at 128M. These are workload measurements, not universal minima.
Measure your plugins, concurrency and maintenance jobs before reducing limits.
Existing operator memory-cap overrides remain separate decisions and do not
retune database buffers.

Changing the startup command of an existing database with real or unknown data
is refused before its cap changes. Plan reviewed maintenance with verified copies
and an explicit migration/recreation procedure; selecting a size does not execute
that procedure. Synthetic disposable data follows its existing replacement policy.
Rebuilding a generation restores its recorded command and cap, even after the
catalogue changes.

## Recovery obligations

The MariaDB catalogue declares logical SQL export, import, restore and verification.
It copies tables, views and triggers, and excludes routines, events, accounts and
grants. Export requires transactional tables and no concurrent schema changes.
Verification compares complete table definitions and rows against a quiescent
restored database. A destination receiving writes can legitimately fail verification.
Restore replaces tables present in the copy; unexpected destination objects remain
and make verification fail. Use a clean destination for an exact recovery.

The forge's copies of the database and of the bucket are separate copies,
not a coordinated multi-store recovery point. Before a change that may
rewrite data incompatibly, stop writes and make and test copies of every affected
store under an operator's recovery procedure. A previous container image may not
understand the new schema or changed objects. Current deployment policy is
`compatible-update`; `protected-maintenance` is reserved and fails as unsupported.
This pack does not provide automatic rollback or execute that maintenance procedure.
