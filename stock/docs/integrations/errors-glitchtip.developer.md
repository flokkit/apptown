# Error reporting — developer guide

## What it's for

When your app breaks for a real person — a crash, a
500, a JavaScript error on their screen — you find out, with the stack
trace, the browser or request, and how many people it hit, instead of
waiting for someone to tell you. Every error your app doesn't catch is
sent to one place where it is grouped, counted and kept.

If your project is attached to the errors integration, error reporting
is provisioned for you: a place for the errors to land, and the DSN in
your app's environment. Wiring the app into it is a change — the card
under Settings › Services opens it for you with **Wire it in →**.

## What you get

- A provider-side project (same name as yours) collecting errors, at
  the instance's GlitchTip.
- `SENTRY_DSN` / `GLITCHTIP_DSN` in your app's public part on a Docker
  host, in previews and deployments alike. Nothing to configure, nothing
  in the repo. Other parts, and deployments on Portainer or App Platform,
  do not receive them; [the app contract](/docs/guides/contract-reference)
  says where each value arrives.
- The same DSN in the environment of implementation-step agents, so the
  agent wiring it in can fire the test hook and check the provider
  (`apptown integrations`) before it reports.

## What your code must do

Initialize a Sentry-compatible SDK at startup, reading the DSN from the
environment — and only from the environment:

- absent DSN = reporting disabled (local dev works untouched)
- never hardcode a DSN; never commit one
- set the release to the commit SHA if available (run receipts and
  error reports then cross-reference)

A static site (a `static:` part: files served as they are) cannot read
its environment. Its page loads `/_forge/env.js`, which AppTown serves
with the values safe for any visitor to read, and takes the DSN from
`window.__FORGE_ENV__`:

    <head>
      <script src="/_forge/env.js"></script>
      <script>
        const dsn = (window.__FORGE_ENV__ || {}).SENTRY_DSN;
        // start the browser SDK only when dsn is set
      </script>
    </head>

It must be the first script in `<head>`, written exactly as shown, with no
attribute but `src` (a CSP `nonce` is fine), and the page must not
use `<base href>`. AppTown checks, against the running preview, that the
values are served and the page's HTML loads them first. That does not show
that reports arrive: a content security policy can still stop the script.

These values are delivered on a Docker host and on a bucket website. A
deploy of a static site that has them to Portainer or App Platform is
refused before it starts.

The agents know this: the observability guidance steers them to wire
the SDK exactly this way, and the `adopt-integration` workflow that
**Wire it in →** starts holds them to it — build, run with the env
absent, run with it set, fire the hook, confirm the event, then a
review and your OK.

## Test hooks — the house standard

Every app with error reporting carries two hooks, so anyone can prove
the wiring in any deployment or preview by hand,
without reading code:

- **Server apps:** open `/_forge/boom` on the app. It captures one
  tagged error (message "forge test boom", tag `forge.probe=boom`) and
  returns 500. Not authenticated; it does nothing else.
- **Browser apps and games:** type `boom!!!` anywhere on the page — the
  letters, then three exclamation marks. The browser SDK sends the same
  tagged error. The special characters are deliberate: it must never
  fire by accident while playing.

Neither hook is authenticated, on purpose. Client-side error reporting
is playable-with by anyone on the internet by its nature — the DSN ships
to every browser, and a JS console can send the provider anything. The
hook adds no capability an outsider doesn't already have, so gating it
would cost you the hand test and buy nothing. Leave it open.

Within a minute the event shows at the provider. The project's
Settings › Services card says when it last saw one, and so does
`apptown integrations` from a shell (inside an agent step, or with
`-p <project>`).

## How you know it's wired in

The card under Settings › Services carries the state from the app's
side: *not wired in yet* (nothing reads the DSN), *wiring* (the change
that wires it is in progress), *wired in* (that change shipped) — and,
next to it, when the provider last saw an event from this app. "Wired
in" with no event seen is a claim; the test hooks turn it into proof.

## Where errors land

The card names the provider-side project (`providerProject`) and lists
its unresolved issues; open GlitchTip at the instance's URL to read
them. Triggered an error and don't see it? Check the app container
actually restarted since attachment (the DSN arrives at container
start).
