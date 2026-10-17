# The project's tests, as the forge's landing runs them: `make test`.
# Keep this target running the whole suite as the app grows. Until the
# app exists there is nothing to test, and the target says so.
# The test database is prepared first: Rails refuses to run tests while
# migrations are pending, and the database the landing provides starts
# empty. db:prepare loads db/schema.rb when it is committed and runs the
# migrations when it is not, so the suite runs either way.
# .PHONY: without it, a folder named test makes make skip the target.
.PHONY: test
test:
	@if [ -e bin/rails ]; then bundle install --quiet && RAILS_ENV=test bin/rails db:prepare && bin/rails test; else echo "no bin/rails yet: nothing to test"; fi
