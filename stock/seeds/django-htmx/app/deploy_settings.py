"""Deployment settings, seeded by the django-htmx way of working.

`manage.py check --deploy --fail-level WARNING` gates every landing, so a
warning fails it as surely as an error. These are the settings a fresh
`startproject` lacks, plus the signing key and the database as AppTown
delivers them. End app/settings.py with

    from app.deploy_settings import *  # noqa: E402,F401,F403

so they apply last, and delete what they replace: startproject's literal
`SECRET_KEY`, its `DEBUG = True` and its console-backend `MAILERS` block
(a development-only mail backend is an error in deployment).

DEBUG is off unless DJANGO_DEBUG=1 says otherwise. The HTTPS-only
settings follow it: on a developer's http://localhost they would redirect
to an https:// that is not there and drop the session cookie.
"""
import os

# The signing key comes from the environment, with no fallback: a missing
# key stops the boot and says why, where a written-down default would sign
# sessions with a key everyone can read. secretspec.toml declares it, so
# AppTown generates one per deployment.
SECRET_KEY = os.environ["DJANGO_SECRET_KEY"]

DEBUG = os.environ.get("DJANGO_DEBUG") == "1"

# The database is the Postgres the runtime contract (.forge/app.yaml)
# needs. Wherever AppTown runs the app with it (a deployment, a change's
# preview, an agent's emulator) it injects the PG* variables (and
# DATABASE_URL, which says the same thing); libpq reads the rest itself
# (PGSSLMODE among them), so nothing here parses a URL or names a host.
# Without PGHOST (your own workspace with no database running)
# startproject's DATABASES stays as it is.
if os.environ.get("PGHOST"):
    DATABASES = {
        "default": {
            "ENGINE": "django.db.backends.postgresql",
            "NAME": os.environ.get("PGDATABASE", ""),
            "USER": os.environ.get("PGUSER", ""),
            "PASSWORD": os.environ.get("PGPASSWORD", ""),
            "HOST": os.environ["PGHOST"],
            "PORT": os.environ.get("PGPORT", ""),
        }
    }

# AppTown's front door terminates TLS and says so in X-Forwarded-Proto;
# without this header trusted, every request looks like plain HTTP and the
# redirect below loops.
SECURE_PROXY_SSL_HEADER = ("HTTP_X_FORWARDED_PROTO", "https")
# The readiness probe calls the app by its address over plain HTTP; the
# seeded app/health.py, first in MIDDLEWARE, answers it before this
# redirect (or host validation) can run.
SECURE_SSL_REDIRECT = not DEBUG
# Browsers refuse plain HTTP to this host (and its subdomains) for a year.
# AppTown serves every deployment over HTTPS, the desktop trial included,
# so this costs nothing there.
SECURE_HSTS_SECONDS = 0 if DEBUG else 31536000
SECURE_HSTS_INCLUDE_SUBDOMAINS = not DEBUG
# Preload is a request to be built into browsers, made by whoever owns the
# registrable domain; announcing it from an app's host is harmless and
# keeps the check quiet without silencing it.
SECURE_HSTS_PRELOAD = not DEBUG
SESSION_COOKIE_SECURE = not DEBUG
CSRF_COOKIE_SECURE = not DEBUG
