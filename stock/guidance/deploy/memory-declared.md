---
name: deploy/memory-declared
tags: deploy,resources,memory,platform
tier: platform
checks: [{"name": "memory-declared", "expects": "each part AppTown runs from a build or an image declares its memory (resources.memory) in .apptown/app.yaml; a static part is served by AppTown's own file server and declares nothing", "severity": "medium", "evaluator": "runtime", "probe": "memory-declared", "title": "Declare the memory each part may use"}, {"name": "memory-within", "expects": "each part of the standing main deployment uses under 90% of the memory limit AppTown applied to it, as AppTown observes it; a part running with no limit does not meet this, and use AppTown cannot observe is not judged", "severity": "high", "evaluator": "runtime", "probe": "memory-within", "title": "Run within the declared memory"}]
---
Every part AppTown runs from your build or image declares the most memory
it may use. A `static:` part is the exception: AppTown serves its folder
from its own file server, so there is nothing to declare.

    app:
      web:
        build: .
        ports: ["8080"]
        public: true
        resources: { memory: 160Mi }

A part that declares nothing runs with **no limit**: one leak or one busy
hour can take the whole host's memory, and with it every other app on it.
AppTown holds each part to what it declares (a Docker host or Portainer
exactly; App Platform on the smallest size that covers it). Production and
previews can differ through profiles:

    profiles:
      production:
        web: { resources: { memory: 1Gi } }
      preview:
        web: { resources: { memory: 128Mi } }

**Choosing the number.** Measure, then declare tight: take what the part
uses when it has settled (the deployment's card shows it), add about half
again, and round up to 32Mi, 48Mi, 64Mi, 96Mi, 128Mi, 160Mi, 192Mi, 256Mi,
320Mi, 384Mi, 512Mi or 1Gi. A starting number is a guess until the card shows
the app under it. When the card or a review shows a part at 90% of its limit or more,
declare more rather than leave it at the edge; a part over its limit is
stopped by the kernel, not slowed down.

**Make the runtime fit the limit.** Most runtimes size themselves from the
memory they can see. Inside a limit, each wants telling or already knows:

- **JVM (Java, Kotlin, Spring Boot):** it sizes its heap from the memory it
  can see, which with no limit is the whole host's, and keeps what it takes.
  Much of a small Spring Boot service's memory is not heap. Measured on two
  small Spring Boot + Hibernate + Flyway services at a 256Mi limit through a
  burst of requests, they held 210–225 MB with the settings below, about
  150 MB of it outside the heap (class metadata, code, stacks, native
  allocations). Declare 320Mi, for headroom over that, and start the JVM with
  `-XX:MaxRAMPercentage=30 -XX:+UseSerialGC -XX:TieredStopAtLevel=1
  -XX:ReservedCodeCacheSize=32m -Xss512k` and `MALLOC_ARENA_MAX=2`. The
  percentage asks for a smaller heap (the JVM's own floor still allows about
  126 MB at these limits); the serial collector has no collector threads; the
  quick compiler alone needs less code cache; smaller thread stacks save
  memory per thread; and capping malloc arenas at two reduces native memory
  overhead. With a 75% heap share the heap grew into the room the rest needs,
  and the kernel stopped an app under load.
  Turn on virtual threads (`spring.threads.virtual.enabled=true`) so request
  threads cost little. Two further options are opt-in, never defaults: the
  JDK's ahead-of-time cache (`-XX:AOTCache`, JDK 25) for faster starts, and a
  GraalVM native image for the smallest footprint at the cost of a slower,
  stricter build. Declare more only when the card shows the app needs it, and
  say why in the app.yaml.
- **.NET:** follows the container's limit by itself (the garbage collector
  keeps the heap to about three quarters of it). A small ASP.NET app uses
  about 64 MB; 128Mi holds it. Set `DOTNET_GCHeapHardLimitPercent` only when
  the app needs another share.
- **Gunicorn (Django, Flask):** one worker unless told otherwise; a Django
  worker uses about 80 MB, so 160Mi holds one. Each extra worker
  (`WEB_CONCURRENCY`) needs about 80 MB more.
- **Puma (Rails):** single mode unless `WEB_CONCURRENCY` asks for workers,
  each about 120 MB. Single mode uses about 120 MB; 256Mi holds it.
- **Node:** the heap does not follow the limit on every version. Set
  `NODE_OPTIONS=--max-old-space-size=<about three quarters of the limit, in
  MB>`, for example 96 for 128Mi.
- **Go and Rust:** nothing to set. A small service uses 5–15 MB; 48Mi holds
  it. A Caddy file server uses about 11 MB; 32Mi holds it.

Two checks hold this. The first reads the contract as AppTown parses it,
at every landing and in every review. The second is judged in reviews on the
standing deployment: each part's use, as AppTown observes it, against the
limit AppTown applied. Where the target does not report use (App
Platform, Portainer), it is not judged and says so.

For a deployment-owned need, inspect `GET /api/v1/dependencies` for catalogue
`sizes`. Select an offered `needs.<kind>.size` or a profile's need `size`; the
catalogue supplies matched memory and startup settings. Only MariaDB `small`
(128Mi) ships now, for local containers. Do not invent names or assume PostgreSQL,
managed instances or shared databases support them. Do not combine a selected
size and explicit need resources, including inherited profile values. An operator
cap override is separately recorded and does not retune the database. A changed
command on real/unknown data requires reviewed maintenance before deployment;
AppTown refuses it before resizing. Measure normal requests and maintenance jobs.
