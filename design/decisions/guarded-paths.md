# ADR: Guarded paths — auth for embedded operational surfaces

Status: **proposed** (2026-09-02). Companion to
design/decisions/needs-and-fulfillment.md (the fulfillment doctrine this applies to
identity) and the django-fullstack guidance (whose env-superuser stance is
the stopgap this replaces).

## Context

Apps grow operational sub-surfaces: Django's admin, back-office pages,
/metrics, feature toggles. The recipe-book walkthrough shipped a Django
admin that was reachable by anyone on the intranet, unstyled, and had no
login that worked — attack surface pretending to be a feature. The
guidance now demands a shipped admin be whole (statics + env-driven
superuser), but env passwords are per-app credentials — the exact thing
central identity exists to kill. Meanwhile every member's identity
already exists in the forge (or will, once it signs in people beyond
`admin`), and every forge app
is only reachable through the forge's ingress.

Hand-rolling auth per app is the worst of all options: agents wire it
inconsistently, people share passwords, revocation means editing N apps.

## Decision (proposed)

**Auth for operational surfaces is a NEED, declared in the contract and
fulfilled at the edge, per environment.**

```yaml
app:
  web:
    public: true
    guard:
      /admin: operators      # role names, not mechanisms
      /metrics: operators
```

- **Declared with the app** (travels with the code, read as of the commit):
  which paths are guarded, and for whom — role names at the altitude of
  intent (`operators`, `members`, `anyone-signed-in`), never mechanisms.
- **Defined by an ENFORCEMENT CONTRACT, never a mechanism** (Traefik will
  not be there on AWS; it is an implementation detail). What `guard:` MEANS, on every rail:
  1. unauthenticated requests to a guarded path never reach the app;
  2. requests that do reach it carry verified identity in the canonical
     header set (`X-Forge-User`, `X-Forge-Roles`);
  3. role names are platform vocabulary, not mechanism config.
  Each deploy renderer owns satisfying that contract and documents its
  rendering (and degradations) in the deploy-targets mapping:
  - LAN/compose (today) → Traefik forwardAuth against forged sessions or
    Authentik — the LAN renderer's implementation detail, nothing more;
  - ECS/ALB → ALB per-rule OIDC authenticate actions (native path-based
    authn; role checks ride the normalized header claims);
  - k8s → ingress/Gateway auth; Cloud Run → fronting LB or the floor
    below.
- **The portable floor: a renderer-injected auth proxy IN the rendered
  deployment.** When a target has no native mechanism, the renderer puts
  an oauth2-proxy-class container in front of the app part in the stack
  it emits — enforcement travels with the rendered artifact, anywhere
  containers run, and normalizes headers so the app sees one identity
  shape on every rail. No ambient infrastructure is ever assumed.
- **Fulfilled per environment**, the needs doctrine verbatim: forged's
  own sessions on a small instance ("who is this person" answered once,
  platform-wide),
  Authentik at the org, the target's IdP wiring in cloud — same
  declaration, environment decides.
- **Previews are guarded too.** Same enforcement contract, no special
  case: a guarded path is guarded everywhere the app runs.

**Apps that want to know WHO** read the trusted header. Django is the
showcase: stdlib `RemoteUserMiddleware` + `RemoteUserBackend` mint users
from the header — the admin works with forge identities, no passwords
anywhere, ~3 lines of settings. Guidance can teach that exactly; most
frameworks have an equivalent; a static dashboard needs nothing at all.

**The in-app `sso` need remains** for the escalation case: an app whose
own user model must be org identity (per-user data, real login flows).
Guarded paths cover the operational 90% without it.

## Trust boundary

The header is trustworthy only because the app is reachable solely
through its enforcer (the LAN ingress today; the ALB, the injected proxy,
or the gateway on other rails). That must hold structurally on every
target: the app publishes no bypass route, and the enforcer strips
inbound identity headers from clients. This is the one invariant the
whole design leans on — every renderer must establish it, and it gets a
conformance check.

## Consequences

- The stock guidance stance shifts once this ships: "guard /admin in the
  contract" replaces "provision an env superuser" as the default; the
  superuser story remains for apps deployed off-forge.
- The decision card gains nothing new: a guard is contract, so it ships
  through the same review/landing as any other line of app.yaml.
- Depends on signing in people beyond `admin` to be worth much on a
  small instance; valuable at the enterprise from day one.

## Open questions (for review)

- **Schema shape**: `guard:` per part as above, vs a top-level block?
  Per-part reads naturally beside `ports:`/`healthcheck:`.
- **Role vocabulary**: fixed platform roles (`operators`, `members`,
  `anyone-signed-in`) vs project-defined groups? Lean: fixed and tiny
  until the login story exists.
- **Whole-host guards**: `guard: { /: members }` makes an entire app
  private — probably the default for non-public apps; is that a
  separate `private: true` sugar?
- **Header contract**: name and claims (`X-Forge-User`, role list?), and
  whether previews forward the same identity as envs.
