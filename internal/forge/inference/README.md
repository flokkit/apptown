# Inference

`Identity.Authenticate` resolves admitted console or step credentials and checks
current account/lifecycle authority. Console identity survives an idle stop and
reconnection; a completed step attempt does not grant another model call.

`Catalog` owns private provider resolution and the public model-ID discovery
cache. Its provider records never leave the package. `PublicProviders` is the credential boundary — a harness-neutral view with no credential field, checked against every private binding before release. Rendering that view into a particular harness's file format is the composition root's job (`inference_wiring.go`); this package names no harness.
The view carries proxy URLs and allowed models only; the session-token reference
and file layout are the renderer's, and nothing here supplies a workflow settlement
capability. `CheckEnvironment` rejects known provider credential aliases on
runtime environment delivery surfaces.

`New` binds Identity, Catalog and Usage.Requests into the dedicated HTTP gateway.
A request passes network and credential checks, capacity admission, provider/model
validation and a second current-identity check before its durable usage admission.
Only then is the upstream called. Streaming observes usage and cancellation;
completion records the outcome even on disconnect or provider failure. A bounded
watcher rechecks authority while the stream lives. No provider credential reaches
a harness, client response or receipt.

`Gateway.Complete` is the same admission for a trusted caller inside the
forge (workflow drafting): one tool-free, non-streaming call in the binding's
own dialect, as the workload its token names. It skips only the network check.

`Gateway.Ask` is the one path for AppTown's own model calls (narratives,
approval briefs, simple changes, pack assistance, checks, what-ifs and the eval
judge; `modelcall.Call`). It resolves the binding the model names, requires the
model to be enabled in it, asks the eval model choice for a project's call (or,
for `Judge`, the judge's door, `ChooseJudge`, instead), takes a concurrency slot
under `forge/<purpose>/<project>`, and writes the receipt and a pending spend
line (`usage.Requests.OpenCall`) before sending. When the call ends it settles
that line once, on its own context, with the provider's counts and price;
unknown spend (no usage, tokens with no price, an unreadable or cut-off answer)
settles as incomplete, and a line never settled counts as unknown once its time
(the call limit plus five minutes) runs out. Callers get the usage back for
their own budgets and never record spend. The caller is AppTown code already
authorized by its route or job, so there is no token and no network check; the
workload is `apptown`, acting as `apptown-system` unless the call names an actor.
The gateway is always constructed; `-inference-url` only opens the listener.

`Catalog.Models` lists the patterns a slot may name (default, curated and
discovered), and `Catalog.Discover` asks an unsaved binding's provider, both
through the same resolution and discovery cache the gateway admits by.
Discovery asks a provider under that binding's own lock only.

Forge constructs Catalog before console environments and the gateway after the
console registry. The listener and interrupted-request startup hook remain in
Forge. Network policy, request deadlines and runtime credential lifetime are
unchanged. This package does not implement hard spending limits.
