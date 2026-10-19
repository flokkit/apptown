# Operating Forge

This page routes to operating contracts; it does not certify the state of a
live instance.
The admin guide shipped in the product, [Running a forge](../../stock/docs/guides/running-a-forge.md),
is the short entry point; this page keeps the routing table and host-specific
procedures.

| Task | Primary reference |
| --- | --- |
| Install a forge, package the Linux console CLI | [Running a forge: Install](../../stock/docs/guides/running-a-forge.md#install), [Repository README](../../README.md) and Makefile |
| Configure authentication and access | [Identity and authority](../subsystems/auth.md) |
| Invite people, grant roles, make tokens | [Running a forge: People](../../stock/docs/guides/running-a-forge.md#people-invites-roles-and-grants), [Invites](../subsystems/auth.md#invites), [Authorization decisions](../subsystems/authorization-capabilities.md) |
| Configure the private model gateway and worker networks | [Inference security](../subsystems/inference-security.md) |
| Understand startup, interrupted work and bounded shutdown | [Daemon lifecycle](../subsystems/daemon-lifecycle.md) |
| Make a release: notes, tag, build, frozen upgrade fixture | [Releasing](releasing.md) |
| The GitHub repository's CI and its published images | [GitHub Actions and images](github.md) |
| Run steps, checks, previews, change chats and release builds on a separate worker machine | [Workers](workers.md) |
| Run a forge with no local Docker daemon | [Workers: running a forge without a local Docker daemon](workers.md#running-a-forge-without-a-local-docker-daemon) |
| Recover a lost administrator password | [Locked out of the admin account](#locked-out-of-the-admin-account) |
| A personal token is refused with "the token has no purpose" | [Tokens made before purposes](#tokens-made-before-purposes) |
| Start a project from existing code, or move one to another forge | [Starting from existing code](start-from-existing-code.md) |
| Keep projects in a public gitlab.com group, with attributed merge requests | [A forge on a public gitlab.com group](gitlab-com.md) |
| Add deployment targets (Docker host, Portainer, App Platform, bucket website) | [Running a forge: Deployment targets](../../stock/docs/guides/running-a-forge.md#deployment-targets), [Deploy targets](../subsystems/deploy-targets.md) |
| Diagnose deployments, uncertainty and stale dashboard observations | [Deployment runtime](../subsystems/deployment.md) |
| Back up and restore the forge's own database; move a forge to another host | [Running a forge: The forge's own backups](../../stock/docs/guides/running-a-forge.md#the-forges-own-backups) |
| Deployment backups, take-down and final backups | [Running a forge: Deployment backups](../../stock/docs/guides/running-a-forge.md#deployment-backups), [Deployment runtime](../subsystems/deployment.md) |
| Upgrade a forge | [Running a forge: Upgrading](../../stock/docs/guides/running-a-forge.md#upgrading) |
| Approve ownership for deployments made before ownership was recorded | [Deployment runtime](../subsystems/deployment.md) (the one-time inspection) |
| Manage packs: offer, recommend a version, what-if | [Running a forge: Packs](../../stock/docs/guides/running-a-forge.md#packs-versions-and-what-if), [Ways-of-working versions](../subsystems/ways-of-working-versions.md) |
| A deployment reads "uncertain" or the ledger says `deployment.needs-attention` | [A deployment needs attention](#a-deployment-needs-attention) |
| Diagnose chat tools, private Docker and retained session state | [Runtime guide](../../stock/docs/guides/runtime-environments.md) |
| Export telemetry and import dashboards | [Telemetry](../subsystems/telemetry.md), [the Forge dashboard](../../stock/observability/README.md) |
| Change the database representation | [Schema ownership](../architecture/schema-ownership.md) |


`cmd/forged/main.go` defines the daemon flags. `-db-url` / `FORGE_DB_URL` selects
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
forge's own host can give an existing account a new password:

```sh
umask 077; forged -reset-password <username> > new-password
```

Run it as the user the forge runs as, with the forge's normal settings (the same
flags or `FORGE_*` environment, including `-db-url`), because it opens the
forge's database; the serving forge can stay up. It makes the same change an
administrator's password edit makes: the account's sessions end and one
`user.saved` receipt records it, with `forge-operator (host)` as the actor. It
prints only the new random password on stdout (logs go to stderr) and exits
without serving. It refuses an unknown username and never creates an account.
It does not change the account's role, and does not re-enable a disabled
account. It cannot run with `-verify-only` or `-drain-local-docker`. Remove
`FORGE_RESET_PASSWORD` from the environment afterwards if you set it there:
while it is set, forged resets and exits instead of serving.


## Tokens made before purposes

Every personal token is made for a command line (`cli`, acts as its person)
or a coding agent (`agent`, bounded like an OAuth connection: it may not
approve, land or administer). Tokens made before purposes existed have none
and are refused at both `/api/` and `/mcp/user`; the forge logs "the token has
no purpose". Nothing guesses which ones agents hold. Either:

- **Replace them (no database access needed).** An administrator signs in with
  their password (browser sessions and trusted-proxy sign-in never depended on
  a token), mints replacements on the People page or with
  `forge token create <name> --purpose cli|agent`, and revokes the old ones. If
  the administrator password is lost too, use
  [Locked out of the admin account](#locked-out-of-the-admin-account) first.
- **Classify them in place.** With a database dump taken first:
  `UPDATE auth_tokens SET purpose='cli' WHERE name IN (…)`, and `'agent'`
  for the tokens coding agents hold. List them with
  `SELECT name, username, role, last_used FROM auth_tokens WHERE revoked_at IS NULL AND purpose=''`.

## A deployment needs attention

A deployment whose last attempt was cut off, or whose records disagree, reads
**uncertain** on the project's Settings → Deployments page; a disagreement found
at startup is also in the ledger as `deployment.needs-attention`. The forge
starts anyway; only that deployment waits, and no new deploy to it is admitted
while its attempt is uncertain.

Press **Check again** on its card (a project maintainer or owner, or an
administrator). The forge asks the deployment's host what runs and settles it:
running as recorded → deployed; gone → failed; host unreachable → still
uncertain, so make the host reachable and press it again. Each press is in the
ledger as `deployment.checked-again`. Do not edit the records by hand. See
[uncertain outcomes and restart](../subsystems/deployment.md#uncertain-outcomes-and-restart).

