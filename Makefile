.PHONY: build web go release upgrade-test run dev-api dev-web clean check check-go-gate check-go check-go-source check-vet check-staticcheck check-test check-web check-web-tsc check-web-build check-web-lint check-web-test check-shape check-exec check-sqlnow check-lint check-lint-test lint-tools vuln-tools check-reachability check-migrations check-race check-vuln tools hooks check-test-root-a check-test-root-b precheck check-test-affected check-journeys check-pins check-docs tour

GO_TEST_FLAGS ?=
SQLC ?= sqlc
SQLC_VERSION := v1.28.0
GOLANGCI_LINT ?= golangci-lint
GOLANGCI_LINT_VERSION := v2.13.2
# Analyzers read the compiler's export data, so a tool built for another Go
# release can fail on this one: pin them here, for local and CI alike.
STATICCHECK_VERSION := v0.8.1
GOVULNCHECK_VERSION := v1.7.0
DEADCODE_VERSION := v0.50.0
.PHONY: sqlc-tools check-sqlc generate-sql check-generated

sqlc-tools:
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
	go install golang.org/x/tools/cmd/deadcode@$(DEADCODE_VERSION)

lint-tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

check-sqlc:
	@test "$$($(SQLC) version)" = "$(SQLC_VERSION)" || { echo "sqlc $(SQLC_VERSION) required: run make sqlc-tools"; exit 1; }

generate-sql: check-sqlc
	python3 scripts/schema-assemble.py
	$(SQLC) generate

check-generated: check-sqlc
	python3 scripts/schema-assemble.py --check
	$(SQLC) diff

build: web go            ## build frontend then the single forged binary
.DEFAULT_GOAL := build

# `make check` mirrors what CI's unit+web stages gate on, so a red pipeline is
# caught here instead of after a push. Install `make hooks` to run it on push.
# It runs the FAST gates (gofmt, vet, staticcheck, test, tsc, build). The
# slower ones — the race detector and the vulnerability scan — run by hand
# (`make check-race` / `make check-vuln`); the public CI is
# .github/workflows/ci.yml.
# Vite replaces web/dist, which Go embeds, so the Vite build finishes before
# anything compiles Go. Every other stage then runs beside go test, each its
# own make target with its own log (scripts/gate.py, the GATE_*_STAGES lists): the gate is
# as long as go test, not the sum of the stages. Nothing is skipped; the gate
# fails if any stage fails, and prints that stage's log.
GATE_GO_STAGES = check-test check-test-root-a check-test-root-b check-go-source check-vet check-staticcheck \
	check-shape check-exec check-sqlnow check-lint-test check-lint \
	check-reachability check-pins check-docs
GATE_WEB_STAGES = check-web-tsc check-web-lint check-web-test
# Stages that run on a developer's machine and not in CI: landing, where the
# migration lock is held to origin/main's.
GATE_LOCAL_STAGES = check-migrations
# check-gate tests the gate's stopping with signals of its own, so it runs
# last and alone: no other stage can be running when it sends one.
# The web build would take minutes before gate.py refuses a gate demoted by
# macOS Game Mode, so the refusal runs first (scripts/gate.py, "Demotion"):
# recipe lines, which run in order even under make -j, not prerequisites.
check:                   ## run the fast CI gates locally
	@python3 scripts/gate.py --refuse-if-throttled
	@$(MAKE) --no-print-directory check-web-build
	@MAKE='$(MAKE)' python3 scripts/gate.py $(GATE_GO_STAGES) $(GATE_WEB_STAGES) $(GATE_LOCAL_STAGES)
	@echo "== check-gate (alone, after the other stages) =="
	@$(MAKE) --no-print-directory check-gate

# precheck: what a branch should pass before it asks for the gate. The gate's
# fast stages that need no test database, and the tests of every package the
# branch's changes can affect (scripts/affected.py), concurrently. It saves a
# failed gate cycle; the gate's full run stays the witness. BASE is what the
# branch will merge onto.
BASE ?= origin/main
PRECHECK_STAGES = check-pins check-docs check-go-source check-vet check-staticcheck check-lint check-shape check-sqlnow check-exec check-reachability check-migrations check-web-tsc check-web-lint check-web-test check-test-affected check-journeys check-sql
precheck:               ## the fast gates and the tests of affected packages, before the gate
	@MAKE='$(MAKE)' python3 scripts/gate.py $(PRECHECK_STAGES)

# The maintainers' own stages and commands (their deploys, demo, lab, nightly
# runner and the public export), which append to the stage lists above. The
# file is not in the published repository, and nothing public needs it.
-include scripts/maintainer.mk

# The journey tier (build tag journey) runs the user stories end to end. CI
# runs it on the docker-host runner; it takes about a minute, so precheck runs
# it too: a story broken by a change to stock content or a workflow fixture
# shows here, not after landing. (The integration tier takes about ten minutes
# and stays with CI and explicit runs.)
check-journeys:         ## the journey tier: the user stories end to end (-tags journey)
	GOMAXPROCS=$${GOMAXPROCS:-4} go test -tags journey ./internal/forge -run TestJourney -count=1 $(AFFECTED_TEST_FLAGS) $(GO_TEST_FLAGS)

# The affected tests can be most of the suite (a change to a central package),
# and they share the test database with the gate: on a machine where several
# branches run gates, run precheck through the same queue as the gate. They run
# at reduced parallelism by default (AFFECTED_TEST_FLAGS).
AFFECTED_TEST_FLAGS ?= -p 2
check-test-affected:    ## tests of the packages a branch's changes can affect (scripts/affected.py)
	@pkgs=$$(python3 scripts/affected.py $(BASE)) || { echo "could not tell which packages are affected"; exit 1; }; \
	if [ -z "$$pkgs" ]; then echo "no Go package affected"; exit 0; fi; \
	echo "== go test, $$(echo "$$pkgs" | wc -l | tr -d ' ') affected packages =="; \
	GOMAXPROCS=$${GOMAXPROCS:-4} go test $$pkgs -count=1 $(AFFECTED_TEST_FLAGS) $(GO_TEST_FLAGS)

# CI's unit job: make check without the web stages, which CI's web job runs
# (over that job's build of web/dist), and without the local scripts' tests.
check-go-gate:          ## make check's Go and checker stages (CI's unit job)
	@MAKE='$(MAKE)' python3 scripts/gate.py $(GATE_GO_STAGES)
	@echo "== check-gate (alone, after the other stages) =="
	@$(MAKE) --no-print-directory check-gate

check-pins:             ## the tools the forge runs for projects have one version each: deploy/tool-pins.txt
	python3 scripts/pins-check_test.py
	python3 scripts/pins-check.py

check-docs:             ## current guides state the current rule: no dated names, status lines or links into plans and reviews; the tour is complete
	python3 scripts/docs-current_test.py
	python3 scripts/docs-current.py
	python3 scripts/tour_test.py
	python3 scripts/tour-check.py

# The UI tour (site/tour): a small example team's AppTown, photographed in
# both themes by Playwright (FORGE_PLAYWRIGHT_MODULE, default the nightly
# tier's install: scripts/nightly-tiers.sh prepare). Local only, not CI; run
# it and commit site/tour before tagging a release (release.sh checks).
tour: web                ## the UI tour's screenshots and page, from site/tour/tour.yaml; commit site/tour before a release
	go test -tags browser ./internal/forge -run '^TestTour$$' -count=1
	@if command -v oxipng >/dev/null 2>&1; then oxipng -q -o 4 --strip safe site/tour/*.png; else echo "tour: oxipng not on PATH; the shots are not compressed (brew install oxipng)"; fi
	python3 scripts/tour-stamp.py write
	python3 scripts/tour-check.py

check-migrations:       ## a released migration keeps its number and statements: the lock only grows from origin/main's
	sh scripts/migrations-append-only.sh

check-reachability:     ## what forged's main can never reach, against scripts/reachability-baseline
	sh scripts/reachability-check.sh

check-exec:             ## process starts outside their owners, against scripts/exec-baseline.json
	python3 scripts/exec-check_test.py
	go run scripts/exec-check.go

check-sqlnow:           ## SQL reading the database's clock fails (frozen migrations aside; scripts/sqlnow-baseline.json records them)
	python3 scripts/sqlnow-check_test.py
	go run scripts/sqlnow-check.go

check-lint:             ## house rules (forbidigo, .golangci.yml) against scripts/lint-baseline.json
	@test "v$$($(GOLANGCI_LINT) version --short 2>/dev/null)" = "$(GOLANGCI_LINT_VERSION)" || { echo "golangci-lint $(GOLANGCI_LINT_VERSION) required: run make lint-tools"; exit 1; }
	go run scripts/lint-check.go -lint $(GOLANGCI_LINT)

check-lint-test:        ## the lint rules and their count check, on fixtures
	GOLANGCI_LINT=$(GOLANGCI_LINT) python3 scripts/lint-check_test.py

check-shape:            ## the architecture's shape rules against scripts/shape-baseline.json
	python3 scripts/shape-check_test.py
	python3 scripts/release_test.py
	python3 scripts/third_party_notices_test.py
	python3 scripts/stranger_install_test.py
	python3 scripts/apptown_start_test.py
	python3 scripts/site_try_test.py
	python3 scripts/forge_recovery_rehearsal_test.py
	cd eval/companies && python3 run_test.py
	python3 scripts/shape-check.py

# check-go and check-web run their parts one after another; `make check`
# runs the same parts concurrently.
check-go: check-go-source check-vet check-staticcheck check-test check-test-root-a check-test-root-b

check-go-source: check-generated check-schema-ownership
	@echo "== gofmt (the exact CI gate) =="
	@fmt=$$(gofmt -l internal cmd); if [ -n "$$fmt" ]; then \
		echo "not gofmt-clean:"; echo "$$fmt"; echo "fix: gofmt -w internal cmd"; exit 1; fi

check-vet:
	@echo "== go vet =="
	go vet ./...

check-staticcheck:
	@echo "== staticcheck (correctness; config in staticcheck.conf) =="
	@staticcheck -version 2>/dev/null | grep -qF "($(STATICCHECK_VERSION:v%=%))" || { echo "staticcheck $(STATICCHECK_VERSION) required: run make tools"; exit 1; }
	staticcheck ./internal/... ./cmd/... ./stock/...

# The root package (internal/forge) runs as two test processes, its tests split
# by name: ROOT_TESTS_A selects one half (-run) and the other half is exactly
# what it does not select (-skip), so every test runs once. On macOS Go starts
# a child process with fork, and frequent forks from a large process slow all
# its other work; root tests start git thousands of times, so one large
# process holding all of them is slower than two smaller ones
# (design/architecture/testing.md).
ROOT_PACKAGE = flokkit.io/forge/internal/forge
ROOT_TESTS_A = ^TestA

check-test:
	@echo "== go test (every package but the root, whose halves are their own stages) =="
	# Start the longest remaining suite early while keeping the complete package set.
	go test ./internal/forge/workflow $$(go list ./... | grep -vx '$(ROOT_PACKAGE)') -count=1 $(GO_TEST_FLAGS)

check-test-root-a:
	@echo "== go test internal/forge, tests matching $(ROOT_TESTS_A) =="
	go test ./internal/forge -run '$(ROOT_TESTS_A)' -count=1 $(GO_TEST_FLAGS)

check-test-root-b:
	@echo "== go test internal/forge, every other test =="
	go test ./internal/forge -skip '$(ROOT_TESTS_A)' -count=1 $(GO_TEST_FLAGS)

check-web: check-web-tsc check-web-build check-web-lint check-web-test

check-web-tsc:
	cd web && npx tsc --noEmit

check-web-build:
	@echo "== web vite build =="
	cd web && npx vite build >/dev/null

check-web-lint:
	cd web && npm run lint

check-web-test:
	cd web && npm test

check-race:              ## the race detector on the default tier (CI runs this)
	# -timeout: the root package alone runs past go test's default 10m under
	# -race on CI's runner (11 Oct).
	go test -race -count=1 -timeout 30m ./internal/... ./cmd/... $(GO_TEST_FLAGS)

check-sql:               ## the silent-DB-write rule (uses local semgrep or docker)
	@if command -v semgrep >/dev/null; then 		semgrep --config .semgrep/no-silent-db-write.yml --error internal/ ; 	else 		echo "semgrep not on PATH — running it via docker"; 		docker run --rm -v "$$PWD:/src" -w /src semgrep/semgrep 			semgrep --config .semgrep/no-silent-db-write.yml --error internal/ ; 	fi

check-vuln:              ## known-CVE scan of deps + the build toolchain (CI runs this)
	@command -v govulncheck >/dev/null || { echo "govulncheck missing — run 'make tools'"; exit 1; }
	govulncheck ./...

tools: sqlc-tools lint-tools vuln-tools ## install the analyzers make check / CI expect
	go install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)

vuln-tools:
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

.PHONY: check-gate
check-gate:              ## the runner of make check's stages, on stub stages (failures, signals, stale results)
	python3 scripts/gate_test.py

hooks:                   ## install in-repo git hooks (pre-push runs `make check`)
	git config core.hooksPath scripts/git-hooks
	@echo "hooks installed (core.hooksPath=scripts/git-hooks); bypass once with 'git push --no-verify'"

web:                     ## the web bundle (installs web/node_modules first when missing)
	@[ -d web/node_modules ] || (cd web && npm ci --no-audit --no-fund)
	cd web && npm run build

# The commit, stamped for builds Go's build information leaves unnamed (a
# build in a git worktree): internal/version.
REVISION_LDFLAGS = -ldflags "-X flokkit.io/forge/internal/version.Revision=$(shell git rev-parse --short=12 HEAD)$(shell git diff --quiet HEAD -- . 2>/dev/null || echo -modified)"

go: console-cli
	go build $(REVISION_LDFLAGS) -o bin/forged ./cmd/forged
	go build $(REVISION_LDFLAGS) -o bin/forge ./cmd/forge
	go build $(REVISION_LDFLAGS) -o bin/forge-worker ./cmd/forge-worker

.PHONY: console-cli
release:                 ## build a release's artifacts from a clean checkout at its tag: make release VERSION=1.0.0
	sh scripts/release.sh $(VERSION)

upgrade-test: web       ## every earlier release's frozen forge upgrades to this commit and keeps its records: run on a release candidate before tagging it (builds web/dist, which Go embeds)
	go test ./internal/forge -run '^TestAnUpgradeKeepsWhatThePreviousReleaseRecorded$$' -count=1
	go test ./internal/forge/dbschema -run '^TestAMigrationOverAReleasesRecordsAppliesOrLeavesThemAlone$$' -count=1

console-cli:             ## build the CLI artifact mounted into Linux containers
	CGO_ENABLED=0 GOOS=linux go build $(REVISION_LDFLAGS) -o bin/forge-linux ./cmd/forge

# Where `make run` and `make dev-api` listen. Open mode (the default) serves
# loopback only. Docker Desktop's host.docker.internal reaches the host's
# loopback, so chats work as is. On native Linux it is the Docker bridge: use
# CONSOLE_ADDR=172.17.0.1:7778 FORGED_FLAGS=-open-on-network (the bridge is
# not loopback, so open mode needs the opt-in), or -auth internal.
ADDR ?= 127.0.0.1:7777
CONSOLE_ADDR ?= 127.0.0.1:7778
FORGED_FLAGS ?=

run: build               ## build everything and serve on $(ADDR)
	./bin/forged -data ./_data -addr $(ADDR) -console-addr $(CONSOLE_ADDR) $(FORGED_FLAGS)

dev-api: console-cli     ## backend only (uses last-built dist for static files)
	go run ./cmd/forged -data ./_data -addr $(ADDR) -console-addr $(CONSOLE_ADDR) -console-cli ./bin/forge-linux $(FORGED_FLAGS)

dev-web:                 ## vite dev server on :5173, proxies /api to :7777
	cd web && npm run dev

clean:
	rm -rf bin web/dist _data

.PHONY: check-schema-ownership
check-schema-ownership:
	python3 scripts/schema-ownership.py
	python3 -m unittest discover -s scripts -p 'schema*_test.py'

# The forge access gate's image, for linux/amd64 (what App Platform runs).
# Building does not publish it: an operator pushes the image to the
# registry the deployment target uses and passes its digest to the forge
# with -restore-gate-image <ref@sha256:...>, so every restored copy runs the
# exact gate that was reviewed.
GATE_IMAGE ?= forge-gate:local
.PHONY: gate-image
gate-image:              ## build the restore gate image (GATE_IMAGE=forge-gate:local)
	docker build --platform linux/amd64 -f cmd/forge-gate/Dockerfile -t $(GATE_IMAGE) .
