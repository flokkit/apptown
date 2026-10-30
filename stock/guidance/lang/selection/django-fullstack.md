---
name: lang/selection/django-fullstack
tags: lang,python,django
checks: [{"name": "admin-ships-whole", "expects": "a routed Django admin has collectstatic in the image build (an unstyled admin is a broken build)", "severity": "medium", "evaluator": "sh", "sh": "[ -n \"$(find . \\( -name node_modules -o -name vendor -o -name .git \\) -prune -o -type f -name urls.py -print 2>/dev/null | head -1)\" ] || { w=\"part $APPTOWN_PARTS_HERE is\"; case \"$APPTOWN_PARTS_HERE\" in *\" \"*) w=\"parts $APPTOWN_PARTS_HERE are\";; esac; d=$APPTOWN_FOLDER; [ \"$d\" = . ] && d=\"the repository root\"; echo \"$w built with python (tools: in .apptown/app.yaml) and there is no Django urls.py in $d: add the app, or correct the part's tools\"; exit 1; }; [ -n \"$(find . \\( -name node_modules -o -name vendor -o -name .git \\) -prune -o -type f -name urls.py -exec grep -l admin.site.urls {} + 2>/dev/null)\" ] || exit 0; [ -f Dockerfile ] || { w=\"part $APPTOWN_PARTS_HERE is\"; case \"$APPTOWN_PARTS_HERE\" in *\" \"*) w=\"parts $APPTOWN_PARTS_HERE are\";; esac; d=$APPTOWN_FOLDER; [ \"$d\" = . ] && d=\"the repository root\"; echo \"$w built with python (tools: in .apptown/app.yaml) and there is no Dockerfile in $d to build its image: add it, or correct the part's tools\"; exit 1; }; if ! grep -q collectstatic Dockerfile; then echo 'urls.py routes the Django admin but the Dockerfile never runs collectstatic — the deployed admin renders unstyled (broken-looking login page). Add: RUN DJANGO_SECRET_KEY=collectstatic-at-build-only .venv/bin/python manage.py collectstatic --noinput (a throwaway key for this command only; serve statics via whitenoise).'; exit 1; fi", "title": "Include the Django admin’s required assets", "appliesTo": ["tool:python"]}, {"name": "admin-exposed-deliberately", "expects": "shipping the Django admin is a decision: working (statics + a provisioned superuser) and acknowledged, or not routed at all", "severity": "medium", "evaluator": "llm", "scope": "tree", "files": ["**/urls.py", "Dockerfile*", "**/settings.py", "*.sh", "**/entrypoint*", "compose*.y*ml", "docker-compose*.y*ml", "app.yaml", "Procfile", "secretspec.toml"], "rubric": "Decide whether urls.py routes django.contrib.admin (admin.site.urls). If it does NOT, pass: the app has chosen not to ship the admin. If it does, shipping it must be deliberate and whole: (1) the Dockerfile runs collectstatic (with whitenoise or equivalent serving statics) so the admin is not an unstyled login page, AND (2) something provisions a superuser from the environment — createsuperuser --noinput or an equivalent driven by DJANGO_SUPERUSER_* variables in a start script, entrypoint, init step, or compose/app.yaml — never hardcoded credentials in code, AND (3) every variable the provisioning reads is actually supplied: DJANGO_SUPERUSER_PASSWORD declared in secretspec.toml (generate = true, or required) so an environment has a value, and DJANGO_SUPERUSER_USERNAME (and EMAIL if read) set in the start script, entrypoint, or app.yaml environment; a provisioning script guarded by 'if the variables are set' with one of them never set provisions nobody. A routed admin missing any of the three is a violation: an admin nobody can log into, or that renders broken, is attack surface pretending to be a feature. Cite the file and line for what you found.", "title": "Make Django admin access an explicit choice", "appliesTo": ["tool:python"]}, {"name": "trusts-front-door", "expects": "settings trust AppTown's front door: ALLOWED_HOSTS and CSRF_TRUSTED_ORIGINS are built from APPTOWN_ORIGINS, wildcard origins translated to Django's leading-dot form, ALLOWED_HOSTS never a bare [\"*\"] and never falling back to one when APPTOWN_ORIGINS is unset", "severity": "high", "evaluator": "sh", "sh": "f=$(ls */settings.py settings.py 2>/dev/null | head -1); [ -n \"$f\" ] || { w=\"part $APPTOWN_PARTS_HERE is\"; case \"$APPTOWN_PARTS_HERE\" in *\" \"*) w=\"parts $APPTOWN_PARTS_HERE are\";; esac; d=$APPTOWN_FOLDER; [ \"$d\" = . ] && d=\"the repository root\"; echo \"$w built with python (tools: in .apptown/app.yaml) and there is no Django settings.py in $d or a folder just below it: this way of working's apps are Django (uv run django-admin startproject app .); add it, or correct the part's tools\"; exit 1; }; if ! grep -q APPTOWN_ORIGINS \"$f\"; then echo \"$f never reads APPTOWN_ORIGINS — behind AppTown's HTTPS front door Django refuses every form post with CSRF verification failed. Build ALLOWED_HOSTS and CSRF_TRUSTED_ORIGINS from APPTOWN_ORIGINS and set SECURE_PROXY_SSL_HEADER = ('HTTP_X_FORWARDED_PROTO','https') and USE_X_FORWARDED_HOST = True, and list app.health.healthz FIRST in MIDDLEWARE so the health probe, which asks by the app's address, still gets 200 (lang/selection/django-fullstack shows the lines; the seed ships app/health.py).\"; exit 1; fi; if grep -Eq \"^ALLOWED_HOSTS[[:space:]]*=[[:space:]]*\\[[[:space:]]*['\\\"]\\*['\\\"][[:space:]]*\\]\" \"$f\"; then echo \"$f sets ALLOWED_HOSTS = ['*'], so any Host header is trusted. Build ALLOWED_HOSTS from APPTOWN_ORIGINS: each origin's host, with a leading *. written as . (Django's subdomain form).\"; exit 1; fi; if grep -Eq \"^[^#]*or[[:space:]]*\\[[[:space:]]*['\\\"]\\*['\\\"][[:space:]]*\\]\" \"$f\"; then echo \"$f falls back to ALLOWED_HOSTS = ['*'] when APPTOWN_ORIGINS is unset, so any Host header is trusted there. Fall back to [\\\"localhost\\\", \\\"127.0.0.1\\\"] (lang/selection/django-fullstack shows the lines).\"; exit 1; fi; if ! grep -Eq '\\*\\\\?\\.' \"$f\"; then echo \"$f reads APPTOWN_ORIGINS but never translates its wildcard origins (https://*.preview.<domain>): ALLOWED_HOSTS needs .preview.<domain>, or the front door's requests are refused with Bad Request (400). Write each origin's host with a leading *. as ., and keep app.health.healthz FIRST in MIDDLEWARE so the health probe by address still gets 200 (lang/selection/django-fullstack shows the lines).\"; exit 1; fi", "title": "Configure the application to trust AppTown’s front door", "appliesTo": ["tool:python"]}, {"name": "django-check-deploy", "expects": "Django's own deployment-readiness checks pass (manage.py check --deploy)", "severity": "high", "evaluator": "tool", "tool": "django-check-deploy", "title": "Resolve Django deployment-check findings", "appliesTo": ["tool:python"]}, {"name": "python-sast", "expects": "no high-confidence security findings from semgrep's django + python rules", "severity": "medium", "evaluator": "tool", "tool": "semgrep-django", "title": "Resolve Python security-check findings", "appliesTo": ["tool:python"]}, {"name": "gitignore-names-build-output", "expects": "git would ignore what Python writes (__pycache__/), so build output can never be committed by accident. If the project commits that output deliberately, this check does not fit it: say so and stop rather than add the line, since an ignore rule over tracked files makes the repository contradict itself", "severity": "medium", "evaluator": "sh", "sh": "t=$(mktemp -d) && git -c init.defaultBranch=main init -q \"$t\" || exit 2; find . \\( -name node_modules -o -name vendor -o -name .git \\) -prune -o -type f \\( -name pyproject.toml -o -name 'requirements*.txt' \\) -print > \"$t/m\" || exit 2; awk -v dir=1 'BEGIN { base = ENVIRON[\"APPTOWN_FOLDER\"]; n = split(ENVIRON[\"APPTOWN_NOT_APP_CODE\"], x, \"\\n\") } { f = $0; sub(/^\\.\\//, \"\", f); if (base != \".\" && base != \"\") f = base \"/\" f; for (i = 1; i <= n; i++) if (x[i] != \"\" && index(f, x[i] \"/\") == 1) next; if (dir) { if (!sub(/\\/[^\\/]*$/, \"\", f)) f = \"\" } print f }' \"$t/m\" | sort -u > \"$t/d\" || exit 2; [ -s \"$t/d\" ] || { w=\"part $APPTOWN_PARTS_HERE is\"; case \"$APPTOWN_PARTS_HERE\" in *\" \"*) w=\"parts $APPTOWN_PARTS_HERE are\";; esac; d=$APPTOWN_FOLDER; [ \"$d\" = . ] && d=\"the repository root\"; echo \"$w built with python (tools: in .apptown/app.yaml) and there is no pyproject.toml or requirements.txt in $d or below it: add it, or correct the part's tools\"; exit 1; }; bad=0; while IFS= read -r r; do for p in __pycache__/probe.cpython-312.pyc; do q=${r:+$r/}$p; git -C \"$APPTOWN_ROOT\" --git-dir=\"$t/.git\" --work-tree=\"$APPTOWN_ROOT\" -c core.excludesFile=/dev/null check-ignore -q \"$q\"; s=$?; [ $s -eq 0 ] && continue; [ $s -eq 1 ] || exit 2; echo \".gitignore does not cover ${q%/*}/ (Python writes it)\"; bad=1; done; done < \"$t/d\"; exit $bad", "title": "Declare generated files in the repository’s ignore rules", "appliesTo": ["tool:python"]}, {"name": "lockfile-in-step", "expects": "pyproject.toml and uv.lock agree (uv lock --check succeeds) and the lockfile is committed, so an install resolves exactly what is committed", "severity": "medium", "evaluator": "tool", "tool": "uv-lock-check", "title": "Keep the dependency lockfile in sync", "appliesTo": ["tool:python"]}]
---
The application is Django: the ORM and migrations, the framework's forms and
auth, Postgres underneath; its pages come from the project's UI pack. The pack seeds the Dockerfile, ignore rules, contract,
`secretspec.toml`, `app/deploy_settings.py` and `app/health.py` with its test;
the app comes from uv and Django's own generator. A project adopted without
those seeded files writes the same ones.

**The first act on an empty repo:**

    uv init --bare --name app
    uv add django gunicorn whitenoise psycopg
    uv run django-admin startproject app .

Commit `pyproject.toml` and `uv.lock` (the image installs from the lockfile).
Plain `psycopg`, never `psycopg[binary]` (its bundled libraries fail the image
scan). Then: end `app/settings.py` with
`from app.deploy_settings import *  # noqa: E402,F401,F403`; add the front-door
settings below with `app.health.healthz` first in `MIDDLEWARE`; whitenoise's
middleware right after `SecurityMiddleware` and
`STATIC_ROOT = BASE_DIR / "staticfiles"`; one test. `make test` runs
`uv run python manage.py test`: keep it running the whole suite. The
starter's Makefile ships its `make test` target commented out until the app
exists; the change that writes the app uncomments it (the landing's tests
step fails for a part built with python while there is no test command). The image
migrates at start and serves with gunicorn (`runserver` is development only);
both stages are `python:3.12-slim`, never the full `python` image. The seeded
`.apptown/app.yaml` declares the web part, `/healthz` and the Postgres need.

**Delete what `startproject` wrote that the deploy settings replace:** the
literal `SECRET_KEY`, `DEBUG = True`, and any console mail backend (configure
no mailer, or a real one whose host and credentials come from the
environment). The deploy
settings read the database from the injected `PG*` variables (never a literal
host, never parse `DATABASE_URL`), DEBUG from `DJANGO_DEBUG=1` only, and turn on
the HTTPS, HSTS and secure-cookie settings `manage.py check --deploy
--fail-level WARNING` requires; the landing runs that check and fails on any
warning. Never silence a check to pass it; say in the report why one does not
fit.

**The signing key comes from the environment, with no fallback:**
`SECRET_KEY = os.environ["DJANGO_SECRET_KEY"]`, declared in `secretspec.toml`
with `generate = true`. Never `os.environ.get("DJANGO_SECRET_KEY", "dev-key")`.
Only the image build's `collectstatic` line gets a throwaway
(`DJANGO_SECRET_KEY=collectstatic-at-build-only` on that `RUN` line, never as
`ENV`); the tests get a generated key from AppTown and the seeded Makefile.

**The admin is for operators, and a decision:**

- Not asked for: delete `path("admin/", admin.site.urls)` and its import.
- Asked for: ship it whole. `collectstatic` in the image with whitenoise
  serving it (else the admin renders unstyled); a superuser created at startup
  with `createsuperuser --noinput`, skipped when it exists, from
  `DJANGO_SUPERUSER_USERNAME/EMAIL` in the start command and a generated
  `DJANGO_SUPERUSER_PASSWORD`:

        [profiles.default]
        DJANGO_SECRET_KEY = { description = "Django signing key", generate = true }
        DJANGO_SUPERUSER_PASSWORD = { description = "Django admin password", generate = true }

  Say in the report where the admin is, the username, and that the password is the
  environment's generated `DJANGO_SUPERUSER_PASSWORD` (an administrator reveals
  it under Settings › Secrets).

**Trust the front door.** Build hosts and origins from `APPTOWN_ORIGINS`
(origins per tier, wildcards included), or every form post fails CSRF and every
request 400s:

    from urllib.parse import urlsplit

    APPTOWN_ORIGINS = [o for o in os.environ.get("APPTOWN_ORIGINS", "").split(",") if o]
    # Django spells a wildcard subdomain as a leading dot: *.x.example -> .x.example
    ALLOWED_HOSTS = [
        "." + h[2:] if h.startswith("*.") else h
        for h in (urlsplit(o).hostname for o in APPTOWN_ORIGINS)
    ] or ["localhost", "127.0.0.1"]  # no APPTOWN_ORIGINS: this machine only
    CSRF_TRUSTED_ORIGINS = APPTOWN_ORIGINS  # Django reads https://*.x.example itself
    SECURE_PROXY_SSL_HEADER = ("HTTP_X_FORWARDED_PROTO", "https")
    USE_X_FORWARDED_HOST = True
    MIDDLEWARE = [
        "app.health.healthz",  # FIRST: answers /healthz before host checks
        "django.middleware.security.SecurityMiddleware",
        "whitenoise.middleware.WhiteNoiseMiddleware",
        # ... the rest of startproject's list
    ]

The fallback is exactly `["localhost", "127.0.0.1"]`: never `["*"]`, never
empty, never a typed hostname. Keep `app/health.py` first (it answers
`/healthz` by address before host checks and redirects) and its test; never
widen `ALLOWED_HOSTS` to let a probe in.

Run `apptown check` before handing back: `check --deploy` gates the landing,
semgrep's findings are advisory. `.gitignore` names `__pycache__/`; `uv.lock`
agrees with `pyproject.toml`.

**Memory:** one gunicorn worker is about 80 MB; declare
`resources: { memory: 160Mi }` and 80 MB more per extra worker.
