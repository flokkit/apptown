# Telemetry

## Enablement and lifetime

`apptownd --otlp-endpoint host:port` (or `APPTOWN_OTLP_ENDPOINT`) enables exports.
`--otlp-protocol` / `APPTOWN_OTLP_PROTOCOL` selects HTTP (default) or gRPC for all
three signals. A bare endpoint or `http://` uses cleartext; `https://` enables
TLS. Use a receiver address, not a Grafana UI URL. An empty endpoint installs
no-op instruments and creates no exporters. The daemon configuration does not
expose the library's optional exporter headers.

Resources carry `service.name=apptownd`, `service.namespace=forge`, the build
revision, and `forge.instance` when PublicHost is configured. Resource attributes
are not automatically metric labels: the receiver's configuration determines
that mapping.

The daemon installs the standard-log bridge once. Shutdown first drains HTTP,
jobs and execution owners within a shared five-second budget, then closes the
providers with the remainder. The provider's own bound does not extend the daemon
budget. A timed-out drain may leave work unfinished and telemetry unflushed.
Telemetry does not change console retention or claim that external processes
have stopped.

## What is exported

| Signal | Meaning and limitations |
| --- | --- |
| HTTP span and `apptown_http_request_duration` | API, MCP, Git and OAuth paths after authentication reaches the span wrapper. Route-pattern name, method, status and caller/project attributes where available. SPA and earlier middleware refusals are not covered. The span also carries the actual URL path. |
| `inference.request` | An admitted inference call, provider/model, workload scope, outcome and observed token counts. Authentication/provider refusals before admission do not create this span. No body or credential is deliberately attached. |
| `apptown_inference_requests_total`, `apptown_inference_tokens_total`, `apptown_inference_request_duration` | Admitted calls, provider-reported token categories and elapsed duration. Tokens are usage, not money. Missing usage does not establish zero consumption. Cache/input accounting remains provider-shaped; do not sum overlapping categories. |
| `workflow.step` with queued/running children | Agent and review attempts, including preflight failures. These are independent root traces, not all workflow step kinds. Harness outcome and step settlement outcome are separate. |
| `apptown_step_attempts_total` | One sample per observed attempt settlement, including attempts that never started a harness. Labels are step type and outcome. |
| `apptown_step_duration` | Time from the initial harness start to attempt settlement; includes later review wrap-up and settlement work. It is not solely model latency. Only attempts whose harness started contribute. |
| `apptown_step_queued` | Measured slot wait, with step-type label and a zero bucket. Preflight failures before slot acquisition do not contribute. |
| `job.<operation>` | Each River execution attempt, with operation, attempt, job ID and key; no dedicated job counter/histogram yet. |
| Standard logs | Complete lines are exported alongside existing output. Severity is inferred from text. The standard logger has no context, so these records have no request trace ID. |

Histograms use milliseconds as their unit and explicit boundaries appropriate to
HTTP, inference, step duration and queue wait. The exported instrument names above
have no `_ms` suffix; the receiver may add a unit suffix when translating them.
Run, step and attempt IDs are span attributes, not metric labels. Metrics are
aggregates; they do not reconstruct a particular run's sequence.

## Following work

The executor publishes a claimed attempt's span context in an in-memory registry
keyed by run, step and attempt, and removes it at settlement. The gateway resolves
the authenticated workload and adds a link to that context when it is available.
This does not require exposing a provider credential or trusting a client-supplied
trace identifier to establish workload authority.

Admission-request to run and enqueue-request to job links are not implemented.
There is no single request-to-settlement trace and no change-ID attribute. A run
search finds related attempts through `forge.run`. Ended attempts are exported;
this is not a replacement for live progress or durable workflow receipts.

## Dashboard and operator instructions

The checked-in [dashboard and import guide](../../stock/observability/README.md)
own installation, panel queries and receiver requirements. Forge does not provision
Grafana or the collection stack. Counter start-time handling is a receiver concern;
check the guide before interpreting first-event rate panels.

The dashboard currently uses the metric `job` label as its variable named
`instance`. Forge's default `forge/apptownd` job is shared across installations with
the same service/namespace, so this is not deployment isolation. Job-span panels
filter service name and do not use that variable. Unique resource identity and
receiver/query mapping need a follow-up before using this dashboard to distinguish
multiple Forge installations. This limitation does not alter access policy.

## Limits and verification

Admission-to-run and enqueue-to-job correlation, change IDs, health/deployment
spans and monetary cost are not implemented, and the dashboard does not filter
by instance when several forges report to one backend.

Provider, gateway, observer and wiring tests cover export behaviour and settlement
failure. Daemon tests cover flush ordering and shutdown bounds. Existing canary
tests protect specific surfaces; arbitrary log text can still contain sensitive
information. Keep prompts, credentials and transcripts out of telemetry.

## Implementation references

- [internal/telemetry/telemetry.go](../../internal/telemetry/telemetry.go)
- [internal/forge/inference/gateway.go](../../internal/forge/inference/gateway.go)
- [internal/forge/jobs/telemetry.go](../../internal/forge/jobs/telemetry.go)
- [cmd/apptownd/runtime.go](../../cmd/apptownd/runtime.go)
- [stock/observability/README.md](../../stock/observability/README.md)
