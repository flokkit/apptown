---
name: deploy/memory-declared
tags: deploy,resources,memory,platform
tier: platform
checks: [{"name": "memory-declared", "expects": "each part AppTown runs from a build or an image declares its memory (resources.memory) in .apptown/app.yaml; a static part is served by AppTown's own file server and declares nothing", "severity": "medium", "evaluator": "runtime", "probe": "memory-declared", "title": "Declare the memory each part may use"}, {"name": "memory-within", "expects": "each part of the standing main deployment uses under 90% of the memory limit AppTown applied to it, as AppTown observes it; a part running with no limit does not meet this, and use AppTown cannot observe is not judged", "severity": "high", "evaluator": "runtime", "probe": "memory-within", "title": "Run within the declared memory"}]
---
Every part AppTown runs from your build or image declares the most memory it
may use (a `static:` part declares nothing). A part that declares nothing runs
with no limit and can take the whole host.

    app:
      web:
        build: .
        resources: { memory: 160Mi }
    profiles:
      production:
        web: { resources: { memory: 1Gi } }
      preview:
        web: { resources: { memory: 128Mi } }

**Choosing the number.** Take what the part uses once settled (the
deployment's card shows it), measured over normal requests and maintenance
jobs, add about half, and round up to 32Mi, 48Mi, 64Mi,
96Mi, 128Mi, 160Mi, 192Mi, 256Mi, 320Mi, 384Mi, 512Mi or 1Gi. At 90% of its
limit or more, declare more: a part over its limit is stopped, not slowed.

**Make the runtime fit the limit:**

- JVM (Java, Kotlin): 320Mi, started with `-XX:MaxRAMPercentage=30
  -XX:+UseSerialGC -XX:TieredStopAtLevel=1 -XX:ReservedCodeCacheSize=32m
  -Xss512k` and `MALLOC_ARENA_MAX=2`; on Spring Boot also
  `spring.threads.virtual.enabled=true`. Left to itself the heap grows into the
  room the rest needs and the app is stopped under load. Declare more only
  when the card shows the app needs it, and say why in `app.yaml`.
- .NET: follows the limit itself; 128Mi for a small ASP.NET app. Set
  `DOTNET_GCHeapHardLimitPercent` only when the app needs another heap share.
- Gunicorn: about 80 MB a worker; 160Mi for one, 80 MB more per extra
  `WEB_CONCURRENCY` worker.
- Puma: about 120 MB in single mode; 256Mi, more with workers.
- Node: set `NODE_OPTIONS=--max-old-space-size=` about three quarters of the
  limit in MB (96 for 128Mi).
- Go, Rust: 48Mi. A Caddy file server: 32Mi.

A deployment-owned need takes an offered `size` from the catalogue
(`GET /api/v1/dependencies`); only MariaDB `small` (128Mi) ships now. Never
invent a size name, and never combine `size` with explicit need resources,
inherited profile values included. A changed database command on real or unknown
data needs reviewed maintenance before it deploys.
