# The project's test entry point: `make test`.
# The target stays commented out until the application exists. The change
# creating it enables the target and declares test: ["make test"] and tools:
# on the part in .apptown/app.yaml. An empty starter declares neither, so
# the landing's tests step is not applicable.
# Keep the target running the whole suite as the app grows.
# The test database is prepared first: Rails refuses to run tests while
# migrations are pending, and the database the landing provides starts
# empty. db:prepare loads db/schema.rb when it is committed and runs the
# migrations when it is not, so the suite runs either way.
# .PHONY: without it, a folder named test makes make skip the target.
#.PHONY: test
#test:
#	bundle install --quiet && RAILS_ENV=test bin/rails db:prepare && bin/rails test
