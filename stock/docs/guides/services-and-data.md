# Services and data

An app declares the services it needs in the `.forge/app.yaml` committed with its source. AppTown reads that contract for the revision it is previewing or deploying. This keeps a service change reviewable with the code that uses it.

## Declare a service

Use `needs:` for a capability in the dependency catalog. For example:

```yaml
app:
  web:
    build: .
    ports: ["8080"]
    public: true
needs:
  postgres: { version: "16" }
  redis: { version: "7" }
```

The catalog includes Postgres, MariaDB (see the [WordPress guide](/docs/guides/wordpress)), Redis, S3-compatible object storage (locally, the [Versity S3 Gateway](https://github.com/versity/versitygw) keeping each bucket as a folder of plain files), and AI models (`llm`). AI models are not run by AppTown: each deployment and each preview gets its own OpenRouter key with a spending limit, once the project has AI models attached; see the [AI models guide](/docs/llm/openrouter). Each target fulfils a declared need using its available provider and gives the app the variables below. A target that cannot fulfil a need refuses the release before anything is made, in a sentence; see [Previews and deployments](/docs/guides/container-lifecycle) for what each target runs.

Error reporting and other integrations are not needs: they are attached to the project under **Settings › Services › Connected to**, the app is given their variables while they are attached (where they arrive, by target and part, is in [the app contract](/docs/guides/contract-reference)), and it must run without them. Declaring one under `needs:` is refused.

## What each need gives the app

Each need has one contract, the same on every target that fulfils it and in a preview. The rules for every need:

- **Read what you are given.** Every connection value comes from these variables. Never hard-code a host, name or credential, and never set a need's credential: a `password` under `needs:` is refused. Values differ between targets and may change between releases.
- **The credential reaches only this deployment's own database, bucket or store.**
- **It exists before the app's first init step starts**, ready for that credential. The app never creates its database or bucket.
- **A preview gets its own, empty**, never the deployment's data or credentials.
- **One name, one meaning.** A contract whose needs would give the app the same variable twice is refused, naming both.

| Need | Variables | The app may rely on | Outside the contract |
| --- | --- | --- | --- |
| `postgres` | `DATABASE_URL`, `PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER`, `PGPASSWORD`, `PGSSLMODE` | a database of its own (the `database` parameter, default `app`) owned by its user (`user`, default `app`), an ordinary user on every target: schemas, tables, views, functions, sequences, and the extensions Postgres marks trusted (`pgcrypto`, `citext`, `pg_trgm`, `uuid-ossp`, `hstore`, `btree_gin`, `btree_gist`, `ltree`, `unaccent` and others); no other user on the server may connect to its database | superuser powers, refused on every target: creating roles or databases, server settings, untrusted extensions (`dblink`, `postgres_fdw`, `pg_stat_statements`, `pg_buffercache`, `plpython3u` and every other the server lists as not trusted in `pg_available_extension_versions`), other databases; a password of its choosing. Migrations must need only the owner. The servers AppTown starts beside its own steps (tests and agent steps, `forge dev`) make the user a superuser, so test runners can make their own databases and load fixtures |
| `mariadb` | `DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USER`, `DB_PASSWORD`, `DB_SSL_MODE` (DISABLED or REQUIRED) | a database of its own and a user with every right on it | TLS being off: read `DB_SSL_MODE` |
| `redis` | `REDIS_URL` (`redis://` or `rediss://`, with a user and password when the server needs them) | its data kept across restarts and backed up where the target backs up, unless the need is `disposable: true` | `REDIS_HOST` and `REDIS_PORT`, which are not given: a host and port cannot carry a password or TLS |
| `s3` | `AWS_ENDPOINT_URL_S3`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION`, `S3_BUCKET` | a key that can put, get, list and delete objects in its bucket; path-style addressing (`forcePathStyle`, `use_path_style_endpoint`), which every target answers | a public address for objects (none is given: serve them through the app); object ACLs; listing buckets or deleting its own bucket; an object's ETag or modification time surviving a backup (its bytes and headers do) |
| `llm` | `OPENROUTER_API_KEY`, `OPENROUTER_BASE_URL` | its own key and spending limit, per deployment and per preview | sharing a key with another deployment |

Credentials on a Docker host, on Portainer and in a preview: each deployment and each preview has its own password for its Postgres, MariaDB and Redis, and its own key, secret and gateway root key for its object store, made by AppTown. They are the same at every deploy of that deployment, and no other deployment or preview has them. AppTown does not store them: it derives them from its secret store's key, so anyone who holds AppTown's database can work them out, as they can read the store's own values (the built-in store is obfuscation, not a vault). Every one of these services sets its credential again each time it starts, so a credential never sits in the service's data. A failed boot's output (a migration's error, the app's last log lines) has these values replaced with `[secret]` before AppTown keeps it; the app's live logs are its own output and are shown as it wrote them. The servers AppTown starts beside its own steps (tests and agent steps, `forge dev`) use the catalogue's fixed values, since they hold no deployment's data. App Platform makes its own credentials.

Object storage on a Docker host, on Portainer and in a preview: the store makes the bucket `app` and the app's account before it says it is healthy, so the app starts only after them. The app's key owns that bucket and is refused making one. The store's root key is AppTown's and is not given to the app.

Object storage on App Platform: the deployment gets a DigitalOcean Spaces bucket of its own, named `forge-<project>-<deployment>-<random>`, made before the app starts, in the target's region (`syd1` for `syd`), and a Spaces key for that bucket alone. The key's secret is kept in AppTown's secret store and given to the app as an App Platform secret. AppTown copies the bucket's objects with the deployment's other data, from the target's copy runner into the backup store, and checks each copy by filling a new store with it; a restore test, a rehearsal and a start from a copy fill a new bucket from it. Taking the deployment down copies and checks the bucket first and then deletes it and its key; a take-down without a final copy deletes the key and keeps the bucket, listed under retained resources until a person deletes or keeps it (an evaluation's synthetic data is deleted with it). The key can make an object public with a `public-read` ACL, which publishes it at Spaces' address; that is outside the contract, so do not.

For a container outside the catalog, declare an `app:` part with `image:`. That part belongs to the app, like its code; it is different from a catalog need, and you change it through a change like any other source. The Services page edits `needs:` only and leaves every `app:` part as written. Credentials are declared separately, in `secretspec.toml` ([Secrets](/docs/guides/secrets)); every file AppTown reads is listed in [the app contract](/docs/guides/contract-reference).

In the project UI, **Settings › Services › Runs against** shows the declared catalog needs. Adding or changing one saves a Change that updates `.forge/app.yaml`; review and preview that Change before landing it. The Change touches only the needs you changed: a need you left alone keeps its entry as written. If the app's services change on `main` while you are editing, the save is refused and the page shows what changed, so you can keep your edits on top or discard them. **Connected to** lists integrations, such as error reporting, which are attached in AppTown rather than declared as app dependencies. At project creation, choosing a pack can preselect services from the same catalog; you can adjust that selection, and it is written as the new project's first commit. The pack's starter files and its suggested services are independent, so check that the resulting app boots with the services it actually uses.

## Initialization and data

Put a migration, or any command that must finish before the app starts, in `init:`. When init steps run, in what order, and what waits for them differs by target: see "Startup order" in [the app contract](/docs/guides/contract-reference). Init steps run for previews and deployments alike, so put there only what is right for both.

A preview's services start empty and are thrown away; a deployment keeps its own. A preview is not a copy of production data.

### Each resource's lifecycle

One table for what a deployment runs and keeps. A deployment owns what it
creates: taking it down deletes everything rebuildable, and its data is kept or
deleted by an explicit choice ([Previews and deployments](/docs/guides/container-lifecycle)).

| Resource | Created by | Owned by | Sized by | Backed up | Deleted when |
| --- | --- | --- | --- | --- | --- |
| An app part's container | each deploy, from the release | the deployment | the part's `resources`, its profile, an operator's override | not copied: rebuilt from the release | replaced each deploy; removed at take-down |
| A need's service on a Docker host (Postgres, MariaDB, Redis, object storage) | the first deploy that needs it; kept across deploys | the deployment | the need's `resources` or catalogue `size`, its profile, an override; cap changes in place, lifted limits remake on the same volume; a service that sets its configuration at every start (Postgres, MariaDB, Redis, object storage) is stopped and remade on the same volume when that configuration moved, not for a new version of Postgres, MariaDB or Redis; other changed commands refuse for real/unknown data | its data volume, by the deployment's backups (on Portainer too, through Portainer) | at take-down, after a complete final backup (an evaluation's synthetic data may go without one) |
| A need's service on Portainer | the first release that needs it, as a service in the project's stack; kept across releases | the deployment | the need's `resources` or catalogue `size`, its profile, an override; each release applies the stack as rendered, without the Docker host's checks, so whether a changed need (a new image or command) suits the data already there is for the person releasing to judge | its data volume, by the deployment's backups, through Portainer | at take-down, after a complete final backup |
| A managed Postgres on App Platform | the first release that needs it | the deployment | the integration's database size | the provider's daily backups, and AppTown's copies where a copy runner is set | at take-down, after a final copy is filled and verified elsewhere |
| A data directory (`dataDir`) | the first deploy | the deployment | not sized: the host's disk | the deployment's backups, on a Docker host and on Portainer | at take-down after a complete final backup (an evaluation's synthetic data may go without one) |
| A preview's containers and needs | each preview boot | the change | the `preview` profile | never: disposable | with the change, or when it goes unlooked-at |
| The agent's step and chat containers | each step or chat | the run or chat | AppTown's task limits | the chat's saved folder | when the step ends; an idle chat is evicted |

What fills up a fixed host is shown on its target page: what it has, the limits
the running containers hold, what they use, and the disk Docker keeps its data
on.

### Backups of a service's data

AppTown backs up each need that keeps data the way its catalogue entry
declares, on a Docker host and on Portainer
([Backups and restore](/docs/guides/container-lifecycle) says what each
target gets):

| Need | Backup | Restore |
| --- | --- | --- |
| Postgres | `pg_dump` of the running database | `pg_restore` into it, then a check confirms every table of the copy is there |
| MariaDB | a logical SQL export of its tables, views and triggers; not routines, events, accounts or grants. The export needs transactional tables and no schema change while it runs | the export is imported over the database's tables, then a check compares every table's definition and rows with the copy. A database written to meanwhile, or one holding objects the copy lacks, fails that check: restore into a clean one for an exact recovery |
| Redis | `SAVE`, then a copy of `dump.rdb`, so it holds what Redis held at that moment | Redis is stopped, the file is put in place, Redis is started and loads it, and a check confirms it holds as many keys as the copy. An older set holding only an archive of Redis's files is not read by Redis: take a new set |
| S3 | every object through the S3 API, run beside the store: its bytes, content type, cache control, content disposition, encoding and language, expiry and user metadata; not ACLs, tags or versions | the bucket is put back exactly as the copy holds it (an object made since is deleted), then a check confirms every object's key, size, headers and bytes, so the restore says "verified". An object uploaded in parts comes back with a new ETag. An older set may hold the store as a file copy, without object metadata; its restore says "files copied, not verified" |

#### How a catalogue kind declares its copy

A dependency kind declares its copy once, under `copy:`. The same commands
run inside the need's own container on a Docker host, given the connection
it serves (or, with `runIn: client`, in a throwaway container of its
`client` image that shares the need container's network, for a service
whose image has no copy tool), and from the target's copy runner for a
managed instance:

- `ext` and `export`: writes a consistent copy to stdout;
- `verify`: run with the copy on stdin, exits 0 only when the service holds
  that copy;
- `restore`: puts a copy back in place, over what is there, reading it from
  stdin. A kind that keeps local data declares it with its export.
  `restoreStopped: true` runs it with the service stopped, in a container
  of the same image on the same volumes, and starts the service afterwards;
- `format`, `client` and `import`, together, for a copy that can fill a
  new, empty place: `import` fills one, in a throwaway container of the
  `client` image. A copy is filled only into a kind of the same `format`;
- `env`: the connection variables the commands read, built from `{host}`,
  `{port}`, `{database}`, `{user}`, `{password}` and `{sslmode}`;
- `caveat`: in words, what this kind's copy does not hold. It is recorded
  with every copy and shown with the set, in the question before a restore
  and in the restore's result.

A kind's `credentials:` are the values AppTown sets and an app may not.
Those it lists under `generate:` are made for each deployment and preview
(the value under `credentials:` is then only for AppTown's own step
servers and `forge dev`), and a kind with a local service that generates
one declares `local.configAtStart: true`: its service must set them again at
every start, never keep the first in its data.

A kind with no `copy:`, or one with only a `caveat`, gets a file copy of its
data directory and an unverified restore. Under `cloud.<provider>`,
`reach: {how: connect, from: named-host, credential: app-user}` says
AppTown copies a managed instance there from the target's copy runner as the
app's user, and `native` names the provider's own backups AppTown shows
beside its copies. Postgres declares these for DigitalOcean. A need the app
marks `disposable: true` (a cache) is never copied and starts empty.

