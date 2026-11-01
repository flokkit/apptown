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
  reporting](../integrations/errors-glitchtip.admin.md) Admin tab.
- **Complaints** collects what got in agents' way.

## Integrations

Each integration's page under **Integrations** has an Admin tab here in the
Docs:

- [Model providers and slots](../integrations/model-pi.admin.md): which models agents use, per job.
- [AI models](../integrations/llm-openrouter.admin.md): model access for the apps you deploy.
- [Package registries and mirrors](../integrations/registry-registry.admin.md), and the container registries releases are published to.
- [Error reporting](../integrations/errors-glitchtip.admin.md).
- [Scanners](../integrations/scanner-tool.admin.md).


## Read status and Attention

The header reports items to fix and sources not known separately. Open Attention
for named links grouped into hosts and deployments, backups, integrations and
app checks. Each unknown reading gives its reason and a next step. Only a failed
status is red; an old usable reading shows when it was last seen.

Infrastructure shows resource presence, health where it applies, and recorded
ownership separately. An overcommit warning states the app limits requested and
the host total. Unreadable memory use names the missing measurement and points
to host diagnostics; it does not establish that the host lacks memory.

Integrations show the last check's age and result. A connection that is unused
and unconfigured says not set up. Ask an administrator to set it up when needed,
or to repair and test a failed connection. Security reports that cannot be read
leave checks incomplete; open a change to re-run the scan, or ask someone with
permission to open one. Parser diagnostics are under Technical details.
