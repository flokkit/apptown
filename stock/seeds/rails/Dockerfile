# Packaging recipe for this way of working (built by AppTown at release).
# Generate the app with `rails new . --skip-docker …` (see the rails
# selection guidance) so this recipe is kept: gems from the committed
# Gemfile.lock, assets precompiled at build. CMD, not ENTRYPOINT, so the
# init step in .forge/app.yaml can run the same image with db:prepare.
# Two stages on the same ruby image: the build stage carries the compiler
# and headers that native gems (pg, psych) need; the runtime stage carries
# only the libraries those gems load, so no compiler ships with the app.
# Ruby's own default gems cannot be replaced in place: a ruby whose bundled
# resolv had a fixed CVE (3.3) is left for the minor that ships the fix
# (3.4), not patched around.
FROM ruby:3.4-slim AS build
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
      build-essential libpq-dev libyaml-dev git \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
ENV RAILS_ENV=production BUNDLE_WITHOUT=development:test
COPY Gemfile Gemfile.lock ./
RUN bundle install && rm -rf /usr/local/bundle/cache
COPY . .
RUN SECRET_KEY_BASE_DUMMY=1 bundle exec rake assets:precompile

# The runtime stage takes the distribution's security updates at build
# time, so a fixed CVE in the base does not fail the image scan. The app
# writes only its runtime folders; everything else stays root's.
FROM ruby:3.4-slim
RUN apt-get update && apt-get upgrade -y \
    && apt-get install -y --no-install-recommends libpq5 libyaml-0-2 \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 10001 --create-home app
WORKDIR /app
ENV RAILS_ENV=production BUNDLE_WITHOUT=development:test PORT=8080
COPY --from=build /usr/local/bundle /usr/local/bundle
COPY --from=build /app /app
RUN mkdir -p db log storage tmp && chown -R app:app db log storage tmp
USER app
EXPOSE 8080
CMD ["bundle", "exec", "puma", "-C", "config/puma.rb"]
