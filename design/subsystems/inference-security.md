# Inference access and provider credentials

## The boundary

AppTown holds the provider credentials. Pi, its tools, installed packages and
worker/scout delegates receive `APPTOWN_SESSION_TOKEN`. Generated `models.json`
contains public model metadata, an AppTown proxy URL and
`"apiKey": "$APPTOWN_SESSION_TOKEN"`. The dollar prefix tells the Pi adapter to use the environment value.
There is no fallback that injects provider credentials when proxy access fails.

Inference requires **both** a live runtime credential and a connection from
an explicitly allowed worker network. The inference handler has its own
listener and is absent from the public web handler and the console API handler.
It accepts console and step credentials. Browser sessions, personal access
tokens and the mere ability to reach the listener do not grant inference.

Source checks use `RemoteAddr`, the actual TCP peer. `X-Forwarded-For`,
`X-Real-IP`, actor headers and cookies confer no authority. There are no
implicit loopback, private-address or trusted-proxy exceptions. Missing or
invalid configuration fails closed; wildcard `/0` networks are rejected.

The network restriction limits use of a stolen token outside the worker
boundary. It does **not** prove who requested a completion. A compromised
allowed runtime can spend with its own token or relay requests for someone
else. NAT can also collapse several workloads into one visible peer. Attribution
means “this credential's workload”, not “the named human intended these words”.

## Credential lifetime and attribution

- A chat credential lasts with the persisted chat. Idle time, container
  recreation and daemon restart do not expire it. Ending/removing the chat or
  disabling its known owner account prevents new requests.
- A step credential is minted for its project, run, step and attempt, and revoked
  at completion. Work supplies one scoped observation of current run state,
  the live step attempt and the recorded inference owner. Admission rejects a
  changed project, inactive run or stale attempt; AppTown separately checks the
  owner's account and the trusted network. Tokens live in daemon memory, so an
  interrupted step cannot reuse one after restart.
- A continued chat receives its own chat identity and token, retaining the
  originating run, producing step and attempt as attribution. Its inference
  does not depend on the old run remaining active.
- Delegates share their parent runtime's credential, attribution and concurrency
  allowance. They receive no separate provider key.

New runs persist their initiating actor before dispatch. Child and automatic
landing runs inherit the actor. Scheduled/internal work is attributed to
`system`; historical runs without reliable attribution use `run/<id>`. Existing
open-mode and decorated audit identities are not a cryptographic claim of a
human initiator. Authenticated deployment is required for meaningful human
attribution. Account disable is checked at admission; runtime API access does not recheck project membership on every inference request.

An active stream rechecks the credential every two seconds, cancels upstream
when the client disconnects, and has a twenty-minute request deadline. A long
chat can make subsequent requests indefinitely while it remains authorized.
Revocation and request cancellation do not prove the provider stopped billing
work it had already accepted. Shutdown keeps the daemon's existing shared,
bounded deadline; inference introduces no unbounded shutdown wait.

## Provider operations and limits

Only administrator-configured bindings choose upstream URLs and credentials.
The proxy admits native OpenAI Chat Completions, OpenAI Responses, Anthropic
Messages and Google generate-content requests. It does not expose uploads,
files, model administration, batch APIs, arbitrary paths or redirects. It
rejects query-string credentials, fallback model lists and background requests.
Mixed API dialects need separate bindings. Upstream auth headers are rebuilt
server-side; runtime cookies, auth, actor and forwarding headers are discarded.
Error bodies and redirect locations are not reflected because providers may
echo credentials in them. Clients receive the upstream error status.

A binding's nonempty curated model list is an admission allowlist. Otherwise
its declared models and configured default `*` pattern are allowed. Custom
OpenAI-compatible endpoints retain cached live model discovery, with declared
metadata taking precedence. A list that cannot be read adds nothing for that
minute: no earlier answer is reused, the model picker names the binding as not
fully listed, and a model the binding does not declare is refused as unknown
(the list could not be read), not as disabled. Pi's fuzzy `~` selectors are not authorization
patterns: curate concrete IDs for those bindings. Built-in metadata remains in
Pi; supplying an override does not itself authorize all built-in models.

Limits are 32 MiB per request, eight concurrent requests per runtime
token, and 64 per daemon. This is **not a monetary budget** or a sustained rate
limit. New sessions can consume additional capacity. Hard user/project/instance
budgets, transactional reservations across replicas and provider-side billing
backstops remain follow-up work under TM-02. Within the configured model set, an authorized runtime can make arbitrary inference requests.

## What is recorded

A workload's request through the gateway is not stored per request. Its
telemetry (a span and the `apptown_inference_*` metrics) carries provider,
model, workload kind, project, run and step, the upstream status, the outcome
and the provider-reported token counts observed on the response transport. A
workload's spend is recorded by its owner from the harness's own usage report
(usage's ledger). AppTown's own calls write a pending spend line before
dispatch and settle it from the answer; failure to write it denies dispatch.
The gateway's telemetry and spend lines do not record prompts, completions,
provider keys or AppTown tokens.

Usage can be missing or partial after rejection, cancellation or provider
failure; that is unknown consumption, not free inference. Native input/cache
counts retain provider semantics; they are not a normalized bill. A restart
locks in AppTown's own calls still pending as unknown spend without inventing
final usage.

The existing Pi-derived usage dashboard remains a separate, advisory accounting
surface. Do not add these two ledgers together: they can describe the same
request. Neither ledger provides authoritative monetary settlement.

## Runtime and deployment contract

Model agents run in containers: on the worker's Docker for agent steps and
worker-placed change chats when a worker is configured, and on the AppTown host's
Docker otherwise, including project chats, step continuations and chats placed
on AppTown (none of which exist under `-no-local-docker`). A local process
can read the daemon's credential files/environment even if its own environment
is filtered. AppTown therefore refuses local model execution instead of presenting
it as isolated. On a worker, agents reach this listener across the network at
`-inference-url`; provider credentials never leave AppTown host. The
shared-host/privileged-DinD escape risk remains TM-03; this proxy is not a
boundary against a hostile tenant that can compromise the host it runs on.

Configure all three settings (equivalent environment variables are supported):

```text
-inference-addr <private-bind-address>:7779       APPTOWN_INFERENCE_ADDR
-inference-url http://<worker-reachable-host>:7779 APPTOWN_INFERENCE_URL
-inference-networks <actual-worker-source-CIDRs>  APPTOWN_INFERENCE_NETWORKS
```

Do not route this listener through public ingress or a generic shared reverse
proxy. Choose the narrow addresses actually observed from both ordinary worker
containers and console DinD networks; with a separate worker machine, those are
the addresses its containers arrive from as AppTown host sees them
([workers](../operations/workers.md#running-apptown-without-a-local-docker-daemon)). Confirm the host firewall and Docker
routing in the deployment; do not infer protection from an RFC1918 address or
an application proxy's access rules. Use a protected transport/network between
hosts; the listener does not add TLS itself.

Startup locks in AppTown's own calls still pending. A restart does not scrub
credential copies made elsewhere.

Consoles recreate their generated homes and drop old Pi auth files when
provisioned. Their root mount is read-only so tools cannot rewrite the persisted
actor/token for the next restart. Only Pi home, sessions, scratch, uploads and
Docker client state are writable. Raw upload reads are confined to the upload
directory, preventing symlinks into sibling credentials. Known provider credentials reintroduced through explicit step
secrets or integration environment settings cause runtime preparation to fail.
Model-config API reads redact inline keys and custom headers too.

Deleting a file or container cannot invalidate a copy of a key already taken.
If an upstream provider key has ever been delivered into a workspace or
transcript, rotate it and review retained transcripts and backups with a secret
scanner.

Gateway tests cover native routes, credential and network checks, error
containment, cancellation and revocation. Budget enforcement and host isolation remain open in the
[threat register](../architecture/threat-register.md).

## Implementation references

- [internal/apptown/inference/gateway.go](../../internal/apptown/inference/gateway.go)
- [internal/apptown/inference/providers.go](../../internal/apptown/inference/providers.go)
- [internal/apptown/identity/runtime_tokens.go](../../internal/apptown/identity/runtime_tokens.go)
- [internal/apptown/usage/inference.go](../../internal/apptown/usage/inference.go)
- [internal/apptown/agentharness/pi/models.go](../../internal/apptown/agentharness/pi/models.go)
- [internal/apptown/inference/gateway_test.go](../../internal/apptown/inference/gateway_test.go)
