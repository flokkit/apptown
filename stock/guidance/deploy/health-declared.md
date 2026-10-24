---
name: deploy/health-declared
tags: deploy,health,platform
tier: platform
checks: [{"name": "runtime-contract-present", "expects": "a runnable application intended to be hosted by AppTown commits its current .apptown/app.yaml alongside its source", "severity": "high", "evaluator": "llm", "scope": "tree", "files": ["README.md", "SPEC.md", ".apptown/app.yaml", ".apptown/app.json", ".apptown/compose.yaml", "Dockerfile", "package.json", "pom.xml", "go.mod", "pyproject.toml"], "rubric": "Determine from the specification and implemented application whether this revision is intended to be hosted or previewed by AppTown. A specification-only revision, CLI, or library with no hosted application passes without a runtime contract. A runnable hosted application must commit .apptown/app.yaml in this same revision, describing its actual build or static/image source and required services or initialization. Missing app.yaml is a violation even if app.json, Compose, a Dockerfile, or a later revision could explain how to run it. Do not infer missing runtime requirements or accept a skipped boot gate as successful execution evidence. Cite the implemented app and missing or contradictory declaration. Use AppTown config validation and a cold preview to verify the declaration where execution is available; do not invent a second YAML schema checker. This check is about whether the declaration is committed, not whether the app answers it: do not judge whether a declared path such as /healthz is served, or by what (a static folder serves a public/healthz file as it is; a framework route, a middleware or a server config may answer it). AppTown's live probes check that, and an endpoint you cannot find in the files listed here is not a violation.", "title": "Commit the runtime contract with the application", "appliesTo": ["repository"]}, {"name": "readiness-declared", "expects": "every served part the app runs as a container (one with ports: or public: true) declares a readiness probe (health.readiness.http) in .apptown/app.yaml; a static: part is served by AppTown's own file server and needs none", "severity": "note", "evaluator": "sh", "sh": "f=\"$APPTOWN_ROOT/.apptown/app.yaml\"; [ -f \"$f\" ] || { echo 'there is no .apptown/app.yaml at the repository root, though the check judges its parts'; exit 1; }; bad=$(awk -v here=\" $APPTOWN_PARTS_HERE \" 'function flush() { if (part != \"\" && index(here, \" \" part \" \") && served && !static && !ready) printf \"%s \", part; part = \"\" } /^[^[:space:]#]/ { flush(); inapp = ($0 ~ /^app:/); pind = 0; next } inapp && /^[[:space:]]+[A-Za-z0-9_.-]+:/ { match($0, /^[[:space:]]+/); if (pind == 0 || RLENGTH <= pind) { flush(); pind = RLENGTH; part = $1; sub(/:.*/, \"\", part); served = 0; static = 0; ready = 0 } } inapp && part != \"\" { if ($0 ~ /^[[:space:]]+(ports|public):/) served = 1; if ($0 ~ /(^[[:space:]]+|[{,][[:space:]]*)static:/) static = 1; if ($0 ~ /(^[[:space:]]+|[{,][[:space:]]*)readiness:/) ready = 1 } END { flush() }' \"$f\"); [ -z \"$bad\" ] && exit 0; echo \".apptown/app.yaml serves ${bad}but declares no readiness probe for it, so AppTown probes GET / — a bootstrap default that can be slow, need auth, or say 200 while the app is unusable. Add under the served part:  health: { readiness: { http: /healthz } }  and give the app a /healthz that answers 200 without touching the database.\"; exit 1", "title": "Declare how to check application readiness", "appliesTo": ["container"]}, {"name": "readiness-redirect-safe", "expects": "the declared readiness path (a static part's root) answers 200 through the front door AND without the front-door signal (no HTTPS redirect on it)", "severity": "medium", "evaluator": "runtime", "probe": "readiness", "title": "Keep readiness checks accessible without redirects", "appliesTo": ["part"]}, {"name": "front-page-answers", "expects": "the app's root, opened through the front door the way a visitor opens it, is not refused: no 400 or 403 (a host allow-list refusing the visitor's host) and no server error. A sign-in prompt, a redirect to sign in, or a 404 from an API-only app with nothing at its root is fine", "severity": "high", "evaluator": "runtime", "probe": "page", "title": "Serve the page a visitor opens", "appliesTo": ["part"]}]
---
Every part AppTown serves from your build or image declares its readiness in
`.apptown/app.yaml` (a `static:` part declares nothing):

    app:
      web:
        build: .
        ports: ["8080"]
        public: true
        health:
          readiness: { http: /healthz }

- `/healthz` answers 200 quickly and never touches the database. Liveness
  (restart) never checks dependencies; readiness (routing) may.
- The path answers 200 both through the front door and by the app's own
  address with no forwarded headers and any Host: exempt it from HTTPS
  redirects and host checks (Django: answer it in the first middleware;
  Rails: exclude `/up` from host authorization).
- Readiness is not the front page. Gates also open the root as a visitor
  does and fail a 400 or 403 (a host the app was never told) or a 5xx. Trust
  every origin in `APPTOWN_ORIGINS`. A 404 at `/` is fine for an API, as is a
  sign-in page or a redirect to one.
