"""The readiness answer, seeded by the django pack.

Some health probes (a cloud platform's, AppTown's readiness check)
reach the app by its address, not through AppTown's front door: their
Host is an IP and port that ALLOWED_HOSTS (built from APPTOWN_ORIGINS)
refuses with 400, and they carry no
X-Forwarded-Proto, so SECURE_SSL_REDIRECT would answer 301. List this
middleware FIRST in MIDDLEWARE, so it answers the readiness path declared
in .apptown/app.yaml before Django validates the host or redirects:

    MIDDLEWARE = [
        "app.health.healthz",
        "django.middleware.security.SecurityMiddleware",
        ...
    ]

It answers that one path and nothing else, and never touches the
database. Every other path still meets host validation, so ALLOWED_HOSTS
stays exactly what APPTOWN_ORIGINS says.
"""
from django.http import HttpResponse

HEALTH_PATH = "/healthz"


def healthz(get_response):
    def middleware(request):
        if request.path == HEALTH_PATH:
            return HttpResponse("ok", content_type="text/plain")
        return get_response(request)

    return middleware
