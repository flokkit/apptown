---
name: lang/selection/rails
tags: lang,ruby,rails
checks: [{"name": "trusts-front-door", "expects": "production trusts AppTown's front door: config.hosts is built from APPTOWN_ORIGINS with wildcard origins translated to Rails' leading-dot form, never cleared, and the health probe is excluded", "severity": "high", "evaluator": "sh", "sh": "f=config/environments/production.rb; [ -f \"$f\" ] || { w=\"part $APPTOWN_PARTS_HERE is\"; case \"$APPTOWN_PARTS_HERE\" in *\" \"*) w=\"parts $APPTOWN_PARTS_HERE are\";; esac; d=$APPTOWN_FOLDER; [ \"$d\" = . ] && d=\"the repository root\"; echo \"$w built with ruby (tools: in .apptown/app.yaml) and there is no $f in $d: this way of working's apps are Rails (rails new .); add it, or correct the part's tools\"; exit 1; }; if ! grep -q APPTOWN_ORIGINS \"$f\"; then echo \"$f never reads APPTOWN_ORIGINS — behind AppTown's front door Rails either blocks every host (Blocked hosts) or trusts every host. Build config.hosts from APPTOWN_ORIGINS (wildcards become Rails' leading-dot form) and exclude /up from host authorization.\"; exit 1; fi; if grep -Eq \"^[[:space:]]*config\\.hosts\\.clear\" \"$f\"; then echo \"$f clears config.hosts, so production trusts any Host header. Build config.hosts from APPTOWN_ORIGINS instead.\"; exit 1; fi; if ! grep -Eq '\\*\\\\?\\.' \"$f\"; then echo \"$f reads APPTOWN_ORIGINS but never translates its wildcard origins (https://*.preview.<domain>): an origin is not a host, so Rails blocks every request through the front door. Take each origin's host and write a leading *. as . (Rails' subdomain form): config.hosts = ENV.fetch('APPTOWN_ORIGINS', '').split(',').map { |o| URI(o).host.sub(/\\\\A\\\\*\\\\./, '.') }\"; exit 1; fi; if ! grep -q host_authorization \"$f\"; then echo \"$f restricts config.hosts but never excludes the readiness probe, which reaches the app on its own address: /up answers Blocked hosts and the boot looks dead. Add: config.host_authorization = { exclude: ->(request) { request.path == '/up' } }\"; exit 1; fi", "title": "Configure the application to trust AppTown’s front door", "appliesTo": ["tool:ruby"]}, {"name": "cmd-not-entrypoint", "expects": "the Dockerfile ends in CMD, so init steps can run the same image with other arguments", "severity": "medium", "evaluator": "sh", "sh": "[ -f Dockerfile ] || { w=\"part $APPTOWN_PARTS_HERE is\"; case \"$APPTOWN_PARTS_HERE\" in *\" \"*) w=\"parts $APPTOWN_PARTS_HERE are\";; esac; d=$APPTOWN_FOLDER; [ \"$d\" = . ] && d=\"the repository root\"; echo \"$w built with ruby (tools: in .apptown/app.yaml) and there is no Dockerfile in $d to build its image: add it, or correct the part's tools\"; exit 1; }; if grep -q '^ENTRYPOINT' Dockerfile; then echo 'Dockerfile uses ENTRYPOINT — the init step (bundle exec rake db:prepare) is appended to it and breaks. Use CMD [\"bundle\", \"exec\", \"puma\", \"-C\", \"config/puma.rb\"].'; exit 1; fi", "title": "Make the container’s application command replaceable", "appliesTo": ["tool:ruby"]}, {"name": "no-encrypted-credentials", "expects": "no encrypted credentials bundle or master key in the repo: every secret is declared on its own and comes from the environment", "severity": "high", "evaluator": "sh", "sh": "[ -f Gemfile ] || { w=\"part $APPTOWN_PARTS_HERE is\"; case \"$APPTOWN_PARTS_HERE\" in *\" \"*) w=\"parts $APPTOWN_PARTS_HERE are\";; esac; d=$APPTOWN_FOLDER; [ \"$d\" = . ] && d=\"the repository root\"; echo \"$w built with ruby (tools: in .apptown/app.yaml) and there is no Gemfile in $d: add it, or correct the part's tools\"; exit 1; }; BAD=$(ls config/credentials.yml.enc config/master.key config/credentials/*.yml.enc config/credentials/*.key 2>/dev/null || true); [ -z \"$BAD\" ] || { echo \"encrypted credentials in the repo: $BAD — one key unlocking a committed bundle is a second secrets system AppTown cannot see; delete them, set SECRET_KEY_BASE (and every other secret) from the environment\"; exit 1; }", "title": "Load application credentials from declared secrets", "appliesTo": ["tool:ruby"]}, {"name": "gitignore-names-build-output", "expects": "git would ignore what Rails writes (tmp/, log/), so build output can never be committed by accident. If the project commits that output deliberately, this check does not fit it: say so and stop rather than add the line, since an ignore rule over tracked files makes the repository contradict itself", "severity": "medium", "evaluator": "sh", "sh": "t=$(mktemp -d) && git -c init.defaultBranch=main init -q \"$t\" || exit 2; find . \\( -name node_modules -o -name vendor -o -name .git \\) -prune -o -type f \\( -name Gemfile \\) -print > \"$t/m\" || exit 2; awk -v dir=1 'BEGIN { base = ENVIRON[\"APPTOWN_FOLDER\"]; n = split(ENVIRON[\"APPTOWN_NOT_APP_CODE\"], x, \"\\n\") } { f = $0; sub(/^\\.\\//, \"\", f); if (base != \".\" && base != \"\") f = base \"/\" f; for (i = 1; i <= n; i++) if (x[i] != \"\" && index(f, x[i] \"/\") == 1) next; if (dir) { if (!sub(/\\/[^\\/]*$/, \"\", f)) f = \"\" } print f }' \"$t/m\" | sort -u > \"$t/d\" || exit 2; [ -s \"$t/d\" ] || { w=\"part $APPTOWN_PARTS_HERE is\"; case \"$APPTOWN_PARTS_HERE\" in *\" \"*) w=\"parts $APPTOWN_PARTS_HERE are\";; esac; d=$APPTOWN_FOLDER; [ \"$d\" = . ] && d=\"the repository root\"; echo \"$w built with ruby (tools: in .apptown/app.yaml) and there is no Gemfile in $d or below it: add it, or correct the part's tools\"; exit 1; }; bad=0; while IFS= read -r r; do for p in tmp/probe log/probe.log; do q=${r:+$r/}$p; git -C \"$APPTOWN_ROOT\" --git-dir=\"$t/.git\" --work-tree=\"$APPTOWN_ROOT\" -c core.excludesFile=/dev/null check-ignore -q \"$q\"; s=$?; [ $s -eq 0 ] && continue; [ $s -eq 1 ] || exit 2; echo \".gitignore does not cover ${q%/*}/ (Rails writes it)\"; bad=1; done; done < \"$t/d\"; exit $bad", "title": "Declare generated files in the repository’s ignore rules", "appliesTo": ["tool:ruby"]}, {"name": "lockfile-in-step", "expects": "Gemfile.lock is committed and agrees with the Gemfile (Bundler's frozen check passes), so an install resolves exactly what is committed", "severity": "medium", "evaluator": "tool", "tool": "bundle-lock-check", "title": "Keep the dependency lockfile in sync", "appliesTo": ["tool:ruby"]}]
---
Ruby web apps are current stable Rails on Ruby 3.4 (pinned in mise.toml),
Postgres, Puma and propshaft; pages come from the project's UI pack. The pack
seeds the Dockerfile, ignore rules, README, `config/database.yml`,
`config/cable.yml` and the contract; the app comes from Rails' own generator.

**The first act on an empty repo:**

    rails new . --database=postgresql --skip-docker --skip-kamal --skip-thruster --skip-ci --skip

`--skip` keeps every seeded file; never overwrite them. Keep the seeded
`.apptown/app.yaml`:

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

Add a page, one test that hits it (`bin/rails test`), commit `Gemfile.lock`, and
the front-door lines below.

- **Postgres through the need.** The seeded `config/database.yml` reads the
  injected `DATABASE_URL` in every environment; keep it (Rails' own names
  databases the need never created). `db/schema.rb` is the schema; evolve it
  with migrations, run each (`bin/rails db:migrate`) and commit the
  `db/schema.rb` it writes in the same commit.
- **One database:** in `config/environments/production.rb`,
  `config.cache_store = :memory_store` and
  `config.active_job.queue_adapter = :async`; keep the seeded `cable.yml`
  (`async`). When jobs must survive a restart, install Solid Queue's tables as
  ordinary migrations and switch the adapter back, in an ADR.
- **Trust the front door.** In production:

      config.hosts = ENV.fetch("APPTOWN_ORIGINS", "").split(",").map { |o| URI(o).host.sub(/\A\*\./, ".") }
      config.host_authorization = { exclude: ->(request) { request.path == "/up" } }

  Never `config.hosts.clear` in production.
- **No credentials bundle.** Delete `config/credentials.yml.enc`,
  `config/master.key` and `config/credentials/` right after `rails new`. Every
  secret is its own entry in a committed `secretspec.toml`, read from the
  environment; never commit a value:

      [project]
      name = "<slug>"
      revision = "1.0"

      [profiles.default]
      SECRET_KEY_BASE = { description = "signs cookies and sessions", generate = true }

- **One deployable:** `bundle exec puma`; the Dockerfile ends in `CMD`, never
  `ENTRYPOINT`, so the init step reuses the image. Two stages, both
  `ruby:3.4-slim`: build-essential, libpq-dev, libyaml-dev and git, bundle and
  asset precompile in the first; only libpq5 and libyaml-0-2 plus
  `/usr/local/bundle` and `/app` in the final one. Bundler reads the ambient
  mirror; never a mirror URL in the Gemfile.
- **Tests:** `make test` installs gems, runs `RAILS_ENV=test bin/rails
  db:prepare` and `bin/rails test` against an empty database; keep both steps.
  The starter's Makefile ships its `make test` target commented out until the
  app exists; the change that writes the app uncomments it (the landing's tests
  step fails for a part built with ruby while there is no test command).
- `Gemfile.lock` is committed and agrees with the `Gemfile` (run `bundle lock`
  after changing it). `.gitignore` names `tmp/` and `log/`.

The image scan reports Ruby's default `json` gem (2.9.1, LOW), which stays on
disk though the lockfile installs a newer one: ask a person to record it as an
exception (build/container-image); never delete gem metadata to hide it.

**Memory:** Puma single mode is about 120 MB; declare
`resources: { memory: 256Mi }` and raise it with workers.
