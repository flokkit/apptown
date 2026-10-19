# Scheduled and webhook invocation

Triggers are declared in a workflow's `on` configuration. There is no separate
trigger entity with its own editable lifecycle.

## Schedules

The daemon checks selected, pinned project workflows every 30 seconds. It reads
persisted last-fire state, evaluates cron eligibility and applies `skipIfActive`
when requested. It does not backfill every missed execution after downtime.
A firing uses the shared Change/run admission command and records attribution.
The scheduler checks cancellation between duties; it shares the daemon's bounded
shutdown rather than waiting indefinitely for triggered work.

## Webhooks

`POST /api/v1/hooks/{slug}/{workflow}` requires a selected workflow declaring a
webhook and the configured `FORGE_WEBHOOK_TOKEN`. The handler accepts that token
only in the `X-Forge-Token` header, compared in constant time; a token in the URL
is refused, since proxies and shells record URLs. Missing token configuration
disables webhooks. The credential is instance-wide, not independently revocable
per workflow: anyone holding it can fire any workflow that declares `on: webhook`,
in any project (accepted for 1.0).

The handler takes a bounded payload, derives a title/body and invokes the same
application command used by other starts. Authentication is distinct from the
browser-origin checks on requests arriving through a browser.

Per-trigger credentials, schedule controls independent of workflow versions and
an event-trigger system remain proposals. The remediation queue is a separate
application operation, not an implementation of a general event bus.

## Implementation references

- [internal/forge/triggers.go](../../internal/forge/triggers.go)
- [internal/forge/httpapi/webhook.go](../../internal/forge/httpapi/webhook.go)
- [internal/forge/changeactions/service.go](../../internal/forge/changeactions/service.go)
