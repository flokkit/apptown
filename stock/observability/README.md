# AppTown dashboard

Current signal semantics and limitations: [Telemetry](../../design/subsystems/telemetry.md).

`forge-dashboard.json` is a Grafana dashboard for AppTown's own telemetry: what
apptownd exports when `--otlp-endpoint` is set. It is a file, not a feature:
forge does not provision Grafana or manage this dashboard. Load it, change it
in Grafana if you like, export it and commit it back; the commit is the record.

## Loading it

Grafana → Dashboards → New → Import → upload the file, or paste it. It expects
the data sources `otel-lgtm` ships with, `Prometheus` and `Tempo`.
It can also be posted to `/api/dashboards/db` with `{"dashboard": <the file>,
"overwrite": true}`; `id` must be null.

The trace links open Grafana's Explore view with the Tempo data source. They
use the dashboard's `tempo_url` box: leave it empty when the dashboard runs in
the Grafana that holds Tempo, or set it to that Grafana's address.

## What the stack must do

AppTown exports cumulative counters and histograms and seeds nothing at zero.
A series therefore does not exist in Prometheus until its first event, and
Prometheus's `rate()` and `increase()` cannot see the jump from no series to
the first value. Without help, the first step attempt, the first inference
call and the first request on each route after every apptownd restart show in
the raw counters but never on a rate panel.

The remedy is the stack's, not AppTown's: OpenTelemetry marks every cumulative
series with its start time, and Prometheus can write a zero sample at that
time so the first event is a visible increase. The M in `otel-lgtm` is
Prometheus, not Mimir, and the setting is Prometheus's feature flag, passed
on its command line in the stack's own configuration:

    --enable-feature=created-timestamp-zero-ingestion

The OTLP receiver (`--web.enable-otlp-receiver`) must be on as well, which
`otel-lgtm` already does. The maintainers' stack runs Prometheus 3.14 with both
set, read back from its status API on 18 September 2026. On a new stack,
enable the flag and verify it by restarting apptownd, running one step, and
checking that the "How attempts ended" panel shows the attempt. AppTown used
to seed one counter's closed label space at zero as a stopgap; that covered
nothing the flag does not, and was removed on 18 September 2026 once the flag
was on.

## What the panels mean

- **Inference.** Provider-reported token usage, in and out, by model. Cache/input
  semantics differ between provider APIs; the gateway currently preserves those
  counts. Cache categories are shown separately and must not be assumed additive. These are tokens, not money; no cost is exported.
- **Steps.** Elapsed time from the initial harness start to attempt settlement,
  including review wrap-up and settlement work;
  how every attempt ended, including the ones that never ran a harness;
  and how long attempts waited for a model slot. `rework` is the reviewer
  sending a change round again. `settlement_error` and `abandoned` should be
  zero.
- **API.** Requests, server errors and p95 latency by route. The agent's
  check call runs the project's tests inside one request and takes tens of
  seconds by design; it has its own panel so it does not bury the pages.
- **Jobs.** Background work by operation, from the job spans in Tempo.
- **Follow a run.** Type a run id at the top and the text panel links to its
  traces: a `workflow.step` trace per attempt with `queued` and `running`
  children, and `inference.request` spans linked to the attempt that made
  them.

The variable called `instance` filters the OpenTelemetry `job` label, normally
`forge/apptownd`. It distinguishes jobs, not multiple AppTown installations with the
same namespace/service name. Tempo job panels only filter service name. Do not
use this dashboard as proof of per-instance isolation until unique instance
attributes are mapped by the receiver and applied consistently to its queries.
