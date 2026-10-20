# The project's tests, as AppTown's landing runs them: `make test`.
# Keep this target running the whole suite as the app grows. Until the
# app exists there is nothing to test, and the target says so.
# .PHONY: without it, a folder named test makes make skip the target.
.PHONY: test
test:
	@if [ -e Cargo.toml ]; then cargo test --locked; else echo "no Cargo.toml yet: nothing to test"; fi
