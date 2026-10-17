# Error reporting (GlitchTip) — admin guide

## What it's for

Apps break for real people, and nobody tells you.
Error reporting sends every uncaught error — server or browser — to one
place where it is grouped, counted and kept, with the stack trace and
context. This guide sets that place up once for the whole instance;
each project then attaches with one click.

Platform pattern: you bind one admin credential; forge provisions a
GlitchTip project (and DSN) for each forge project that attaches.

## 1. Stand up GlitchTip

Any Sentry-compatible backend works; GlitchTip is the boring self-hosted
choice. You need: the base URL, an organization, and an auth token created by an
admin user (Profile → Auth Tokens) with scopes:
`project:read project:write team:read team:write org:read`
(team:write only exercised if the org has no team yet — forge then
creates a 'forge' team; projects in GlitchTip live under teams). Store the token in the secrets backend (e.g.
`GLITCHTIP_TOKEN`) — never paste it anywhere.

## 2. Bind it

Integrations → Error reporting → GlitchTip: URL, organization slug,
token as a secret reference. Test verifies the org is reachable and the
token accepted.

## 3. What attachment does

A project attaches from its Settings › Services page, under "Connected
to" (`+ Error reporting`), or with
`POST /projects/{slug}/integrations` `{"capability":"errors"}`. Forge
creates a provider project named after the forge project (adopting it
if it already exists), reads its DSN, and stores it as the attachment's
resource. From then on: app containers (previews and deployed copies)
receive `SENTRY_DSN`/`GLITCHTIP_DSN`, and so do the agents of
implementation steps, so they can fire the test hook; agent steering
gains a generated "error reporting attached" section. Detaching stops
the injection; the provider project and its history are left alone.

Attaching is half the act. The card then offers **Wire it in →**, which
opens a change on the stock `adopt-integration` workflow (or a chat
seeded with the same brief): the agent initialises the SDK from the
env, adds the test hooks, proves an event reached GlitchTip, and the
change goes through review and your OK like any other. The card carries
the state from the app's side — *not wired in yet*, *wiring · #N*,
*wired in* — beside when the provider last saw an event from that
project. `forge integrations` prints the same from a shell.

DSNs are attachment resources, shown redacted in the UI; app containers
and implementation-step agents receive the value.

## 4. The forge itself

The forge reports its own failures the same way it asks every app to.
Attach the `forge` project (Settings › Services on the forge project, or
`POST /projects/forge/integrations {"capability":"errors"}`): the forge
provisions a GlitchTip project named `forge` and from then on forged
sends there, tagged by run and step:

- a panic in any runner, with its stack;
- a step that ended in *error* — the forge's failure to judge, never the
  change's;
- a fleet review that could not run;
- an environment the health controller could not rebuild.

Nothing is sent while the forge project is unattached, nothing is sent
synchronously, and a burst is capped at thirty events a minute so a
crash loop cannot flood the tracker. The event's release is the build's
commit.
