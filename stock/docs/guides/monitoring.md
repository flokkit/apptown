# Monitoring and integrations

- **Up or down.** `GET /healthz` answers `ok` (200) while AppTown serves and
  its database answers, and 503 otherwise; it needs no sign-in and says nothing
  else, for a load balancer or a service manager.
- **Usage** shows model tokens and estimated cost, and "Where the time goes" shows time per
  step (also `apptown timing`).
- **Telemetry.** With `-otlp-endpoint` (and `-otlp-protocol http|grpc`) AppTown sends its own traces, metrics and logs to an OpenTelemetry
  receiver. A Grafana dashboard for them is in the repository at
  `stock/observability/apptown-dashboard.json`, with a README on loading it.
- **Errors.** Attach the `apptown` project to the error tracker and AppTown reports its own errors there; see the [error
  reporting](/docs/errors/glitchtip) Admin tab.
- **Complaints** collects what got in agents' way.

## Integrations

Each integration's page under **Integrations** has an Admin tab here in the
Docs:

- [Model providers and slots](/docs/model/pi): which models agents use, per job.
- [AI models](/docs/llm/openrouter): model access for the apps you deploy.
- [Package registries and mirrors](/docs/registry/registry), and the container registries releases are published to.
- [Error reporting](/docs/errors/glitchtip).
- [Scanners](/docs/scanner/tool).
