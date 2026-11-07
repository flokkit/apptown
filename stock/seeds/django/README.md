# This project

A Django web application with server-rendered pages and HTMX, managed
with uv.

## Run

    uv sync
    APPTOWN_TEST_REPORT=/tmp/django-tests.xml make test
    uv run python manage.py runserver
    docker build -t app . && docker run --rm -p 8080:8080 app

The first app change imports `requirements-test.txt` with
`uv add --dev -r requirements-test.txt`, commits `pyproject.toml` and `uv.lock`,
and enables the Makefile target and the contract's `test:` declaration.
`make test` installs from the lockfile and runs the whole `manage.py test`
suite with `app.test_settings`. It writes JUnit XML to `APPTOWN_TEST_REPORT`
and readable results to the log, with a throwaway `DJANGO_SECRET_KEY` when
none is set. AppTown supplies the report path in its tests step.
The image collects static files at
build, applies migrations at start and serves with gunicorn on port 8080;
`GET /healthz` answers 200 when it is ready.
