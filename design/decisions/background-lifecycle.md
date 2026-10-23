# Background work ownership, and why not DSKit

AppTown keeps its own lifecycle convention for background work. It does not add a
service manager such as DSKit beside it.

## The convention

Workflow, remediation and narratives each close admission, signal cancellation
and expose completion. App signals all three before waiting; the daemon caps
the whole shutdown at five seconds, drains HTTP and recurring tasks, and flushes
telemetry last. A timeout bounds the wait, not the underlying work. Console
containers and saved sessions survive daemon shutdown. Tests drain the same
owners before removing their temporary resources.

Narratives count each detached refresh (`sync.WaitGroup.Go`), and the owner's
gate refuses new work once draining begins. A failed refresh keeps the previous
summary and does not stop the service. No service manager can infer ownership
of goroutines launched behind its back.

## Why not DSKit

`github.com/grafana/dskit/services` can signal every service and wait with a
bound, but its listeners only observe state; they do not stop siblings. AppTown
would still have to distinguish a fatal producer failure from an optional
refresh failure, and adopting it for narratives alone would keep the daemon's
coordinator and App's sequencing while adding an adapter and several module
upgrades. It would remove neither the narrative admission gate nor its
accounting.

## When to reconsider

A replacement must cover the daemon's producers and all three App owners
together, remove the parallel lifecycle machinery, and use the same teardown in
tests. It must keep the single deadline, listener startup rollback, named
unfinished work, failure isolation, telemetry ordering and surviving console
containers. Compare the whole diff, adapters, tests and module changes
included, and adopt it only for a clear reduction in maintenance.
