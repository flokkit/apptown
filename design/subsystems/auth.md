# Authentication and authority

## Authentication establishes a caller

There are three configured authentication modes, selected by `apptownd --auth`:

| Mode | Identity source |
| --- | --- |
| `internal` | Forge accounts, password login and session cookies; first-account setup is a separate explicit operation |
| `trusted-header` | A configured proxy username header plus the configured shared proxy secret |
| `open` (default) | Caller-supplied `X-AppTown-Actor`, or anonymous attribution; this is not authentication of that name |

Personal bearer tokens work alongside these modes. On `/api/`, a
present but invalid bearer token is refused; it does not fall back to a cookie or
open identity. Otherwise authentication checks the mode's identity source. In
internal mode, console/run session tokens have their own bounded instrument path.
MCP, Git, OAuth and the inference listener each verify their protocol credentials.

`--auth-header` names the trusted proxy user header. `--auth-proxy-secret-env`
names the environment variable containing its shared secret. Configure these with
an actual trusted proxy boundary. Open-mode attribution does not protect an
exposed instance from impersonation. Browser-origin/cookie rules are described in
[browser security](browser-security.md).

A person's name may not take a form the forge reads as something else:
`identity.ValidName` refuses one containing `/` (a chat, run or connection)
or ` via ` (someone acting through something), one of the forge's own names
(`system`, `apptown-system`), control characters, and leading or trailing
spaces. The trusted header and the open-mode actor are refused with 401;
members, group members, grants (at the shared grant writer), token holders,
invites, new accounts and the startup administrator seed are refused as
invalid. New accounts, invites and group members also keep the slug rule.
An existing account is changed whatever its name's syntax (a single sign-on
name such as an email address); a row recorded before names were checked
whose name fails the rule may only be disabled, which is never refused.

## Authorization checks current authority

Root comes from a live `instance-administrator` grant, directly or through group
membership. A displayed account role is derived information, not an independent
root switch. No instance-wide profile governs root reach.

- Root reaches every project after credential/instrument limits are applied.
- Root is exempt from approver eligibility and author self-approval refusal.
  Workflow still owns quorum and whether the step can be decided.
- For everyone else, named approvers and the per-project author-segregation rule
  apply. Groups can carry grants as well as participate in approval eligibility.
- A personal token's role is a ceiling. An admin-bounded token does not promote
  its owner; the owner must currently hold the authority. OAuth scopes and
  instrument bindings also narrow rather than create authority.
- A step agent's run token has no person whose current grants could bound
  it, so its reads are implied only inside its project. On a record of the
  whole instance (the ledger, people, groups, grants) it holds exactly its
  table (`access.Decide`): revoking the shared read grant stops runs as well
  as people. A chat's reads go on through its person's current grants.
- A personal token has a purpose, chosen when it is made, with no default.
  A `cli` token is its person's own command line and acts with their
  authority. An `agent` token is a coding agent's and is bounded exactly like
  an OAuth connection holding every scope (`access.CredentialCovers`). That
  rule is applied in `access.Decide` and in Identity's administration check,
  so an agent token cannot approve, land, administer, or mint itself a `cli`
  token, whatever its person holds. A token with no purpose (one made before
  purposes existed) is refused until an operator records its purpose. The
  operations guide has the recovery without database access (sign in, mint
  replacements).
- Disconnecting a coding-agent connection is a person's act even on their own
  connections: an OAuth connection or agent token cannot end the others.
- Routes that change where a project's code goes (initialise its repository,
  attach a remote), commit to its default branch outside a Change (seed a
  toolchain) or give a model write access to a Change branch (open a chat)
  are capability routes, not membership routes, so credential limits apply
  before any effect.
- Missing caller context never implies a worker or root identity. Admitted
  workers have explicit scoped authority and domain-specific eligibility.

The exact capabilities and role bundles are defined in `internal/access`, not a
second prose catalogue. `identity.Service.Require` reads current facts and
propagates policy/required-receipt failures. Transports establish verified caller
context; owners check capabilities and resource identity before effects.
Transport-only authorization is not a substitute for an owner's command check.

The route table requires an explicit policy: capability/resource, administrator,
or a documented public/protocol/owner/filtered contract. For an exceptional route, its named protocol or owner implements that contract.
See `routes.go`, `route_table.go` and their policy tests.

## Setup, credentials and lifetime

Initial setup is governed by a durable `setup_completed` marker. Bootstrap and
explicit startup root seeding record authority and setup together. Under
internal authentication bootstrap also needs the setup token the forge writes
to `<data>/setup-token` (mode 0600) while no account exists
(`identity/setup_token.go`); it is compared in constant time and removed once
the first account commits. Open authentication refuses a listener other
machines can reach unless `-open-on-network` (`forge.OpenExposure`). The
credential doors share a per-client rate limit (`credentialLimiter.door` in `internal/forge/internal_auth.go`). Losing a
password or the last usable account does not reopen bootstrap. Administrator
changes protect the last enabled administrator.

Personal-token issuance, listing and revocation are checked Identity operations.
Plaintext is returned only after issuance commits, metadata excludes it, and
revocation is one-way. Personal tokens currently have no expiry. Credential
verification at the transport does not promise cancellation of an already-admitted
command when the credential is revoked later.

### Invites

In internal mode an administrator adds a person by invite instead of choosing
their password: People → Invite a person, `apptown invite <username> [--role …]`,
or `POST /api/v1/invites`. An invite names one account that has no password
yet, a role (member or admin), an optional label (a name or email address kept
for the administrator; Forge sends no email) and a lifetime of 1 to 30 days,
seven by default. A member invite is refused for a name that already holds
administrator authority (directly or through a group); make an admin invite
instead, so the invite's role is what the person gets. The response carries the link `<public origin>/invite/<token>`
once; only the token's SHA-256 hash is stored, and receipts never contain it.
The token is 32 random bytes, so it is looked up by hash rather than compared,
and there is no separate rate limit. A new invite for the same name withdraws
the pending one, so a lost link is replaced rather than left working.

The invite link's page needs no session. It shows who invited whom, and the
invitee chooses a password under the ordinary password rule. Acceptance is
one transaction: the invite must still be pending and unexpired, its inviter
must still be an enabled instance administrator, and the account must still
have no password. It then sets the password (a `user.saved` receipt by the
invitee), grants the administrator role in the inviter's name when the invite
said admin, marks the invite used, records `user.invite-accepted` and starts a
session. When the account will be an administrator after acceptance, from the
invite or from authority it has gained since, acceptance from outside the
administrator networks is refused without using the invite. A used, withdrawn or expired invite says so and changes
nothing. Administrators list pending invites and withdraw them
(`user.invite-revoked`); used and withdrawn invites stay as records.

A password reset link is the same kind of link for an account that already
has a password: People → reset password, `apptown password-link <username>
[--days N]`, or `POST /api/v1/password-links {username, days?}`. It needs the
same authority as an invite (an agent token or OAuth connection is refused),
is refused for a name without a password and for a disabled account, and has
no role and no label. It lasts 1 to 30 days, three by default, with the same
token handling, the same `/invite/<token>` page (which says it is a password
reset) and the same rule that a newer link for the name withdraws the older
one. Until it is used the account's password and sessions keep working. Use
is one transaction: the link must be pending and unexpired, its maker must
still be an enabled instance administrator, and the account must still have
a password and not be disabled. It then replaces the password, which ends
every session the account had and stops the old password, leaves the
account's grants exactly as they were, marks the link used, records
`user.password-link-used` (by the account, naming who issued it) and starts a
session. Issuing records `user.password-link`. The administrator networks
apply as for an invite when the account is an administrator. Personal access
tokens and agent connections the account holds are not touched; the reset
action says so and points to revoking them on People when the account may be
compromised. Outside `--auth internal` the routes refuse with a sentence
saying to reset the password, or add the person, in the identity provider,
and People shows that sentence instead of the reset action.

A password sign-in checks the password against the hash it read without a
lock (bcrypt is slow), then starts its session in a transaction that locks
the account's row and refuses if the hash is no longer the account's. A
password change or reset holds the same row lock while it replaces the
password and ends the sessions, so a sign-in racing a reset either commits
first and is ended by it, or is refused.

A link's token is a live credential in its URL. The link page moves it out
of the address bar as soon as it is read (leaving `/invite`, so reloading
asks for the link again), the page is served with `Referrer-Policy:
no-referrer`, and browser error reports have every `apptown_invite_` token
replaced before they are sent.
Setting a password directly (People → set password, People → Add a person
with a password you choose instead, or `POST /api/v1/users`) remains for
scripted setup.
An administrator disables or enables an account on People, or with
`POST /api/v1/users {username, disabled}`. Disabling ends its sessions and
stops sign-in; the account, its record and its grants stay. A change that
names no role leaves the account's authority as it is; only `role: admin` or
`role: member` changes it.
An account is real once it has a password, has signed in (a session, an
agent connection or a used personal token) or appears as an actor on the
ledger; single sign-on and proxy accounts never have a password, so this is
what keeps them. A real account is disabled, never removed, so a later
trusted-header sighting finds it still disabled. Only a name that never
signed in or acted can be removed (`DELETE /api/v1/users/{name}`), and its
direct grants, memberships, personal tokens, sessions, agent connections
and pending invites are revoked in the same transaction; the
`user.name-removed` receipt lists them. A later sighting of the name starts
an account that holds none of them. Wherever authority is decided, a name with no
account is refused like a disabled one, never treated as enabled; only the
forge's own names (`system`, `apptown-system`, `run/<id>`) act without one. A
grant to a person records that person's account with it (never signed in,
so it can still be removed), as the grant and member doors always did.

Console sessions and running step attempts use lifecycle-scoped credentials;
a console's idle process stop is not session deletion. The inference gateway
rechecks workload authority while streaming. Provider credentials remain in the
gateway's private provider resolution; they are not the runtime's Forge token.
See [the inference package guide](../../internal/forge/inference/README.md).

## Visible projects and aggregate reads

Identity evaluates a requested read capability against candidate project resources,
respecting instrument bounds before enumeration. Project, Workflow, ADR and
Adoption readers apply the resulting scope before aggregation, limits and counts.
Project visibility is not inferred from the ability to read runs or from a single
instance-level authorization decision.

Fact memoization lasts one visibility operation only; later operations read grants
and account state again. Enumeration does not emit decision receipts for every
candidate. An empty visible set is different from a policy/database failure.
A person holding only Project creator and the roles on their own projects can
open every page the menu shows them: aggregate pages are filtered to their
projects, and instance-only pages are left out of the menu and refused (see
[authorization decisions](authorization-capabilities.md)); the executable
evidence is `checkMemberScopedReads` in the route policy evidence suite.

The rules every owner keeps are in
[authorization decisions](authorization-capabilities.md#rules-every-owner-keeps).
The [threat model](../architecture/threat-model.md) and
[threat register](../architecture/threat-register.md) track security risks and
unimplemented controls.

## Implementation references

- [internal/forge/auth.go](../../internal/forge/auth.go)
- [internal/forge/identity/service.go](../../internal/forge/identity/service.go)
- [internal/forge/identity/token_commands.go](../../internal/forge/identity/token_commands.go)
- [internal/forge/identity/invites.go](../../internal/forge/identity/invites.go)
- [internal/forge/identity/visibility.go](../../internal/forge/identity/visibility.go)
- [internal/forge/routes.go](../../internal/forge/routes.go)
- [internal/forge/route_table.go](../../internal/forge/route_table.go)
