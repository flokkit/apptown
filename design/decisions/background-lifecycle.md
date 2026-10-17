# Background work ownership and DSKit

Decision: 18 September 2026. Retain Forge's existing lifecycle convention for
this fix. Do not add DSKit beside it. Reconsider adoption when a replacement
can demonstrate less coordination code and one lifecycle convention.

## What the fix needs

Workflow, remediation and narratives close admission, signal cancellation and
expose completion. App signals all three before waiting; the daemon caps the
whole shutdown at five seconds, drains HTTP and recurring tasks, and flushes
telemetry last. A timeout bounds waiting, not the underlying work. Console
containers and saved sessions survive daemon shutdown. Tests drain the same
owners before removing their temporary resources.

Narratives additionally need to count each detached refresh and synchronize
admission with stopping. `sync.WaitGroup.Go` handles counting; the owner's gate
prevents admitting work after draining begins. Individual refresh errors retain
the previous summary and do not terminate the service. No service manager can
infer ownership of arbitrary goroutines launched behind its back.

## Evaluation

Reviewed `github.com/grafana/dskit/services` at
`v0.0.0-20260917084233-c4a11ed0bf44`. Its `Manager.StopAsync` signals every service
and `AwaitStopped(ctx)` supports bounded waiting. The manager's installed
listeners observe state and notify failures; they do not automatically stop
siblings. Forge would still distinguish fatal producer failure from an optional
refresh failure, and retain the latter inside the narrative service.

This is a credible replacement candidate, but adopting it only for narratives
would retain the daemon coordinator and App sequencing while adding an adapter.
It would not remove the narrative admission gate or accounting. No replacement
prototype was built, so no net deletion is claimed. The existing daemon runtime
is 178 lines; App shutdown is 39 after this fix. That is the coordination code to
compare against, not every line in an execution owner's runtime.

Dependency measurement used temporary copies of Forge's `go.mod` and `go.sum`
and `go get github.com/grafana/dskit/services@<version>`; the repository's module
files were untouched. It added three requirements (DSKit, `pkg/errors`,
`uber/atomic`), upgraded seven existing requirements, and added 20 checksum lines.
The services package's non-standard package dependencies were only `pkg/errors`
and `uber/atomic`. The larger module nevertheless raised selected versions of
crypto, net, sync, text, gRPC and two genproto modules. These are pre-tidy `go get`
measurements against this checkout, not a claim that every DSKit package would
be linked into forged.

## Reconsideration criteria

A replacement must cover the daemon producers and all three App owners together,
remove the parallel lifecycle machinery, and use the same teardown in tests.
It must preserve the single deadline, listener startup rollback, named unfinished
work, failure isolation, telemetry ordering and surviving console containers.
Application-specific dependency ordering can remain explicit. Compare the full
diff, including adapters, tests and module changes; accept only a clear reduction
in maintenance burden. This decision does not block the narrative flake fix.

Sources: [DSKit service API](https://pkg.go.dev/github.com/grafana/dskit/services),
[reviewed manager implementation](https://github.com/grafana/dskit/blob/c4a11ed0bf44/services/manager.go).
