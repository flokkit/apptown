# Scheduled invocation

Triggers are declared in a workflow's `on` configuration. There is no separate
trigger entity with its own editable lifecycle.

## Schedules

The daemon checks selected, pinned project workflows every 30 seconds. It reads
persisted last-fire state, evaluates cron eligibility and applies `skipIfActive`
when requested. It does not backfill every missed execution after downtime.
A firing uses the shared Change/run admission command and records attribution.
The scheduler checks cancellation between duties; it shares the daemon's bounded
shutdown rather than waiting indefinitely for triggered work.

Schedule controls independent of workflow versions and an event-trigger
system remain proposals. The remediation queue is a separate
application operation, not an implementation of a general event bus.

## Implementation references

- [internal/apptown/triggers.go](../../internal/apptown/triggers.go)
- [internal/apptown/changeactions/service.go](../../internal/apptown/changeactions/service.go)
