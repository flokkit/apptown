# Secrets

An app needs things it must never commit: a key that signs its cookies,
a token for a payment service, a password for a service AppTown does
not run. AppTown's rule has three parts. The app **declares** which
secrets it needs. A **store** holds the values. AppTown **delivers**
each value to the containers that need it, and writes down that it did,
never what the value was.

## Declaring

The app commits a `secretspec.toml` next to its `.forge/app.yaml`. It
names each secret and says which parts of the app use it. It never
contains a value.

```toml
[project]
name = "quick-poll"
revision = "1.0"

[profiles.default]
SECRET_KEY_BASE = { description = "signs cookies and sessions", generate = true }
STRIPE_API_KEY  = { description = "payments" }

[profiles.preview]
STRIPE_API_KEY  = { required = false }

[scopes.web]
secrets = ["SECRET_KEY_BASE", "STRIPE_API_KEY"]

[scopes.worker]
secrets = ["SECRET_KEY_BASE"]
```

- A **profile** is a set of expectations. `default` is the baseline;
  other profiles inherit it and change what they need to. Each deployment
  and each preview uses one profile, the same one `app.yaml`'s profiles use
  ([Resources and profiles](/docs/guides/contract-reference)): the one chosen
  for it, else `preview` for a preview when either file declares it, else
  `default`. When this file does not declare that profile it uses its own
  `default`, or, if it has none, its first profile in name order.
- A **scope** names a part of the app (by its name in `app.yaml`, or an
  init step as `init-<name>`) and the secrets it receives. With no
  scopes, every part receives every secret. With scopes, a part gets
  only what its scope lists, so a migration password never reaches the
  web server.
- `generate = true` means AppTown makes the value itself, once per
  deployment (all previews share one), and keeps using it. Signing keys
  are the usual case.

This is the SecretSpec format. AppTown reads names, descriptions,
requiredness, defaults, `generate`, profiles and scopes. It does not use
`[providers]` or per-secret `ref` and `providers`: where a value lives is
decided in AppTown, never in the repo.

## Binding

A project's **Settings › Secrets** page shows each deployment and, for
every declared secret, where its value comes from:

- **Type a value.** It goes into AppTown's built-in store and is never
  shown again. The built-in store is part of AppTown itself: there is
  no separate service and nothing to run or connect. AppTown reads and
  writes it in its own process, and the values sit in AppTown's own
  database, encrypted with a key AppTown generated and keeps in that
  same database. That is obfuscation, not protection: **anyone with a dump
  or backup of the database has every value and the key to read them.** It
  is not a vault. For a team, or for
  any value that matters, connect Infisical and name keys there instead.
- **Name an Infisical key.** When this AppTown is connected to Infisical,
  a secret can point at a key there by name. AppTown fetches it at boot
  and never copies it into its database. Use this for anything that
  matters.

A boot stops, before any container exists, when a required secret has
no value for that deployment or preview. The message names the secret and this page.

A deploy checks the same thing earlier, before anything is built. When a
landing or a manual deploy selects a commit, AppTown reads that commit's
declaration and the deployment's bindings and stops if a required secret
has no value there. The deploy step says `missing-settings`, names the
deployment, the secret and the parts it would reach, and points here; no
image is built or published and no secret is generated. A secret AppTown
generates is not missing: it is made at boot. Values a service gives the
app (a database's `DATABASE_URL`) are not secrets and are not checked. If
a store cannot be read (Infisical unreachable), the deploy stops as
`unassessable`, which says nothing about whether the value exists.

On App Platform, a deploy stops before anything is made when a secret or a
declared environment value contains `${…}`: the platform replaces `${NAME}`
with its own variable of that name, in secrets too, and has no way to write
it literally. The message names the variable and the part, not the value.

The early check is not a promise. The boot checks again, so a secret
removed while the release is being built still stops the deploy there.

## Restore rehearsals

A restore rehearsal runs a copy of production data somewhere new, so it is
given almost nothing. It gets a bound secret only when the app's
`rehearsal` profile names it, and that profile must not inherit from
`default`:

```toml
[profiles.rehearsal]
defaults = { inherit = false }
LOOKUP_KEY = { description = "read-only lookups" }
```

A secret the profile names with a `default` gets that value, whether or
not the deployment binds it, and `default = ""` gives it an empty one: the
deployment's own value never reaches the rehearsal. Use it for a credential the app cannot start without but that a
rehearsal must not hold, such as an uploads bucket's write key:

```toml
AWS_SECRET_ACCESS_KEY = { description = "uploads (stand-in)", default = "rehearsal-stand-in" }
```

A name without a `default` gets the deployment's bound value, so list only
what is safe to share with a copy (read-only keys).

A `rehearsal` profile that inherits stops the rehearsal and says so. With
no `rehearsal` profile, no bound secret is delivered. Every
`generate = true` secret gets a new value made for the rehearsal, never the
deployment's. Nothing is required, so a withheld secret never stops it; the
rehearsal records which secrets it ran without. Leave payment, mail and
webhook credentials out: the rehearsal does not block outbound network.

A restored copy (a restore test, or a copy people open to look at production
as it was) follows the same rule with the same `rehearsal` profile. It runs on
production's own platform, so remember that its data can still hold
credentials and webhook addresses of its own: withholding secrets reduces
what it can do outside, it does not stop it.

## What AppTown records

Every preview and deploy records which secrets it delivered, to which
parts, from which store, at which version. Never a value. Unbinding a
secret deletes the built-in store's copy; an Infisical key stays where
it is.

## What is masked, and what is not

Text AppTown records and shows replaces every current value its stores
hold, eight characters or longer, with `[secret]`: a step's recorded output and
evidence, and a chat's messages as the chat and history pages show them.
This catches an accidental echo, not a determined one: a value the agent
encodes, splits or reverses is not recognised. The files the agent writes
for its own conversation, which it resumes from, keep what it saw; so does
the live terminal of a chat.

## What not to do

Do not commit values, in any form. Do not commit an encrypted bundle
that one key unlocks (Rails' `credentials.yml.enc` and `master.key` are
the well-known case): AppTown cannot see inside it and cannot make its
key. Declare each secret on its own and read it from the environment.
