---
name: lang/selection/rails
tags: lang,ruby,rails
checks: [{"name": "trusts-front-door", "expects": "production trusts AppTown's front door: config.hosts is built from APPTOWN_ORIGINS with wildcard origins translated to Rails' leading-dot form, never cleared, and the health probe is excluded", "severity": "high", "evaluator": "sh", "sh": "f=config/environments/production.rb; [ -f \"$f\" ] || exit 0; if ! grep -q APPTOWN_ORIGINS \"$f\"; then echo \"$f never reads APPTOWN_ORIGINS — behind AppTown's front door Rails either blocks every host (Blocked hosts) or trusts every host. Build config.hosts from APPTOWN_ORIGINS (wildcards become Rails' leading-dot form) and exclude /up from host authorization.\"; exit 1; fi; if grep -Eq \"^[[:space:]]*config\\.hosts\\.clear\" \"$f\"; then echo \"$f clears config.hosts, so production trusts any Host header. Build config.hosts from APPTOWN_ORIGINS instead.\"; exit 1; fi; if ! grep -Eq '\\*\\\\?\\.' \"$f\"; then echo \"$f reads APPTOWN_ORIGINS but never translates its wildcard origins (https://*.preview.<domain>): an origin is not a host, so Rails blocks every request through the front door. Take each origin's host and write a leading *. as . (Rails' subdomain form): config.hosts = ENV.fetch('APPTOWN_ORIGINS', '').split(',').map { |o| URI(o).host.sub(/\\\\A\\\\*\\\\./, '.') }\"; exit 1; fi; if ! grep -q host_authorization \"$f\"; then echo \"$f restricts config.hosts but never excludes the readiness probe, which reaches the app on its own address: /up answers Blocked hosts and the boot looks dead. Add: config.host_authorization = { exclude: ->(request) { request.path == '/up' } }\"; exit 1; fi", "title": "Configure the application to trust AppTown’s front door"}, {"name": "cmd-not-entrypoint", "expects": "the Dockerfile ends in CMD, so init steps can run the same image with other arguments", "severity": "medium", "evaluator": "sh", "sh": "if [ -f Dockerfile ] && grep -q '^ENTRYPOINT' Dockerfile; then echo 'Dockerfile uses ENTRYPOINT — the init step (bundle exec rake db:prepare) is appended to it and breaks. Use CMD [\"bundle\", \"exec\", \"puma\", \"-C\", \"config/puma.rb\"].'; exit 1; fi", "title": "Make the container’s application command replaceable"}, {"name": "no-encrypted-credentials", "expects": "no encrypted credentials bundle or master key in the repo: every secret is declared on its own and comes from the environment", "severity": "high", "evaluator": "sh", "sh": "BAD=$(ls config/credentials.yml.enc config/master.key config/credentials/*.yml.enc config/credentials/*.key 2>/dev/null || true); [ -z \"$BAD\" ] || { echo \"encrypted credentials in the repo: $BAD — one key unlocking a committed bundle is a second secrets system AppTown cannot see; delete them, set SECRET_KEY_BASE (and every other secret) from the environment\"; exit 1; }", "title": "Load application credentials from declared secrets"}, {"name": "gitignore-names-build-output", "expects": "git would ignore what Rails writes (tmp/, log/), so build output can never be committed by accident. If the project commits that output deliberately, this check does not fit it: say so and stop rather than add the line, since an ignore rule over tracked files makes the repository contradict itself", "severity": "medium", "evaluator": "sh", "sh": "t=$(mktemp -d) && git -c init.defaultBranch=main init -q \"$t\" || exit 2; bad=0; for p in tmp/probe log/probe.log; do git --git-dir=\"$t/.git\" --work-tree=. -c core.excludesFile=/dev/null check-ignore -q \"$p\" || { echo \".gitignore does not cover ${p%/*}/\"; bad=1; }; done; exit $bad", "title": "Declare generated files in the repository’s ignore rules"}, {"name": "lockfile-in-step", "expects": "Gemfile.lock is committed and agrees with the Gemfile (Bundler's frozen check passes), so an install resolves exactly what is committed", "severity": "medium", "evaluator": "tool", "tool": "bundle-lock-check", "title": "Keep the dependency lockfile in sync"}]
---
Ruby web apps are current stable Rails on Ruby 3.4 (pinned in mise.toml),
Postgres, Puma, propshaft for assets and importmap for any JavaScript —
no Node toolchain in the repo. The pack seeds the packaging recipe, the
ignore rules, a README and the runtime contract; the app comes from
Rails' own generator, which is always current and complete, never from a
hand-kept skeleton.

**The first act on an empty repo** (the "build the first version" change):

    rails new . --database=postgresql --skip-docker --skip-kamal --skip-thruster --skip-ci --skip

`--skip-docker` keeps the seeded Dockerfile (CMD, port 8080, the init
step reuses it); `--skip-thruster` leaves out a proxy the image never runs
(its Go binary carries findings the scan refuses); `--skip` keeps every file the seed already wrote (the
`.gitignore` carries Rails' own ignore list plus AppTown's lines, and an
overwrite would drop `.apptown/*`; `config/database.yml` reads
`DATABASE_URL`; `config/cable.yml` keeps Action Cable in-process). The seeded runtime contract at
`.apptown/app.yaml` declares the web part with `/up` as its readiness
path, the Postgres need, so the database is there from the first
round, and the init step that loads the schema before the app starts (a
cold container without it answers every page with `PG::UndefinedTable`).
AppTown fulfils the need and runs the init step in every environment;
keep both:

    app:
      web:
        build: .
        ports: ["8080"]
        public: true
        health:
          readiness: { http: /up }
    init:
      - name: migrate
        build: .
        command: ["bundle", "exec", "rake", "db:prepare"]
    needs:
      postgres:
        version: "16"

Add a page, one test that hits it (`bin/rails test`), commit
`Gemfile.lock`, and the front-door lines below in production.rb. The
checks on this note fail until the app trusts the front door and the
recipe ends in CMD.

- **Server-rendered first.** ERB views, Hotwire (Turbo + Stimulus via
  importmap) when a page needs to feel live. A separate JavaScript build
  is a decision, not a default — record it in an ADR.
- **Postgres through the need.** `DATABASE_URL` is injected everywhere;
  the seeded `config/database.yml` (kept by `rails new --skip`) reads it in
  every environment and configures nothing by hand. Keep it: Rails' own
  version names a user and separate production databases for cache,
  queue and cable that the need never created, and the init step fails on
  them.
  `db/schema.rb` is the schema; `rake db:prepare` in the init step loads
  it before the app starts; evolve it with migrations. Every change that
  adds a migration runs it (`bin/rails db:migrate`, or `make test`, which
  prepares the test database) and commits the `db/schema.rb` it writes in
  the same commit: the schema in the repository is the one the migrations
  produce.
- **Cache, jobs and cable without extra tables.** With one database,
  `db:prepare` never loads Rails' separate cache, queue and cable schemas,
  so Solid Cache and Solid Queue find no tables in production. In
  `config/environments/production.rb` use `config.cache_store =
  :memory_store` and `config.active_job.queue_adapter = :async`; the
  seeded `config/cable.yml` (kept by `rails new --skip`) already uses the
  `async` adapter, so keep it. When jobs must survive a restart,
  install Solid Queue's tables as ordinary migrations (Rails' single-database
  setup) and switch the adapter back; say so in an ADR.
- **Trust the front door.** AppTown terminates TLS and forwards proto
  and host, and tells the app where it is served (`APPTOWN_ORIGINS`, origins
  per tier, wildcards included: `https://*.preview.<domain>,…`). In
  production, `config.hosts` is built from each origin's host, a leading
  `*.` written as `.` (Rails' form for "this domain and its subdomains"),
  and `/up` is excluded so the probe, which reaches the app on its own
  address, gets in:

      config.hosts = ENV.fetch("APPTOWN_ORIGINS", "").split(",").map { |o| URI(o).host.sub(/\A\*\./, ".") }
      config.host_authorization = { exclude: ->(request) { request.path == "/up" } }

  With no `APPTOWN_ORIGINS` (not behind AppTown) the list is empty and
  Rails restricts nothing. Never `config.hosts.clear` in production.
- **Secrets are not in the repo, and never as an encrypted bundle.**
  Delete `config/credentials.yml.enc` and `config/master.key` (and
  `config/credentials/`) right after `rails new`: one key unlocking a
  committed bundle is a second secrets system AppTown cannot see, and
  its key can never be generated because it must match the file. Every secret is declared on
  its own, in a committed `secretspec.toml`, and comes from the
  environment. AppTown generates `SECRET_KEY_BASE` itself, one per
  environment, when the declaration says so:

      [project]
      name = "<slug>"
      revision = "1.0"

      [profiles.default]
      SECRET_KEY_BASE = { description = "signs cookies and sessions", generate = true }

  Add every other secret the app reads (an API key, a token) as its own
  entry; a person binds those under Settings → Secrets, and a boot stops
  with a sentence naming any that is missing. Never commit a value.
- **One deployable per repo**: the ruby image, `bundle exec puma`. The
  Dockerfile ends in `CMD`, never `ENTRYPOINT`, so the init step can run
  the same image with `db:prepare`. It has two stages, both
  `ruby:3.4-slim`: the first installs build-essential, libpq-dev,
  libyaml-dev and git, bundles and precompiles assets; the final one
  installs only libpq5 and libyaml-0-2 and copies `/usr/local/bundle` and
  `/app` (the seed; Rails' own generated Dockerfile splits the same way).
  The compiler and headers native gems need never ship. Bundler reads the ambient mirror
  (`BUNDLE_MIRROR__HTTPS://RUBYGEMS__ORG`); never write a mirror URL
  into the Gemfile. Commit `Gemfile.lock` as soon as it exists.
- **Tests are Rails' own** (`bin/rails test`, minitest) against the real app.
  The landing runs `make test` against an empty database; the seeded
  Makefile installs the gems, prepares the test database
  (`RAILS_ENV=test bin/rails db:prepare`) and runs `bin/rails test`. Keep
  that target running the whole suite, and keep the prepare step:
  without it the suite stops with "Migrations are pending".

Runtime output stays out of git: `.gitignore` names `tmp/` and `log/`
(as `rails new` writes them), so a local run can never be committed by
accident.

Dependencies are what the files say: `Gemfile.lock` is committed and
agrees with the `Gemfile` (Bundler's frozen check, the same one
`bundle install --frozen` makes), so the image installs exactly what is
committed. A Gemfile changed without `bundle lock` fails the check.

The image scan on ruby:3.4-slim reports one finding the app cannot fix:
Ruby's own default `json` gem (2.9.1, LOW) stays on disk even when
`Gemfile.lock` installs a newer json, which the app then uses. Ask a
person to record it as an exception with that justification
(build/container-image says how) until a Ruby release ships the newer
default; do not delete gem metadata to hide it.

**Fits a container limit.** Puma runs in single mode unless
`WEB_CONCURRENCY` asks for workers, each about 120 MB. Single mode uses about
120 MB: declare `resources: { memory: 256Mi }` in `.apptown/app.yaml` and raise
it with the workers; deploy/memory-declared says how to choose and check it.
