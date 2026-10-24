# The project's tests, as AppTown's landing runs them: `make test`.
# Uncomment the target below in the change that writes the app: until
# then there is nothing to test, the landing finds no test command, and
# its tests step says not applicable. Once the app's part names its tool
# (tools: in .apptown/app.yaml), a missing test command fails the landing.
# Keep the target running the whole suite as the app grows.
# The test database is prepared first: Rails refuses to run tests while
# migrations are pending, and the database the landing provides starts
# empty. db:prepare loads db/schema.rb when it is committed and runs the
# migrations when it is not, so the suite runs either way.
# .PHONY: without it, a folder named test makes make skip the target.
#.PHONY: test
#test:
#	bundle install --quiet && RAILS_ENV=test bin/rails db:prepare && bin/rails test
