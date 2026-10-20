---
name: deploy/health-declared
tags: deploy,health,platform
tier: platform
checks: [{"name": "runtime-contract-present", "expects": "a runnable application intended to be hosted by AppTown commits its current .apptown/app.yaml alongside its source", "severity": "high", "evaluator": "llm", "scope": "tree", "files": ["README.md", "SPEC.md", ".apptown/app.yaml", ".apptown/app.json", ".apptown/compose.yaml", "Dockerfile", "package.json", "pom.xml", "go.mod", "pyproject.toml"], "rubric": "Determine from the specification and implemented application whether this revision is intended to be hosted or previewed by AppTown. A specification-only revision, CLI, or library with no hosted application passes without a runtime contract. A runnable hosted application must commit .apptown/app.yaml in this same revision, describing its actual build or static/image source and required services or initialization. Missing app.yaml is a violation even if app.json, Compose, a Dockerfile, or a later revision could explain how to run it. Do not infer missing runtime requirements or accept a skipped boot gate as successful execution evidence. Cite the implemented app and missing or contradictory declaration. Use AppTown config validation and a cold preview to verify the declaration where execution is available; do not invent a second YAML schema checker. This check is about whether the declaration is committed, not whether the app answers it: do not judge whether a declared path such as /healthz is served, or by what (a static folder serves a public/healthz file as it is; a framework route, a middleware or a server config may answer it). AppTown's live probes check that, and an endpoint you cannot find in the files listed here is not a violation.", "title": "Commit the runtime contract with the application"}, {"name": "readiness-declared", "expects": "every served part the app builds or runs declares a readiness probe (health.readiness.http) in .apptown/app.yaml; a static: part is served by AppTown's own file server and needs none", "severity": "note", "evaluator": "sh", "sh": "f=.apptown/app.yaml; [ -f \"$f\" ] || exit 0; bad=$(awk 'function flush() { if (part != \"\" && served && !static && !ready) printf \"%s \", part; part = \"\" } /^[^[:space:]#]/ { flush(); inapp = ($0 ~ /^app:/); pind = 0; next } inapp && /^[[:space:]]+[A-Za-z0-9_.-]+:/ { match($0, /^[[:space:]]+/); if (pind == 0 || RLENGTH <= pind) { flush(); pind = RLENGTH; part = $1; sub(/:.*/, \"\", part); served = 0; static = 0; ready = 0 } } inapp && part != \"\" { if ($0 ~ /^[[:space:]]+(ports|public):/) served = 1; if ($0 ~ /(^[[:space:]]+|[{,][[:space:]]*)static:/) static = 1; if ($0 ~ /(^[[:space:]]+|[{,][[:space:]]*)readiness:/) ready = 1 } END { flush() }' \"$f\"); [ -z \"$bad\" ] && exit 0; echo \"$f serves ${bad}but declares no readiness probe for it, so AppTown probes GET / — a bootstrap default that can be slow, need auth, or say 200 while the app is unusable. Add under the served part:  health: { readiness: { http: /healthz } }  and give the app a /healthz that answers 200 without touching the database.\"; exit 1", "title": "Declare how to check application readiness"}, {"name": "readiness-redirect-safe", "expects": "the declared readiness path (a static part's root) answers 200 through the front door AND without the front-door signal (no HTTPS redirect on it)", "severity": "medium", "evaluator": "runtime", "probe": "readiness", "title": "Keep readiness checks accessible without redirects"}, {"name": "front-page-answers", "expects": "the app's root, opened through the front door the way a visitor opens it, is not refused: no 400 or 403 (a host allow-list refusing the visitor's host) and no server error. A sign-in prompt, a redirect to sign in, or a 404 from an API-only app with nothing at its root is fine", "severity": "high", "evaluator": "runtime", "probe": "page", "title": "Serve the page a visitor opens"}]
---
Every part AppTown serves from your build or image declares how to
tell it is ready. A `static:` part is the exception: AppTown serves its
folder from its own file server, so there is nothing to declare, and the
runtime check probes the site's root instead. Declaring
nothing still boots — AppTown synthesizes `GET /` for readiness and a TCP
connect for liveness — but `GET /` is a bootstrap default, not a check worth
trusting in production: it can be expensive, sit behind auth, or return a
decorative 200 while the app is actually unusable.

Declare a readiness probe on the served part in `.apptown/app.yaml`:

    app:
      web:
        build: .
        ports: ["8080"]
        public: true
        health:
          readiness: { http: /healthz }

and give the app a `/healthz` that answers 200 quickly and without touching
the database. Liveness (restart) is separate and must never check
dependencies; readiness (routing) may.

AppTown renders that one declaration into the concrete probe for every
environment it runs — cold boot, preview, main, and later a cloud target's
own probe format — so the check that passed in the workshop is the check
production uses.

**The path must be redirect-safe.** Some targets probe without any
front-door header (an ALB target-group check cannot send one). A readiness
path that redirects such a probe to HTTPS looks dead to it. So the declared
path must answer 200 both through the front door and naked, whatever Host
the probe sends; frameworks that force HTTPS or check the host need the
path exempted (Django: answer it in the first middleware; Rails: exclude
`/up` from host authorization). The
runtime check here probes the standing main environment both ways.

**Readiness is not the front page.** A readiness path is often exempt from
what refuses visitors: Rails skips host authorization for `/up`, so an app
whose `config.hosts` never took its hosts from `APPTOWN_ORIGINS` answers `/up`
200 and every real page 403. Gates therefore also open the app's root as a
visitor does, with the visitor's host name and the front-door signal, and
fail a 400 or 403 (Django and Rails refusing a host they were never told) or a
server error there. A 404 at `/` is fine for an API with nothing at its root, as
is a sign-in page or a redirect to one. Trust every origin in `APPTOWN_ORIGINS`.
