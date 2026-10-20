---
name: lang/selection/django-fullstack
tags: lang,python,django
checks: [{"name": "admin-ships-whole", "expects": "a routed Django admin has collectstatic in the image build (an unstyled admin is a broken build)", "severity": "medium", "evaluator": "sh", "sh": "if grep -rq \"admin.site.urls\" --include='urls.py' . 2>/dev/null && [ -f Dockerfile ] && ! grep -q collectstatic Dockerfile; then echo 'urls.py routes the Django admin but the Dockerfile never runs collectstatic — the deployed admin renders unstyled (broken-looking login page). Add: RUN DJANGO_SECRET_KEY=collectstatic-at-build-only .venv/bin/python manage.py collectstatic --noinput (a throwaway key for this command only; serve statics via whitenoise).'; exit 1; fi", "title": "Include the Django admin’s required assets"}, {"name": "admin-exposed-deliberately", "expects": "shipping the Django admin is a decision: working (statics + a provisioned superuser) and acknowledged, or not routed at all", "severity": "medium", "evaluator": "llm", "scope": "tree", "files": ["**/urls.py", "Dockerfile*", "**/settings.py", "*.sh", "**/entrypoint*", "compose*.y*ml", "docker-compose*.y*ml", "app.yaml", "Procfile", "secretspec.toml"], "rubric": "Decide whether urls.py routes django.contrib.admin (admin.site.urls). If it does NOT, pass: the app has chosen not to ship the admin. If it does, shipping it must be deliberate and whole: (1) the Dockerfile runs collectstatic (with whitenoise or equivalent serving statics) so the admin is not an unstyled login page, AND (2) something provisions a superuser from the environment — createsuperuser --noinput or an equivalent driven by DJANGO_SUPERUSER_* variables in a start script, entrypoint, init step, or compose/app.yaml — never hardcoded credentials in code, AND (3) every variable the provisioning reads is actually supplied: DJANGO_SUPERUSER_PASSWORD declared in secretspec.toml (generate = true, or required) so an environment has a value, and DJANGO_SUPERUSER_USERNAME (and EMAIL if read) set in the start script, entrypoint, or app.yaml environment; a provisioning script guarded by 'if the variables are set' with one of them never set provisions nobody. A routed admin missing any of the three is a violation: an admin nobody can log into, or that renders broken, is attack surface pretending to be a feature. Cite the file and line for what you found.", "title": "Make Django admin access an explicit choice"}, {"name": "trusts-front-door", "expects": "settings trust AppTown's front door: ALLOWED_HOSTS and CSRF_TRUSTED_ORIGINS are built from APPTOWN_ORIGINS, wildcard origins translated to Django's leading-dot form, ALLOWED_HOSTS never a bare [\"*\"] and never falling back to one when APPTOWN_ORIGINS is unset", "severity": "high", "evaluator": "sh", "sh": "f=$(ls */settings.py settings.py 2>/dev/null | head -1); [ -n \"$f\" ] || exit 0; if ! grep -q APPTOWN_ORIGINS \"$f\"; then echo \"$f never reads APPTOWN_ORIGINS — behind AppTown's HTTPS front door Django refuses every form post with CSRF verification failed. Build ALLOWED_HOSTS and CSRF_TRUSTED_ORIGINS from APPTOWN_ORIGINS and set SECURE_PROXY_SSL_HEADER = ('HTTP_X_FORWARDED_PROTO','https') and USE_X_FORWARDED_HOST = True, and list app.health.healthz FIRST in MIDDLEWARE so the health probe, which asks by the app's address, still gets 200 (lang/selection/django-fullstack shows the lines; the seed ships app/health.py).\"; exit 1; fi; if grep -Eq \"^ALLOWED_HOSTS[[:space:]]*=[[:space:]]*\\[[[:space:]]*['\\\"]\\*['\\\"][[:space:]]*\\]\" \"$f\"; then echo \"$f sets ALLOWED_HOSTS = ['*'], so any Host header is trusted. Build ALLOWED_HOSTS from APPTOWN_ORIGINS: each origin's host, with a leading *. written as . (Django's subdomain form).\"; exit 1; fi; if grep -Eq \"^[^#]*or[[:space:]]*\\[[[:space:]]*['\\\"]\\*['\\\"][[:space:]]*\\]\" \"$f\"; then echo \"$f falls back to ALLOWED_HOSTS = ['*'] when APPTOWN_ORIGINS is unset, so any Host header is trusted there. Fall back to [\\\"localhost\\\", \\\"127.0.0.1\\\"] (lang/selection/django-fullstack shows the lines).\"; exit 1; fi; if ! grep -Eq '\\*\\\\?\\.' \"$f\"; then echo \"$f reads APPTOWN_ORIGINS but never translates its wildcard origins (https://*.preview.<domain>): ALLOWED_HOSTS needs .preview.<domain>, or the front door's requests are refused with Bad Request (400). Write each origin's host with a leading *. as ., and keep app.health.healthz FIRST in MIDDLEWARE so the health probe by address still gets 200 (lang/selection/django-fullstack shows the lines).\"; exit 1; fi", "title": "Configure the application to trust AppTown’s front door"}, {"name": "django-check-deploy", "expects": "Django's own deployment-readiness checks pass (manage.py check --deploy)", "severity": "high", "evaluator": "tool", "tool": "django-check-deploy", "title": "Resolve Django deployment-check findings"}, {"name": "python-sast", "expects": "no high-confidence security findings from semgrep's django + python rules", "severity": "medium", "evaluator": "tool", "tool": "semgrep-django", "title": "Resolve Python security-check findings"}, {"name": "gitignore-names-build-output", "expects": "git would ignore what Python writes (__pycache__/), so build output can never be committed by accident. If the project commits that output deliberately, this check does not fit it: say so and stop rather than add the line, since an ignore rule over tracked files makes the repository contradict itself", "severity": "medium", "evaluator": "sh", "sh": "t=$(mktemp -d) && git -c init.defaultBranch=main init -q \"$t\" || exit 2; bad=0; for p in __pycache__/probe.cpython-312.pyc; do git --git-dir=\"$t/.git\" --work-tree=. -c core.excludesFile=/dev/null check-ignore -q \"$p\" || { echo \".gitignore does not cover ${p%/*}/\"; bad=1; }; done; exit $bad", "title": "Declare generated files in the repository’s ignore rules"}, {"name": "lockfile-in-step", "expects": "pyproject.toml and uv.lock agree (uv lock --check succeeds) and the lockfile is committed, so an install resolves exactly what is committed", "severity": "medium", "evaluator": "tool", "tool": "uv-lock-check", "title": "Keep the dependency lockfile in sync"}]
---
The application is Django — batteries included. Server-rendered templates,
Django ORM + migrations, the admin for back-office CRUD for free, Postgres underneath,
forms and auth from the framework. Sprinkle interactivity with HTMX, never a SPA.

The pack seeds the packaging recipe, the ignore rules, the runtime
contract, the secret declaration (`secretspec.toml`), the deploy
settings (`app/deploy_settings.py`) and the health answer with its
test (`app/health.py`, `app/test_health.py`); the app comes from uv and Django's
own generator, never from a hand-kept skeleton.

**The first act on an empty repo** (the "build the first version" change):

    uv init --bare --name app
    uv add django gunicorn whitenoise psycopg
    uv run django-admin startproject app .

That writes `pyproject.toml` and `uv.lock` (commit both: the Dockerfile
installs from the lockfile and refuses to build without it) and the `app`
project module the Dockerfile's `gunicorn app.wsgi` serves. Plain
`psycopg` uses the image's libpq; `psycopg[binary]` bundles its own old
copies of system libraries, which fail the image scan. Add the front-door
settings below (with the seeded `app/health.py` first in `MIDDLEWARE`),
the import of the seeded deploy settings (they bring the signing key
and the database), whitenoise (its middleware right after
`SecurityMiddleware`, and
`STATIC_ROOT = BASE_DIR / "staticfiles"`: the seeded Dockerfile runs
`collectstatic` at build), and one test (`manage.py test`).
The landing runs `make test`, and the seeded Makefile runs
`uv run python manage.py test`: keep that target running the whole suite. The
image applies migrations when it starts and serves with gunicorn; Django's
`runserver` is for development only. The image has two stages, both
`python:3.12-slim`: uv, the lockfile install, `collectstatic` and any
compiler a native wheel needs in the first; the final one copies the
`.venv` and the app and removes pip. Never use the full `python` image
(Debian's build image, with compilers, about 1 GB). The seeded `.apptown/app.yaml` declares
the web part, its readiness path and the Postgres need:

    needs:
      postgres:
        version: "16"

The seeded `app/deploy_settings.py` builds `DATABASES` from the `PG*`
variables AppTown injects wherever the app runs with that need (a
deployment, a preview, the agent's emulator), so the app uses Postgres
there and never SQLite. Keep that block; never write a literal host, and
never parse `DATABASE_URL` by hand.

**The signing key comes from the environment, with no fallback.**
`startproject` writes a literal `SECRET_KEY = "django-insecure-…"`;
delete it. The seeded deploy settings read

    SECRET_KEY = os.environ["DJANGO_SECRET_KEY"]

and the seeded `secretspec.toml` declares it, so AppTown generates one
per environment (`DJANGO_SECRET_KEY = { description = "Django signing
key", generate = true }`); keep that entry when you add secrets. Never
fall back to a written-down key
(`os.environ.get("DJANGO_SECRET_KEY", "dev-key")`): a deployment missing
its key would sign sessions with a key everyone can read, where without
the fallback the boot stops and says why. Two places run settings without
the deployed key, and each gets a throwaway, not a fallback:

- the image build: the seeded Dockerfile's `collectstatic` line sets
  `DJANGO_SECRET_KEY=collectstatic-at-build-only` for that one command.
  It signs nothing and is not in the running image's environment; keep it
  on that `RUN` line, never as `ENV`.
- the tests: AppTown gives the tests step a generated key, and the
  seeded Makefile generates one when none is set (your workspace).

The admin is for OPERATORS, not the public — shipping it is a decision, not
a default:

- **Not asked for? Don't route it.** `startproject` routes
  `path("admin/", admin.site.urls)` in urls.py; delete that line (and its
  import) unless the request asks for an admin or back-office screens. An
  unused admin login on a deployed app is attack surface with zero
  function.
- **Asked for? Ship it whole.** Two things make the difference between a
  feature and an embarrassment:
  - `collectstatic` in the Dockerfile (the seeded one runs it), with
    whitenoise serving statics — otherwise the deployed admin renders as an
    unstyled 1997 login page and the whole app looks broken.
  - A superuser story: `createsuperuser --noinput` at startup, skipped when
    the user already exists, driven by `DJANGO_SUPERUSER_USERNAME/EMAIL`
    set in the start command and `DJANGO_SUPERUSER_PASSWORD` from the
    environment. The password is a generated secret: declare it in the
    committed `secretspec.toml` beside the signing key and AppTown mints one per environment
    and delivers it — an administrator can reveal it under Settings ›
    Secrets. Credentials come from env, never code. An admin nobody can
    log into is worse than none.

        [profiles.default]
        DJANGO_SECRET_KEY = { description = "Django signing key", generate = true }
        DJANGO_SUPERUSER_PASSWORD = { description = "Django admin password", generate = true }

  - `whitenoise` in the dependency manifest, its middleware right after
    `SecurityMiddleware`, `STATIC_ROOT` set — collectstatic without a
    server for the result is half a fix.
- Say it in the report: "the admin is at /admin, user admin, the password
  is the environment's generated DJANGO_SUPERUSER_PASSWORD" — a shipped
  door the operator doesn't know about might as well not exist.

Rationale: for CRUD-shaped webapps the framework has already made every boring
decision correctly; the admin alone pays for Django — but only when it works
and someone holds the key.

Behind AppTown's front door (every environment: previews, main, the
intranet) the app is reached over HTTPS through a proxy that keeps the
public host name. AppTown tells the app where it is served in
`APPTOWN_ORIGINS`, as origins per tier, wildcards included
(`https://*.preview.<domain>,https://*.main.<domain>,…`). Build the
host and origin settings from it, or every form post dies with "CSRF
verification failed" (or every request with "Bad Request (400)"), and
list the seeded health middleware first, or the health probe dies
instead (below):

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

Without `APPTOWN_ORIGINS` the app is not behind AppTown's front door, so
it answers only on this machine: the fallback is exactly
`["localhost", "127.0.0.1"]`, never `["*"]` (any Host header trusted) and
never empty (every request refused). Django's tests add `testserver`
themselves. Never type a hostname into settings — it is wrong in the next
environment — and never set `ALLOWED_HOSTS = ["*"]` outright.

`/healthz` must answer both through the front door and **by the app's
address, on its own port**: a cloud platform's health check asks that
way, and so does AppTown's readiness check. Such a probe's Host is
an IP and port that the `ALLOWED_HOSTS` above refuses with 400, and it
carries no `X-Forwarded-Proto: https`, which `SECURE_SSL_REDIRECT`
answers with 301. Either way a healthy app looks dead. The seeded
`app/health.py` answers `/healthz` with 200 before Django looks at the
host or redirects; it answers no other path, never touches the
database, and leaves `ALLOWED_HOSTS` exactly as `APPTOWN_ORIGINS` says.
Keep it first in `MIDDLEWARE` (a project adopted without it writes the
same ten lines), keep the seeded `app/test_health.py`, which holds both
halves (`/healthz` by address answers 200; any other path by address is
still refused with 400), and keep `health: { readiness: { http: /healthz } }`
on the served part in `.apptown/app.yaml`. Never widen `ALLOWED_HOSTS`
(an IP, `"*"`) to let the probe in.

**The deploy check fails on warnings.** The landing runs
`manage.py check --deploy --fail-level WARNING`, so every warning fails it
as surely as an error, and a fresh `startproject` fails it. The seed ships
`app/deploy_settings.py` (`startproject` leaves it in place; a project
adopted without it writes the same settings) with the
settings that pass it, the signing key and the database; end
`app/settings.py` with

    from app.deploy_settings import *  # noqa: E402,F401,F403

and delete what `startproject` wrote that it replaces:

- the literal `SECRET_KEY`: the module reads `DJANGO_SECRET_KEY`.
- `DEBUG = True`: DEBUG comes from the environment and is off unless
  `DJANGO_DEBUG=1`.
- the console-backend `MAILERS` block: a development-only mail backend is
  an error in deployment. Configure no mailer (Django then uses SMTP) or
  a real one whose host and credentials come from the environment.

The module sets, while DEBUG is off: `SECURE_SSL_REDIRECT` (with
`SECURE_PROXY_SSL_HEADER`, so the front door's forwarded HTTPS counts;
the health middleware answers the probe before it), `SECURE_HSTS_SECONDS` with
`SECURE_HSTS_INCLUDE_SUBDOMAINS` and `SECURE_HSTS_PRELOAD`,
`SESSION_COOKIE_SECURE` and `CSRF_COOKIE_SECURE`. Together with the
signing key and `ALLOWED_HOSTS` above, that is everything the check asks
of a fresh project. Do not silence a check to pass it; if one does not
fit the app, say why in the report.

Checks: this way of working runs Django's own **`manage.py check --deploy`**
(the authoritative, framework-current deployment linter) and **semgrep**'s
django/python security rules — maintained tools, not hand-written greps.
Run them yourself before handing back with
`apptown check`; the deploy check gates a landing, the SAST findings are
advisory. The one thing no external tool knows is AppTown's front-door
contract above — that `APPTOWN_ORIGINS` is read, its wildcards translated,
and the hosts not left wide open — so that stays a small AppTown check.

Build output stays out of git: `.gitignore` names `__pycache__/` (or
`*.pyc`), so an import cache can never be committed by accident.

Dependencies are what the files say: `uv.lock` is committed and agrees
with `pyproject.toml`, so `uv sync` installs exactly what is committed,
everywhere.

**Fits a container limit.** Gunicorn runs one worker unless
`WEB_CONCURRENCY` asks for more; a Django worker uses about 80 MB. Declare
`resources: { memory: 160M }` in `.apptown/app.yaml` for one worker and about
80 MB more for each extra; deploy/memory-declared says how to choose and
check it.
