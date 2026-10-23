# Operating AppTown

This page routes to operating contracts; it does not certify the state of a
live instance.
The admin guide shipped in the product, [Running AppTown](../../stock/docs/guides/running-apptown.md),
is the short entry point; this page keeps the routing table and host-specific
procedures.

| Task | Primary reference |
| --- | --- |
| Install an AppTown, package the Linux console CLI | [Running AppTown: Install](../../stock/docs/guides/running-apptown.md#install), [Setting up a server](../../stock/docs/guides/server-setup.md), [Repository README](../../README.md) and Makefile |
| Configure authentication and access | [Identity and authority](../subsystems/auth.md) |
| Invite people, grant roles, make tokens | [People, invites, roles and grants](../../stock/docs/guides/people-and-roles.md), [Invites](../subsystems/auth.md#invites), [Authorization decisions](../subsystems/authorization-capabilities.md) |
| Configure the private model gateway and worker networks | [Inference security](../subsystems/inference-security.md) |
| Understand startup, interrupted work and bounded shutdown | [Daemon lifecycle](../subsystems/daemon-lifecycle.md) |
| Make a release: notes, tag, build, frozen upgrade fixture | [Releasing](releasing.md) |
| The GitHub repository's CI and its published images | [GitHub Actions and images](github.md) |
| Run steps, checks, previews, change chats and release builds on a separate worker machine | [Workers](workers.md) |
| Run an AppTown with no local Docker daemon | [Workers: running an AppTown without a local Docker daemon](workers.md#running-apptown-without-a-local-docker-daemon) |
| Recover a lost administrator password | [Locked out of the admin account](#locked-out-of-the-admin-account) |
| Start a project from existing code, or move one to another AppTown | [Starting from existing code](start-from-existing-code.md) |
| Keep projects in a public gitlab.com group, with attributed merge requests | [An AppTown on a public gitlab.com group](gitlab-com.md) |
| Add deployment targets (Docker host, Portainer, App Platform, bucket website) | [Deployment targets](../../stock/docs/guides/deployment-targets.md), [Deploy targets](../subsystems/deploy-targets.md) |
| Diagnose deployments, uncertainty and stale dashboard observations | [Deployment runtime](../subsystems/deployment.md) |
| Back up and restore a whole AppTown (`apptownd backup`, `apptownd restore`); move an AppTown to another host | [AppTown's own backups](../../stock/docs/guides/apptown-backups.md) |
| Deployment backups, take-down and final backups | [Deployment backups](../../stock/docs/guides/deployment-backups.md), [Deployment runtime](../subsystems/deployment.md) |
| Upgrade an AppTown | [Upgrading](../../stock/docs/guides/upgrading.md) |
| Move a deployment made before 1.0 to its names by key | [Deployment runtime](../subsystems/deployment.md) (Names on a host) |
| Manage packs: offer, recommend a version, what-if | [Packs, versions and what-if](../../stock/docs/guides/packs-and-what-if.md), [Ways-of-working versions](../subsystems/ways-of-working-versions.md) |
| A deployment reads "uncertain" or the ledger says `deployment.needs-attention` | [A deployment needs attention](#a-deployment-needs-attention) |
| Diagnose chat tools, private Docker and retained session state | [Runtime guide](../../stock/docs/guides/runtime-environments.md) |
| Export telemetry and import dashboards | [Telemetry](../subsystems/telemetry.md), [AppTown dashboard](../../stock/observability/README.md) |
| Change the database representation | [Schema ownership](../architecture/schema-ownership.md) |

`cmd/apptownd/main.go` defines the daemon flags. `-db-url` / `APPTOWN_DB_URL` selects
PostgreSQL; absent that setting the daemon starts its built-in database container.
`-verify-only` is the read-serving verification mode, without recovery, workers
or scheduler. It still opens/initializes the database: it is not a substitute for
a backup or permission to point an experimental binary at production.

Public, console and inference listeners have different authority. Do not publish
the inference listener through ordinary public ingress. A build succeeding,
a dashboard displaying data, and a revision being deployed are separate facts.
Record the actual deployed revision and smoke results when rolling out.

## Locked out of the admin account

When nobody can sign in as an administrator (the password is lost, and no other
administrator can set a new one from the People page), an operator on the
AppTown's own host can give an existing account a new password:

```sh
umask 077; apptownd -reset-password <username> > new-password
```

Run it as the user the apptown runs as, with AppTown's normal settings (the same
flags or `APPTOWN_*` environment, including `-db-url`), because it opens the
AppTown's database; the serving AppTown can stay up. It makes the same change an
administrator's password edit makes: the account's sessions end and one
`user.saved` receipt records it, with `apptown-operator (host)` as the actor. It
prints only the new random password on stdout (logs go to stderr) and exits
without serving. It refuses an unknown username and never creates an account.
It does not change the account's role, and does not re-enable a disabled
account. It cannot run with `-verify-only`. Remove
`APPTOWN_RESET_PASSWORD` from the environment afterwards if you set it there:
while it is set, apptownd resets and exits instead of serving.

## A deployment needs attention

A deployment whose last attempt was cut off, or whose records disagree, reads
**uncertain** on the project's Settings → Deployments page; a disagreement found
at startup is also in the ledger as `deployment.needs-attention`. AppTown
starts anyway; only that deployment waits, and no new deploy to it is admitted
while its attempt is uncertain.

Press **Check again** on its card (a project maintainer or owner, or an
administrator). AppTown asks the deployment's host what runs and settles it:
running as recorded → deployed; gone, replaced, stopped, or never recorded
as started → failed; host unreachable → still uncertain, so make the host
reachable and press it again. After a failed attempt, release again (it
replaces the failed app container and reuses standing services and their
data) or take the deployment down. Each press is in the
ledger as `deployment.checked-again`. Do not edit the records by hand. See
[uncertain outcomes and restart](../subsystems/deployment.md#uncertain-outcomes-and-restart).
