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
validation and a second current-identity check. Only then is the upstream
called. Built-in bindings get their permitted API dialects from provider defaults;
custom endpoints declare one. The requested operation must use a permitted
dialect, which selects routing and provider authentication. Model definitions
may repeat the binding’s dialect but cannot select a different one. Streaming observes usage and cancellation; the request's telemetry
reports the outcome even on disconnect or provider failure. A bounded watcher
rechecks authority while the stream lives. No provider credential reaches a
harness, client response or telemetry.

`Gateway.Ask` is the one path for AppTown's own model calls (narratives,
approval briefs, pack assistance, checks, what-ifs and the eval
judge; `modelcall.Call`). It resolves the binding the model names, requires the
model to be enabled in it, asks the eval model choice for a project's call (or,
for `Judge`, the judge's door, `ChooseJudge`, instead), takes a concurrency slot
under `forge/<purpose>/<project>`, and writes a pending spend line
(`usage.Requests.OpenCall`) before sending. When the call ends it settles
that line once, on its own context, with the provider's counts and price.
When the provider omits a price, it uses the embedded model catalog generated
from the exact worker Pi pin, or the declared custom endpoint model rates.
Cache reads, writes, long cache writes and context tiers use Pi's rate semantics.
Provider prices, including a reported zero, take precedence. All-zero or absent
model rates are unpriced. Each call records `provider-reported`, `catalog` or
`none`;
unknown spend (no usage, tokens with no price, an unreadable or cut-off answer)
settles as incomplete, and a line never settled counts as unknown once its time
(the call limit plus five minutes) runs out. Callers get the usage back for
their own budgets and never record spend. The caller is AppTown code already
authorized by its route or job, so there is no token and no network check; the
workload is `apptown`, acting as `apptown-system` unless the call names an actor.
The gateway is always constructed; `-inference-url` only opens the listener.

`Catalog.Models` lists only enabled patterns for built-in OpenAI, Anthropic and
OpenRouter providers. An empty enabled list offers and admits nothing, including
the configured default. Custom endpoints with no enabled list use live discovery,
using capability fields where supplied without inferring capabilities from IDs.
The default is not added separately. Stored profile models outside these choices
remain visible with a warning. `Catalog.Discover` asks an unsaved binding’s provider
for its full catalog. Both use the gateway’s provider resolution; saved choices
share its discovery cache.
Discovery asks a provider under that binding's own lock only.

AppTown constructs Catalog before console environments and the gateway after the
console registry. The listener and interrupted-request startup hook remain in
AppTown. Network policy, request deadlines and runtime credential lifetime are
unchanged. This package does not implement hard spending limits.

Built-in provider model definitions in the public view carry the pinned catalog's
costs, so discovery cannot replace Pi's prices with zero placeholders. Cost
model overrides are removed for built-in providers; custom endpoints retain their
declared costs and overrides. Agent and chat spend is recorded only from Pi
transcripts, using `cost.totalFromProvider` to distinguish reported prices from
model calculations. The HTTP proxy never writes another spend line for them.

`make generate-model-catalog` updates `model-catalog.json` from the agent's exact
`PI_VERSION`. It installs the package into a temporary directory and exports the
built-in chat model definitions; the daemon needs no Node runtime. The version
test requires regeneration with every worker Pi update.

`Failure` owns provider failure headlines for gateway responses, AppTown calls
and step outcomes. It names the provider by its connector title and classifies
its upstream HTTP status: refused API key (401), refused model access (403),
rate limit or credit (429), or internal error (5xx). Connection failures say
AppTown could not reach the provider; an empty answer with no reported error
names the model that returned nothing. SDK errors supply their HTTP status when
there is no structured status. Raw provider text belongs in transcript evidence
and logs, never in these headlines.
