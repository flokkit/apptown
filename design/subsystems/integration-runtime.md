# Integrations and runtime credentials

This guide explains how configuration reaches applications and agents. The
[integration package](../../internal/apptown/integration/doc.go) owns bindings,
attachments and service-kind rendering; the [secret package](../../internal/apptown/secret/doc.go)
owns stored values, application declarations and provider resolution.

GitLab API requests, including registry provisioning, refuse redirects so the
provider token stays at the configured endpoint. Configure the final GitLab URL;
even a redirect within the same origin is refused.

GitLab is “not set up” when both `GITLAB_URL` and `GITLAB_TOKEN` are empty.
If only one is set, its health check fails and names the missing setting.
Integrations that are not set up have no check time and do not count as connected
or needing attention.

## From configuration to runtime

1. An administrator configures a provider binding for a capability. The project
   attaches a binding and resource; an available instance binding alone does not
   satisfy a project's required capabilities.
2. Service-kind YAML describes fields, environment templates, steering and guidance.
   Direct resources use those fields. Provisioning linked resources requires a
   compiled adapter; declaring YAML does not install one.
3. Some attachments need repository changes. `changeactions.OpenWiring` opens
   the wiring Change in one transaction with its attachment link; see
   [Wiring an attachment](#wiring-an-attachment) below.
4. Deployment resolves application secrets from the selected source declaration
   and project/deployment bindings. Integration renders service contributions.
   The runtime receives the values selected for that consumer. File-based secret
   delivery is not supported; see the Secret package for exact declaration rules.
5. Error reporting's variables come from its stock definition
   (`stock/dependencies/errors.yaml`, `register.inject`): today `SENTRY_DSN`
   alone, rendered with the attached DSN (framework settings such as Spring's
   resolver order belong to that framework's pack).
   Changing what apps are given is a data change there, not engine code; the
   browser is given only the entries that carry the DSN, in its public form.
6. **A template that does not resolve is not given.** When a service kind's
   template still has an unresolved placeholder (a resource not provisioned yet,
   a secret not set), the variable is left out of a server or agent environment
   and the log names the project, connector and variable. A template that
   resolves is given as rendered, including a field or literal that is blank.
   A variable's presence does not prove the service is ready. A definition,
   attachment or secret store that cannot be read is an error that stops the
   boot or step, for a server's environment and a browser's values alike,
   never a variable left out or an empty set.

## Wiring an attachment

`POST /api/v1/projects/{slug}/integrations/{capability}/wire` calls one owner
command, `changeactions.OpenWiring`. It is the only caller of the link.

- **One transaction.** Work admits the Change (in workflow mode, also its first
  `adopt-integration` Run). Inside the same transaction, an admission participant
  (`workflow.AdmissionParticipant`) calls `integration.LinkWireIn`, which locks the
  attachment row and records the Change on it, and then writes the
  `integration.wire` receipt. Nothing is committed, prepared or dispatched until
  all of that succeeds. A failure at any point leaves no Change, no Run, no link
  and no receipt. The HTTP answer is the error, 422 unless the owner classified it.
- **No duplicate work.** While the attachment's linked Change is live (anything
  but abandoned), a new wiring request is refused with a conflict (409) that
  names that Change (`integration.WireOpenError`). A retry after a lost response
  therefore finds the work it already opened. Only an abandoned wiring, or none,
  may be replaced. The row lock serialises concurrent requests.
- **After commit.** Workspace preparation and dispatch are git and process
  effects, so they run after commit and are never undone by a rollback. The
  Change stays linked whatever follows. In workflow mode a failed preparation
  abandons the Change and Run, and a new wiring may start; a failed dispatch
  leaves the Run ready and in progress for inspection or retry, and a new wiring
  is refused. In chat mode a failed workspace preparation leaves the Change in
  progress and linked; the error names it.

`internal/apptown/changeactions/wiring_test.go` injects failures at the lock, the
link and the receipt, and after commit at preparation and dispatch, and checks
the persisted records through the owners' reads.

## AI models keys

The `llm` connector (provider `openrouter`) holds a reference
to an OpenRouter provisioning key and the limits (`maxLimit` required,
`defaultLimit`, `previewLimit`). `Bindings.Save` refuses a reference starting
`STEP_` (those reach steps and workers), and every use checks again. Its
attachment is only the project's choice of connector. `RegisterLLMKey`,
`ReleaseLLMKey` and `LLMAllowance` are the adapter Deployment calls: the
first reads the attachment strictly before any OpenRouter call, makes or
keeps an owner's key (limit and reset sent together on every boot), and
stores its value in the built-in store under the owner's scope. AI models keys
never appear in `Environment`, browser values or step environments; they reach
an app only through its deployment's or preview's boot
([deployment.md](deployment.md#requesting-and-executing-a-release)).

## Values a browser may be given

A static part is served as files; it cannot read its container's environment.
Values an attached integration gives an app therefore reach a static page
another way:

- **Marked at the source.** Only values safe for any visitor to read are given
  to browsers: the error-reporting DSN in its public form (a password typed in
  with a DSN is removed; the public key stays; servers still get the DSN as
  stored), the feedback URL and key, and service-kind `env` entries marked
  `browser: true`. A definition that marks a value drawn from a secret
  (`{{secret:…}}`) is refused by `ServiceKinds.Save`, whoever saves it.
- **Read strictly.** `integration.BrowserEnvironment` is the whole set. An
  unreadable attachment, or a stored definition that no longer passes the rules,
  is an error naming it, never an empty set; the static part's boot fails.
- **Served by AppTown, set by AppTown alone.** Every static server (preview,
  local deployment, release image, generated compose) runs
  `docker.StaticServerCommand`: the site from `/srv`, `/_apptown/release`
  answering `APPTOWN_RELEASE` (`<part>@<revision>`, what App Platform's readiness witnesses), and
  `/_apptown/env.js` answering `window.__APPTOWN_ENV__ = Object.freeze(Object.assign(new Object(), …))`
  from `APPTOWN_BROWSER_ENV` at request time; unset, an empty object. A static
  site's image carries that command itself (`docker.StaticSiteDockerfile`),
  so no target adds one; previews and generated
  compose mount the files into the stock file server and give it the command. Only static
  parts are given it (a part packed into the file server keeps `ServesStatic`),
  and it is set after the contract's env and secrets. A contract or
  `secretspec.toml` that names `APPTOWN_BROWSER_ENV` is refused. `Host.Boot`
  reads the values before needs, init containers or other parts change, and
  hands that value to the public container, so a failed read leaves the whole
  running stack as it was. It fails in its own boot phase, `config`, which the
  landing and the Docker driver settle as a known failure (as they do an
  unknown need or a missing secret), so a fixed setting can be deployed again. An earlier release's preview image is the file
  server and keeps `ServesStatic`, so it is given its values too. A Docker host and a Portainer stack deliver them (the stack sets
  `APPTOWN_BROWSER_ENV` on the public service, last); DigitalOcean deploys do
  not, so a deploy there of a static site whose page is given values is
  refused before any effect, naming the values.
- **Checked where it runs, against trusted facts.** The platform guidance block
  `deploy/browser-values` declares the runtime check `browser-env`. Whether it
  applies comes from the contract the target runs: the change's tree at the
  gate, the recorded release revision for a standing deployment (unreadable or
  unrecorded is unknown, never the working tree). A server app, or a static site
  given nothing, is not judged. Otherwise three facts are compared, in order:
  1. What the page must be given: the integration settings now.
  2. What the container was started with: its `APPTOWN_BROWSER_ENV`, read by
     `docker.InspectBrowserValues`, which reads that one variable and the
     container's id and nothing else of its environment. It is read before and
     after the probe; a container replaced in between is unknown.
  3. What `/_apptown/env.js` serves.

  Any difference, or any of them unreadable, is `cannot-assess` with AppTown
  at fault: settings changed since the start ("start it again") or delivery
  broken. Then the page's HTML must follow the loader convention: the first
  script a browser would run (data blocks and `<template>` contents do not; a
  missing `type` falls back to `language`, as browsers do) is in `<head>` with
  `src` exactly `/_apptown/env.js` and no other attribute except a CSP `nonce`,
  and the page has no `<base href>`. The
  convention is lint over markup: a pass says the values are served and the
  markup loads them first, not that a browser ran them or that reports arrive.
- **Acted on by the guidance step.** A failed convention is a finding and gates
  at `failOn` like `sh` and `tool` findings. A required runtime check (at or
  above `failOn`) that was probed and could not be judged holds the step as an
  error for a person, never rework; an exception does not waive an unknown.
  The same fault attribution reaches explicit acceptance, which errors instead
  of reworking. A run that has already booted its preview (a preview step of
  the run passed) requires it: a preview that cannot be found holds the same
  way. A run that has booted none, because its workflow has none (the landing)
  or boots it in a later stage, records the check unjudged and does not hold. `adopt-integration` runs the step on its preview.

## Credential delivery

A step declaring `scm: write` gets a GitLab project access token at Developer
level. On GitLab a Developer may push to a branch no rule protects, every rule
matching a branch applies (the most permissive wins), and merging a merge
request is a permission apart from pushing. So AppTown mints a write token
only when at least one protection rule matches the default branch (by name or
wildcard) and, in every matching rule, every push and merge entry is a role of
Maintainer or higher, or No one; an entry naming a user, group or deploy key is
refused rather than judged, and so is a project with 100 or more protection
rules (AppTown reads one page). Otherwise the step is told what to change, and
nothing is minted.
The check runs at mint: a token minted under protection lives at most 72 hours
if the protection is removed afterwards (accepted).

| Consumer | Delivery and owner |
| --- | --- |
| Application part or init step | Secret prepares declared values by consumer; Integration renders attached service environment values. Plaintext is accessible in the receiving container. |
| Workflow step | Secret selects explicit `STEP_` values; execution owners can add scoped SCM, registry and integration grants. |
| Tests step | Secret gives the app's `generate = true` secrets a throwaway value minted per run and its declared defaults; no deployed value or other credential. The evidence names what was and was not provided. |
| Chat | Conversation and Agentenv prepare the session's permitted grants. A chat does not automatically receive every step or application secret. Private Docker does not grant registry push authority. |
| Model inference | The runtime presents an AppTown token to Inference. Provider-model keys remain in the gateway's private provider resolution. |

The [runtime environment guide](../../stock/docs/guides/runtime-environments.md)
owns the detailed grant and persistence tables. [Inference security](inference-security.md)
owns gateway credential lifetime, network checks and admission limits. A binding
or a rendered contribution does not independently authorize an operation.

## Failure and storage limits

Application Infisical bindings resolve against that store alone. Daemon/runtime
lookup instead overlays local `secrets.env` values on Infisical's. Both read
strictly: a `secrets.env` that cannot be read, or a configured Infisical that
cannot be, fails the read with its reason, never fewer values. Infisical's
values are read at most once a minute; a refresh that fails is a failed read
and never answers the values read before, which only masking still uses. So
while Infisical is unreachable every runtime secret read fails, names that
`secrets.env` would supply included: deploys, steps given step secrets, target
connections and GitLab calls stop with "Infisical could not be read" until it
answers.

What an app's containers are given from its attached integrations is read the
same way. An attachment, service definition, connector configuration, project
or secret store that cannot be read fails the deploy, preview or step that
asked, with the reason; it never starts without those variables. Only absence
gives nothing: no attachment, an error-reporting definition that injects
nothing, or a template whose value cannot be produced yet (a secret name the
store does not hold), which the log names. Registry credentials follow the same
rule: a registry attachment or credential that cannot be read, or a login that
fails, stops the pull; it is never retried with another credential or
anonymously. No registry attached reads public images anonymously.

An administrator sets or removes one of AppTown's own credentials (the secret
a target's `tokenFrom` or `keyFrom` names) with `PUT`/`DELETE /api/v1/secrets/{name}`,
`apptown secret set <NAME>` (the value on standard input) or Integrations → Secrets,
and from the target wizard beside the field that names it. The value goes to
`secrets.env` and is never answered back; each change leaves a
`secret.instance-set` or `secret.instance-removed` receipt with the actor and
name. A name Infisical holds is refused, because a `secrets.env` line would
silently win over it; when Infisical is configured but unreadable (a failed
refresh included, whatever it last cached) the write is refused as unknown. The
names list behind these doors does not fall back as runtime lookup does: an
unreadable `secrets.env` or Infisical answers unavailable, and the pages show
unknown rather than "not held". Values are one line: an SSH key's line breaks are written
as `\n`, which the Docker-host connection unfolds.

The built-in store keeps obfuscated values; it is not a vault. Its values are
encrypted with a key held in the same database, so anyone with a dump or backup
of the database can read them. Preserve the original key when restoring values.
Key rotation and further cloud secret stores are not built. The
[threat register](../architecture/threat-register.md) tracks the broader
security work.
