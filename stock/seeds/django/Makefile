# The project's test entry point: `make test`.
# The target stays commented out until the application exists. The change
# creating it enables the target and declares test: ["make test"] and tools:
# on the part in .apptown/app.yaml. An empty starter declares neither, so
# the landing's tests step is not applicable.
# Keep the target running the whole suite as the app grows.
# Settings refuse to load without DJANGO_SECRET_KEY. AppTown gives the
# tests a generated one; where none is set (your own workspace), the
# target generates a throwaway for this run, so no key is ever written down.
# .PHONY: without it, a folder named test makes make skip the target.
#.PHONY: test
#test:
#	DJANGO_SECRET_KEY="$${DJANGO_SECRET_KEY:-$$(uv run python -c 'import secrets; print(secrets.token_urlsafe(50))')}" uv run python manage.py test
