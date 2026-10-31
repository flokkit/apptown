.PHONY: build web go upgrade-test run dev-api dev-web clean check check-go-gate check-go check-go-source check-vet check-staticcheck check-test check-web check-web-tsc check-web-build check-web-lint check-web-test check-shape check-exec check-container-output check-sqlnow check-lint check-lint-test lint-tools vuln-tools check-reachability check-migrations check-race check-vuln tools hooks check-test-root-a check-test-root-b precheck check-test-affected check-journeys check-pins check-npm-scripts check-build-artifacts check-npm-audit check-docs check-public check-ci-heavy tour

GO_TEST_FLAGS ?=
SQLC ?= sqlc
# renovate: datasource=go depName=github.com/sqlc-dev/sqlc versioning=semver
SQLC_VERSION := v1.28.0
GOLANGCI_LINT ?= golangci-lint
# renovate: datasource=go depName=github.com/golangci/golangci-lint/v2 versioning=semver
GOLANGCI_LINT_VERSION := v2.15.0
# Analyzers read the compiler's export data, so a tool built for another Go
# release can fail on this one: pin them here, for local and CI alike.
# renovate: datasource=go depName=honnef.co/go/tools versioning=semver
STATICCHECK_VERSION := v0.8.2
# renovate: datasource=go depName=golang.org/x/vuln versioning=semver
GOVULNCHECK_VERSION := v1.7.0
# renovate: datasource=go depName=golang.org/x/tools versioning=semver
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

build: web go            ## build frontend then the single apptownd binary
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
	check-shape check-exec check-container-output check-sqlnow check-lint-test check-lint \
	check-reachability check-pins check-npm-scripts check-build-artifacts check-docs check-public check-ci-heavy
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
PRECHECK_STAGES = check-pins check-npm-scripts check-build-artifacts check-docs check-public check-go-source check-vet check-staticcheck check-lint check-shape check-sqlnow check-exec check-container-output check-reachability check-migrations check-web-tsc check-web-lint check-web-test check-test-affected check-journeys check-sql
precheck:               ## the fast gates and the tests of affected packages, before the gate
	@MAKE='$(MAKE)' python3 scripts/gate.py $(PRECHECK_STAGES)

# The maintainers' own stages and commands (their deploys, demo, lab and
# cache maintenance), which append to the stage lists above. The file is in
# their private repository, checked out at private/ when present; nothing
# public needs it.
-include private/scripts/maintainer.mk

# Journeys use the same Linux runner as CI, including its pinned agent image
# build and cleanup. Run precheck through the gate queue on a shared machine.
check-journeys:         ## the journey tier: the user stories end to end (-tags journey)
	CI_HEAVY_ID="$${CI_HEAVY_ID:-journeys-$$$$}" sh scripts/ci-heavy.sh test -tags journey ./internal/apptown -run TestJourney -count=1 $(AFFECTED_TEST_FLAGS) $(GO_TEST_FLAGS)

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

# The public gate (scripts/public-check.py): HEAD's committed tree, as a push
# publishes it, has no private/ path or maintainers' root file, names none of
# the maintainers' home-lab hosts, and gitleaks finds nothing. Needs gitleaks.
check-public:           ## what a push publishes holds nothing private: paths, home-lab names, gitleaks
	python3 scripts/public-check_test.py
	python3 scripts/public-check.py

check-ci-heavy:         ## the heavy tiers' runner: a required test that skipped or did not run fails the job; a release's heavy evidence is a run that passed first time
	python3 scripts/ci_heavy_test.py
	python3 scripts/ci-cache-trim_test.py
	python3 scripts/ci-test-partition_test.py
	python3 scripts/ci_workflows_test.py
	python3 scripts/ci-content-mtimes_test.py
	python3 scripts/ci-test-environment_test.py
	python3 scripts/testinputs-cache_test.py
	python3 scripts/heavy-evidence_test.py

check-pins:             ## the tools the apptown runs for projects have one version each (deploy/tool-pins.txt), and AppTown's own Dockerfiles name every base image by digest
	python3 scripts/pins-check_test.py
	python3 scripts/pins-check.py

check-npm-scripts:      ## no dependency's install script runs in AppTown's own npm installs: web/.npmrc, web/install-scripts.txt
	python3 scripts/npm-scripts-check_test.py
	python3 scripts/npm-scripts-check.py

check-build-artifacts:  ## no compiled program is tracked: the stock check repo/no-build-artifacts (no-build-artifacts-tracked), its own script, over the index
	python3 scripts/build-artifacts-check_test.py
	python3 scripts/build-artifacts-check.py

# Asks the registry's advisory service, so CI runs it (ci.yml's web job) and
# make check, which needs no network, does not.
check-npm-audit:        ## no unexcused high or critical npm advisory in web/ (without dev) or a starter seed (with dev): scripts/npm-audit-exceptions.txt
	python3 scripts/npm-audit-check_test.py
	python3 scripts/npm-audit-check.py

check-docs:             ## current guides state the current rule: no dated names, status lines or links into plans and reviews; shipped guides' and release notes' links work on GitHub and the Docs page; the tour is complete
	python3 scripts/docs-current_test.py
	python3 scripts/docs-current.py
	python3 scripts/docs-links_test.py
	python3 scripts/docs-links.py
	python3 scripts/tour_test.py
	python3 scripts/publish_tour_test.py
	python3 scripts/tour-check.py

# The UI tour (site/tour): a small example team's AppTown, photographed in
# both themes by Playwright (APPTOWN_PLAYWRIGHT_MODULE, default
# ~/.local/share/forge-nightly/playwright, where the maintainers' make
# test-tools installs the version deploy/ci-heavy/Dockerfile pins). The release
# workflow runs it in the ci-heavy image and publishes the shots with the
# site (candidate.yml's tour job); locally it is a preview, and the shots are
# not committed (.gitignore).
tour: web                ## the UI tour's screenshots and page, from site/tour/tour.yaml (a local preview; the release workflow makes the published ones)
	go test -tags browser ./internal/apptown -run '^TestTour$$' -count=1
	@if command -v oxipng >/dev/null 2>&1; then oxipng -q -o 4 --strip safe site/tour/*.png; else echo "tour: oxipng not on PATH; the shots are not compressed (brew install oxipng)"; fi
	python3 scripts/tour-check.py --shots

check-migrations:       ## preserve released migrations above the supported baseline
	python3 scripts/migrations-append-only_test.py
	sh scripts/migrations-append-only.sh

check-reachability: check-stock-embed ## what apptownd's main can never reach, against scripts/reachability-baseline
	python3 scripts/reachability-check_test.py
	sh scripts/reachability-check.sh

# The binaries read their stock from the update files a release ships
# (share/apptown/updates); none may import package stock, whose embedded copy
# is for tests, or a production path could fall back to it.
check-stock-embed:      ## apptownd and apptown-worker do not import package stock
	@if go list -deps ./cmd/apptownd ./cmd/apptown-worker | grep -qx 'github.com/flokkit/apptown/stock'; then \
		echo "apptownd or apptown-worker imports github.com/flokkit/apptown/stock: production reads stock from its shipped update files, never the embedded copy"; exit 1; fi

check-exec:             ## process starts outside their owners, against scripts/exec-baseline.json
	python3 scripts/exec-check_test.py
	go run scripts/exec-check.go

check-container-output: ## reading what a container printed as a result, outside internal/containerresult
	python3 scripts/container-output-check_test.py
	go run scripts/container-output-check.go

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
	python3 scripts/shape-report_test.py
	python3 scripts/release-notes-check_test.py
	python3 scripts/release-prepare_test.py
	python3 scripts/candidate-evidence_test.py
	python3 scripts/pre_push_test.py
	python3 scripts/update-manifest_test.py
	python3 scripts/release-upload_test.py
	python3 scripts/homebrew-formula_test.py
	python3 scripts/third_party_notices_test.py
	python3 scripts/image_build_test.py
	python3 scripts/stranger_install_test.py
	python3 scripts/apptown_start_test.py
	python3 scripts/site_try_test.py
	python3 scripts/apptown_recovery_rehearsal_test.py
	python3 scripts/chat_check_test.py
	python3 scripts/native_install_check_test.py
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

# The root package (internal/apptown) runs as two test processes, its tests split
# by name: ROOT_TESTS_A selects one half (-run) and the other half is exactly
# what it does not select (-skip), so every test runs once. On macOS Go starts
# a child process with fork, and frequent forks from a large process slow all
# its other work; root tests start git thousands of times, so one large
# process holding all of them is slower than two smaller ones
# (design/architecture/testing.md).
ROOT_PACKAGE = github.com/flokkit/apptown/internal/apptown
ROOT_TESTS_A = ^TestA

check-test:
	@echo "== go test (every package but the root, whose halves are their own stages) =="
	# Start the longest remaining suite early while keeping the complete package set.
	go test ./internal/apptown/workflow $$(go list ./... | grep -vx '$(ROOT_PACKAGE)') -count=1 $(GO_TEST_FLAGS)

check-test-root-a:
	@echo "== go test internal/apptown, tests matching $(ROOT_TESTS_A) =="
	go test ./internal/apptown -run '$(ROOT_TESTS_A)' -count=1 $(GO_TEST_FLAGS)

check-test-root-b:
	@echo "== go test internal/apptown, every other test =="
	go test ./internal/apptown -skip '$(ROOT_TESTS_A)' -count=1 $(GO_TEST_FLAGS)

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

# The image CI runs the rule in (a local semgrep, when installed, runs it
# instead); pinned by version and digest.
SEMGREP_IMAGE = semgrep/semgrep:1.178.0@sha256:32e459968daabe7ab86968184a29109b9564aa00392401156f9788452b42786b
check-sql:               ## the silent-DB-write rule (uses local semgrep or docker)
	@if command -v semgrep >/dev/null; then 		semgrep --config .semgrep/no-silent-db-write.yml --error internal/ ; 	else 		echo "semgrep not on PATH — running it via docker"; 		docker run --rm -v "$$PWD:/src" -w /src $(SEMGREP_IMAGE) 			semgrep --config .semgrep/no-silent-db-write.yml --error internal/ ; 	fi

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
REVISION_LDFLAGS = -ldflags "-X github.com/flokkit/apptown/internal/version.Revision=$(shell git rev-parse --short=12 HEAD)$(shell git diff --quiet HEAD -- . 2>/dev/null || echo -modified)"

go: console-cli stock-files
	go build $(REVISION_LDFLAGS) -o bin/apptownd ./cmd/apptownd
	go build $(REVISION_LDFLAGS) -o bin/apptown ./cmd/apptown
	go build $(REVISION_LDFLAGS) -o bin/apptown-worker ./cmd/apptown-worker

.PHONY: console-cli stock-files
stock-files:             ## the update files the binaries read their stock from at start (share/apptown/updates, beside bin/)
	go run $(REVISION_LDFLAGS) scripts/stock-files.go -o share/apptown/updates >/dev/null
upgrade-test: web       ## each frozen release's AppTown (internal/apptown/testdata/upgrade) upgrades to this commit and keeps its records (CI's unit tests run it on every commit; builds web/dist, which Go embeds)
	go test ./internal/apptown -run '^TestAnUpgradeKeepsWhatThePreviousReleaseRecorded$$' -count=1
	go test ./internal/apptown/dbschema -run '^TestAMigrationOverAReleasesRecordsAppliesOrLeavesThemAlone$$' -count=1

console-cli:             ## build the CLI artifact mounted into Linux containers
	CGO_ENABLED=0 GOOS=linux go build $(REVISION_LDFLAGS) -o bin/apptown-linux ./cmd/apptown

# Where `make run` and `make dev-api` listen, and how they sign in. They
# choose open mode (no sign-in; apptownd's own default is internal), which
# serves loopback only. Docker Desktop's host.docker.internal reaches the
# host's loopback, so chats work as is. On native Linux it is the Docker
# bridge: use CONSOLE_ADDR=172.17.0.1:7778 APPTOWND_FLAGS=-open-on-network (the
# bridge is not loopback, so open mode needs the opt-in), or AUTH=internal.
ADDR ?= 127.0.0.1:7777
CONSOLE_ADDR ?= 127.0.0.1:7778
AUTH ?= open
APPTOWND_FLAGS ?=

run: build               ## build everything and serve on $(ADDR)
	./bin/apptownd -data ./_data -addr $(ADDR) -console-addr $(CONSOLE_ADDR) -auth $(AUTH) $(APPTOWND_FLAGS)

dev-api: console-cli stock-files ## backend only (uses last-built dist for static files)
	go run ./cmd/apptownd -shipped share/apptown/updates -data ./_data -addr $(ADDR) -console-addr $(CONSOLE_ADDR) -auth $(AUTH) -console-cli ./bin/apptown-linux $(APPTOWND_FLAGS)

dev-web:                 ## vite dev server on :5173, proxies /api to :7777
	cd web && npm run dev

clean:
	rm -rf bin web/dist _data

.PHONY: check-schema-ownership
check-schema-ownership:
	python3 scripts/schema-ownership.py
	python3 -m unittest discover -s scripts -p 'schema*_test.py'

# AppTown access gate's image, for linux/amd64 (what App Platform runs).
# Building does not publish it: an operator pushes the image to the
# registry the deployment target uses and passes its digest to AppTown
# with -restore-gate-image <ref@sha256:...>, so every restored copy runs the
# exact gate that was reviewed.
GATE_IMAGE ?= apptown-gate:local
.PHONY: gate-image
gate-image:              ## build the restore gate image (GATE_IMAGE=apptown-gate:local)
	docker build --platform linux/amd64 -f cmd/apptown-gate/Dockerfile -t $(GATE_IMAGE) .
