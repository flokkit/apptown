# Application health checks

Appcontract validates per-part readiness and liveness declarations. Deployment
supplies target selection and application context. The Docker library implements
probes and the standing-deployment health controller.

Appcontract also owns how an answer is judged: `Ready` (the declared status
exactly, otherwise below 400) and `SampleReadiness`, one GET that follows no
redirect. Every target and the restore rehearsals use them, so a health path
is ready or not on all of them alike; a target whose probe runs elsewhere
(Portainer's and a sealed rehearsal's in-network fetch) judges the status it
reports with `Ready`.

Readiness asks whether an application can serve. Liveness can trigger a restart.
The schema rejects dependency-checking liveness because a downstream outage must
not cause an application restart loop. It also rejects exec readiness with a
front-door request context. Probe durations require explicit units.

Readiness is not the front page. A readiness path is often exempt from what
refuses visitors (Rails skips host authorization for `/up`), so a gate's boot
(the preview step and the deploy step, `BootRequest.PageCheck`) also opens the
app's root as a visitor does: the visitor's host name and the front-door signal,
same-origin redirects followed. A 400 or 403 (a host allow-list refusing the
visitor's host) or a server error fails the boot in the `page` phase; a 404 is an
API-only app answering, and a 401 or a sign-in redirect is a working page. A
client-only site's page must look like an app shell. Passed or refused, the
check is recorded as `frontPage` in the step's evidence (`BootResult.Page`: the
path that answered, the host it asked as, the status and the verdict), on the
worker as here. The runtime probe `page`
asks the same question of a running deployment through its real front door
(`internal/check/page.go`).

## Execution policy

HTTP readiness and supported liveness probes accept `period`, `timeout`,
`failureThreshold`, `successThreshold` and `checksDeps`. Written periods are
whole seconds from 5 through 300; timeouts from 1 through 10; thresholds from
1 through 50. `checksDeps` is the application's assertion, not proof of a
healthy dependency, and cannot be true for liveness. TCP and exec readiness
remain refused.

Omitted fields retain each target and phase's defaults. Explicit readiness must
fit its consecutive successes within the greater of 90 seconds and
`startupGrace`. A probe's timeout bounds its application requests; surrounding
identity observations have a separate five-second allowance, still bounded by
the operation deadline. Cancellation and late answers never establish health.

Docker executes structured health on its public application. Portainer executes
public HTTP readiness and refuses structured liveness and nonpublic structured
health. App Platform renders HTTP policy for declared-port services and refuses
worker readiness, exec probes and HTTP liveness without a declared port. Static
buckets refuse runtime execution policy. These refusals occur before preparation
or provider effects; accepted syntax does not promise every target can run it.

Cold readiness requires consecutive answers from the same container start or
provider deployment. Maintained readiness keeps separate, derived counters for
the exact generation, artifact, runtime and policy. A miss immediately removes
positive health evidence; failure thresholds do not authorize stale healthy
status. Runtime replacement, unavailable source or process restart resets the
streak. Due times use sample starts, with no overlapping or catch-up probes;
a saturated observer can report stale or unknown rather than invent evidence.
Docker maintenance reads the exact admitted source revision and retained image.
Old recorded provider artifacts keep their existing policy defaults.

## Docker controller

The controller watches settled standing Docker-host deployments. It excludes
previews and deployments with running or uncertain attempts; other providers own
their health control. It detects failed liveness and excessive Docker restarts,
applies thresholds and bounded backoff, and can stop a crash loop.

Before holding a crash loop, the controller witnesses the public container's
exact deployment host label and settled generation. It stops that captured
container ID, so a replacement under the same name is not the target. An
unreadable or mismatched identity is logged and left for a later sweep.

A crash-loop hold receipt records its captured project, deployment, generation
and container identities. When that exact generation has a released record,
the same best-effort transaction appends a timestamped post-release note to its
release log. The original successful release remains successful; its later
crash loop and the confirmed stop (or unconfirmed stop attempt) are separate
facts. The current release permalink continues refreshing that log. A receipt
without a generation witness is never attached to a release by name or timing.

Health-control receipts and post-release notes are best effort. A probe or read failure is not a successful
health observation. Display samples are refreshed by a separate observer and
cannot authorize a restart or settle deployment state.

The controller's public-part lookup reads `app.yaml` at the deployment's
recorded release revision, so its probe and restart policy are the running
release's: changing health settings takes effect with the next release, not
when the change lands. No recorded revision, or a declaration that cannot be
read, uses the legacy default only when no explicit policy has been observed.
Once explicit policy is known, unreadable source suspends its liveness action.

Generic readiness-based traffic draining, replicas and identical probe translation
across all providers are not established by this implementation. See
[deployment runtime](deployment.md) for observation freshness and shutdown.

## Implementation references

- [internal/deploy/appcontract/health.go](../../internal/deploy/appcontract/health.go)
- [internal/deploy/docker/health.go](../../internal/deploy/docker/health.go)
- [internal/apptown/deployment/health.go](../../internal/apptown/deployment/health.go)
- [internal/apptown/deployment/observations.go](../../internal/apptown/deployment/observations.go)
