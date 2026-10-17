---
name: data/networked-database
tags: data,storage,platform
conflicts: data/sqlite-everywhere
checks: [{"name": "db-client-has-need", "expects": "every database or cache client the app depends on has a matching need declared in .forge/app.yaml — add it under needs: so every environment fulfils it", "severity": "high", "evaluator": "sh", "sh": "m=$(cat go.mod requirements*.txt pyproject.toml package.json Gemfile *.csproj 2>/dev/null); needs=$(sed -n '/^needs:/,/^[^ ]/p' .forge/app.yaml 2>/dev/null); miss=''; if echo \"$m\" | grep -Eqi 'jackc/pgx|lib/pq|psycopg|asyncpg|\"pg\"|\"postgres\"|npgsql|gem .pg.|pgsql'; then echo \"$needs\" | grep -q '^  postgres' || miss=\"$miss postgres\"; fi; if echo \"$m\" | grep -Eqi 'go-redis|ioredis|\"redis\"|redis-py|gem .redis.|stackexchange.redis'; then echo \"$needs\" | grep -q '^  redis' || miss=\"$miss redis\"; fi; [ -z \"$miss\" ] || { for n in $miss; do echo \"the app depends on a $n client but .forge/app.yaml declares no $n need\"; done; exit 1; }", "occurrences": "lines", "title": "Declare the database the application uses"}, {"name": "no-file-database", "expects": "persistence is a networked need, not a database file on disk — state lives in a declared need (postgres, redis, s3) so it survives on targets without a disk; a database file is not something the forge can provision, back up or move", "severity": "high", "evaluator": "sh", "sh": "bad=$(grep -EHni 'modernc.org/sqlite|mattn/go-sqlite3|better-sqlite3|\"sqlite3\"|gem .sqlite3.|microsoft.data.sqlite|aiosqlite' go.mod requirements*.txt pyproject.toml package.json Gemfile *.csproj 2>/dev/null); [ -z \"$bad\" ] || { echo \"$bad\"; exit 1; }", "occurrences": "lines", "title": "Move application data to the declared database service"}, {"name": "no-db-runtime-files-tracked", "expects": "no database or WAL/journal files are committed", "severity": "high", "evaluator": "sh", "sh": "BAD=$(find . -name '*.db' -o -name '*.sqlite' -o -name '*.sqlite3' -o -name '*.db-shm' -o -name '*.db-wal' -o -name '*.db-journal' 2>/dev/null | grep -v '^./.forge' || true); [ -z \"$BAD\" ] || { echo \"$BAD\"; exit 1; }", "occurrences": "lines", "title": "Remove database runtime files from version control"}, {"name": "state-behind-needs", "expects": "everything a deployed app must keep is in a declared need; dataDir holds only caches and scratch; a tool or library the forge does not run passes", "severity": "note", "evaluator": "llm", "scope": "tree", "files": [".forge/app.yaml", "go.mod", "requirements*.txt", "pyproject.toml", "package.json", "Gemfile", "**/settings.py", "config/database.yml", "**/*.go", "**/*.py", "**/*.rb", "**/*.ts", "**/*.js"], "rubric": "State a deployed app must keep across restarts and releases (records, uploads, sessions that matter) lives behind a need declared in .forge/app.yaml (postgres, redis, s3) and reached through the injected connection env. A dataDir, if declared, holds only caches or scratch the app can rebuild. A violation is durable state a deployed part writes to local files (a database file, an uploads folder, a JSON store) instead of a need; cite the file and line. An app with no persistence at all passes. A repository with no app part in .forge/app.yaml (a command-line tool or a library the forge does not run) passes: there is no deployment to keep state for, and the files such a tool writes for the person running it (a folder they name, a folder under their home directory) are theirs, not the app's.", "title": "Declare the services that hold application state"}]
---
State the app must keep lives in a networked need, declared in
`.forge/app.yaml` and fulfilled by each environment: an emulator in
previews, a managed instance in the cloud, a service with a volume on a
host the institution runs. The app reads the connection from the injected
env (`DATABASE_URL`, `REDIS_URL`, the `AWS_ENDPOINT_URL_S3` family) and
never hard-wires a host.

Postgres is the default need for records; Redis for caches and queues
where a second process reads them; `s3` for uploads and files. A database
that is a file on disk is not persistence the forge can promise: the
deployment targets it prefers have no disk. `dataDir` is for caches and
scratch the app can rebuild, nothing the app must keep.

The forge makes the database and the bucket before the app starts; the
app never creates them. Migrations need only the database's owner: no
`CREATE ROLE`, `CREATE DATABASE` or untrusted extension. The app's user is
an ordinary owner on every target, and these are refused; only the
forge's tests and agent steps make it a superuser, for test runners.
Tests that need a declared need run whenever its connection env is set
(`DATABASE_URL`, `REDIS_URL`): the forge starts the need beside its tests and
agent steps and sets it, so gate such tests on that env alone, never on an
extra opt-in switch, which the tests check would only see as skipped. Address the bucket path-style, and serve objects through the
app: the forge gives them no public address.

Rationale: one shape of persistence that runs everywhere the forge
deploys, and a need is something the forge can provision, back up and
receipt; a file is not. An app that keeps its records in a SQLite file
moves them to the `postgres` need.
