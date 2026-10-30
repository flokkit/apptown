# The project's tests, as AppTown's landing runs them: `make test`.
# Uncomment the target below in the change that writes the app: until
# then there is nothing to test, the landing finds no test command, and
# its tests step says not applicable. Once the app's part names its tool
# (tools: in .apptown/app.yaml), a missing test command fails the landing.
# Keep the target running the whole suite as the app grows.
# Settings refuse to load without DJANGO_SECRET_KEY. AppTown gives the
# tests a generated one; where none is set (your own workspace), the
# target generates a throwaway for this run, so no key is ever written down.
# .PHONY: without it, a folder named test makes make skip the target.
#.PHONY: test
#test:
#	DJANGO_SECRET_KEY="$${DJANGO_SECRET_KEY:-$$(uv run python -c 'import secrets; print(secrets.token_urlsafe(50))')}" uv run python manage.py test
