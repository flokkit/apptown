# This project

A Django web application with server-rendered pages and HTMX, managed
with uv.

## Run

    uv sync
    make test
    uv run python manage.py runserver
    docker build -t app . && docker run --rm -p 8080:8080 app

`make test` runs the test suite (`manage.py test`), with a throwaway
`DJANGO_SECRET_KEY` when none is set. The image collects static files at
build, applies migrations at start and serves with gunicorn on port 8080;
`GET /healthz` answers 200 when it is ready.
