# The project's tests, as the forge's landing runs them: `make test`.
# Keep this target running the whole suite as the app grows. Until the
# app exists there is nothing to test, and the target says so.
# Settings refuse to load without DJANGO_SECRET_KEY. The forge gives the
# tests a generated one; where none is set (your own workspace), the
# target generates a throwaway for this run, so no key is ever written down.
# .PHONY: without it, a folder named test makes make skip the target.
.PHONY: test
test:
	@if [ -e manage.py ]; then DJANGO_SECRET_KEY="$${DJANGO_SECRET_KEY:-$$(uv run python -c 'import secrets; print(secrets.token_urlsafe(50))')}" uv run python manage.py test; else echo "no manage.py yet: nothing to test"; fi
