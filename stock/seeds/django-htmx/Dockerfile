# Packaging recipe for this way of working (built by AppTown at release).
# Adapt the project module (`app` here, as `django-admin startproject app .`
# names it) to the app; keep the python base. Dependencies via uv from the
# committed lockfile — the declared way of working (uv for everything,
# mise.toml pins the interpreter): pyproject.toml AND uv.lock must both be
# committed, and gunicorn must be one of the dependencies.
# uv and pip build; they do not ship. The runtime stage removes the pip the
# python image carries (the app's .venv has none): the app never runs it,
# and its known vulnerabilities would fail the image scan on every landing.
# The build stage carries libpq5 too: collectstatic loads the settings, and
# a Postgres DATABASES makes Django import psycopg, which needs libpq.
FROM python:3.12-slim AS build
RUN apt-get update \
    && apt-get install -y --no-install-recommends libpq5 \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
RUN pip install --no-cache-dir uv
COPY pyproject.toml uv.lock ./
RUN uv sync --frozen --no-dev
COPY . .
# Static files (the admin's included) are collected here; whitenoise serves
# them and STATIC_ROOT names the folder. The build has none of the app's
# secrets, and settings refuse to load without DJANGO_SECRET_KEY, so this
# one command gets a throwaway that signs nothing. It is set on this line
# only, never as ENV: the running image has no key but the one delivered.
RUN DJANGO_SECRET_KEY=collectstatic-at-build-only .venv/bin/python manage.py collectstatic --noinput

# The runtime stage takes the distribution's security updates at build
# time, so a known fixed CVE in the base (libssl, libpcre2) does not fail
# the image scan. psycopg (not psycopg[binary], whose wheel bundles its own
# old libraries) loads Debian's patched libpq. The app runs as an
# unprivileged user that owns /app (a SQLite database, if one is used, is
# created there) and has a home (gunicorn keeps its control socket there).
FROM python:3.12-slim
RUN apt-get update && apt-get upgrade -y \
    && apt-get install -y --no-install-recommends libpq5 \
    && rm -rf /var/lib/apt/lists/* \
    && python -m pip uninstall -y pip \
    && useradd --uid 10001 --create-home app \
    && install -d -o app -g app /app
WORKDIR /app
COPY --from=build --chown=app:app /app /app
ENV PATH="/app/.venv/bin:$PATH" PYTHONUNBUFFERED=1
USER app
EXPOSE 8080
# Start: bring the schema up, then serve with gunicorn (Django's runserver
# is a development server). $PORT when the platform sets one.
CMD ["sh", "-c", "python manage.py migrate --noinput && exec gunicorn app.wsgi --bind 0.0.0.0:${PORT:-8080}"]
