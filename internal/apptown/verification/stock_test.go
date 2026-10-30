package verification

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/stock"
)

type localChecks struct{ Effects }

func (localChecks) Run(ctx context.Context, req execution.Request) execution.Outcome {
	return agentenv.Execute(execution.Runner{}, context.Background(), "local", req, time.Now())
}
func stockChecksOf(t *testing.T, block string) []conformance.CheckDeclaration {
	t.Helper()
	items, _ := wow.ReadStockGuidance(stock.FS, nil)
	for _, it := range items {
		if it.Name == block {
			var cs []conformance.CheckDeclaration
			if err := json.Unmarshal([]byte(it.ChecksJSON), &cs); err != nil {
				t.Fatal(err)
			}
			return cs
		}
	}
	t.Fatalf("no stock block %s", block)
	return nil
}
func sourcedNamed(t *testing.T, block, name string) conformance.DeclaredCheck {
	t.Helper()
	for _, c := range stockChecksOf(t, block) {
		if c.Name == name {
			return conformance.DeclaredCheck{Block: block, Version: 1, Check: c}
		}
	}
	t.Fatalf("no check %s in %s", name, block)
	return conformance.DeclaredCheck{}
}
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
func TestNoIgnoredFilesTrackedAsksTheTreeItself(t *testing.T) {
	t.Parallel()
	evaluator := NewEvaluator(localChecks{}, nil, stock.Versions())
	check := sourcedNamed(t, "repo/no-build-artifacts", "no-ignored-files-tracked")

	clean := t.TempDir()
	writeTree(t, clean, map[string]string{".gitignore": "target/\n.apptown/*\n!.apptown/app.yaml\n", "src/Main.java": "class Main {}", ".apptown/app.yaml": "app: {}"})
	results, findings, errs := evaluator.runShChecks(context.Background(), stock.Images().Sh, []conformance.DeclaredCheck{check}, clean, "t-ign-clean")
	if len(results) != 1 || results[0].Verdict != workflow.VerdictPass || len(findings) != 0 || errs != 0 {
		t.Fatalf("a tree that honours its own ignore list passes: %+v %+v %d", results, findings, errs)
	}

	bad := t.TempDir()
	writeTree(t, bad, map[string]string{".gitignore": "target/\n.apptown/*\n!.apptown/app.yaml\n", "src/Main.java": "class Main {}",
		"target/surefire-reports/TEST-x.xml": "<xml/>", ".apptown/CHANGE.md": "report", ".apptown/app.yaml": "app: {}"})
	results, findings, errs = evaluator.runShChecks(context.Background(), stock.Images().Sh, []conformance.DeclaredCheck{check}, bad, "t-ign-bad")
	if len(results) != 1 || results[0].Verdict != workflow.VerdictFail || errs != 0 || len(findings) != 1 {
		t.Fatalf("a force-added ignored file fails: %+v %+v %d", results, findings, errs)
	}
	got := strings.Join(findings[0].Items, ",")
	if !strings.Contains(got, "target/surefire-reports/TEST-x.xml") || !strings.Contains(got, ".apptown/CHANGE.md") || strings.Contains(got, "app.yaml") || strings.Contains(got, "Main.java") {
		t.Fatalf("the occurrences name the ignored files and only them: %v", findings[0].Items)
	}

	// No ignore list at all: nothing is declared, nothing is refused.
	none := t.TempDir()
	writeTree(t, none, map[string]string{"target/x.class": "x", "README.md": "hi"})
	results, _, errs = evaluator.runShChecks(context.Background(), stock.Images().Sh, []conformance.DeclaredCheck{check}, none, "t-ign-none")
	if results[0].Verdict != workflow.VerdictPass || errs != 0 {
		t.Fatalf("with no declaration the check has nothing to say: %+v", results)
	}
}
func TestNoBuildArtifactsTrackedNamesPrograms(t *testing.T) {
	t.Parallel()
	evaluator := NewEvaluator(localChecks{}, nil, stock.Versions())
	check := sourcedNamed(t, "repo/no-build-artifacts", "no-build-artifacts-tracked")
	big := strings.Repeat("x", 3<<20)
	data := t.TempDir()
	writeTree(t, data, map[string]string{"assets/map.png": "\x89PNG\r\n" + big, "fixtures/rows.csv": big, "notes.txt": "tiny"})
	if results, _, errs := evaluator.runShChecks(context.Background(), stock.Images().Sh, []conformance.DeclaredCheck{check}, data, "t-prog-data"); results[0].Verdict != workflow.VerdictPass || errs != 0 {
		t.Fatalf("data and media of any size pass: %+v", results)
	}
	built := t.TempDir()
	writeTree(t, built, map[string]string{"server": "\x7fELF" + "\x02\x01\x01", "Greeting.class": "\xca\xfe\xba\xbe\x00\x00", "assets/map.png": "\x89PNG" + big})
	results, findings, errs := evaluator.runShChecks(context.Background(), stock.Images().Sh, []conformance.DeclaredCheck{check}, built, "t-prog-built")
	if results[0].Verdict != workflow.VerdictFail || errs != 0 || len(findings) != 1 || len(findings[0].Items) != 2 {
		t.Fatalf("an ELF and a class file are named, the image is not: %+v %+v", results, findings)
	}
}

// webContract is the smallest contract a part check applies to: one public
// part, web, built from the Dockerfile at the repository root, naming tools.
func webContract(tools ...string) string {
	s := "version: 2\napp:\n  web:\n    build: .\n    public: true\n    ports: [\"8080\"]\n"
	if len(tools) > 0 {
		s += "    tools: [" + strings.Join(tools, ", ") + "]\n"
	}
	return s
}

// Each lane's gitignore check, once a part names the lane's tool, wants the
// ignore list to cover what that tool writes beside the part's manifest, and
// fails without it; a part naming the tool with no manifest fails in words
// rather than passing with nothing judged.
//
// Kills: a probe path the lane's ignore lines do not cover, a missing
// .gitignore read as a pass, and an absent manifest read as a pass.
func TestLaneGitignoreChecksNameTheirOwnBuildOutput(t *testing.T) {
	t.Parallel()
	evaluator := NewEvaluator(localChecks{}, nil, stock.Versions())
	cases := []struct {
		block, tool, manifest, good string
		names                       string // how the no-manifest failure names it
	}{
		{"lang/selection/java-spring-boot", "java", "pom.xml", "target/\n", "pom.xml"},
		{"lang/selection/rust", "rust", "Cargo.toml", "/target\n", "Cargo.toml"},
		{"lang/selection/node-fullstack", "node", "package.json", "node_modules/\ndist/\n", "package.json"},
		{"frontend/vite-react", "node", "package.json", "**/node_modules/\ndist/**\n", "package.json"},
		{"lang/selection/django-fullstack", "python", "pyproject.toml", "*.py[cod]\n", "pyproject.toml"},
		{"lang/selection/dotnet-aspnet", "dotnet", "app.csproj", "[Bb]in/\n[Oo]bj/\n", ".csproj"},
		{"lang/selection/rails", "ruby", "Gemfile", "/tmp/*\n/log/*\n", "Gemfile"},
	}
	run := func(check conformance.DeclaredCheck, files map[string]string, name string) (Results, []conformance.Finding, int) {
		dir := t.TempDir()
		writeTree(t, dir, files)
		return evaluator.runShChecks(context.Background(), stock.Images().Sh, []conformance.DeclaredCheck{check}, dir, name)
	}
	for _, c := range cases {
		check := sourcedNamed(t, c.block, "gitignore-names-build-output")
		contract := webContract(c.tool)
		if results, _, errs := run(check, map[string]string{".apptown/app.yaml": contract, c.manifest: "x", ".gitignore": c.good}, "t-gi-good"); results[0].Verdict != workflow.VerdictPass || errs != 0 {
			t.Fatalf("%s: %q names the build output: %+v", c.block, c.good, results)
		}
		if results, findings, errs := run(check, map[string]string{".apptown/app.yaml": contract, c.manifest: "x", ".gitignore": "*.log\n"}, "t-gi-bare"); results[0].Verdict != workflow.VerdictFail || errs != 0 || len(findings) != 1 {
			t.Fatalf("%s: an ignore list without it fails: %+v", c.block, results)
		}
		if results, _, _ := run(check, map[string]string{".apptown/app.yaml": contract, c.manifest: "x"}, "t-gi-none"); results[0].Verdict != workflow.VerdictFail {
			t.Fatalf("%s: no ignore list at all fails: %+v", c.block, results)
		}
		if results, _, _ := run(check, map[string]string{".apptown/app.yaml": contract, ".gitignore": c.good}, "t-gi-nomanifest"); results[0].Verdict != workflow.VerdictFail || !strings.Contains(results[0].Output, "there is no "+c.names) {
			t.Fatalf("%s: a part built with %s and no %s fails saying so: %+v", c.block, c.tool, c.manifest, results)
		}
	}
}

// frontend/vite-react is taken beside a backend, which may have no
// frontend at all (a Go service failed it, 2026-10). Applicability, not the
// script, now decides that: a part that names only go is not judged; a part
// that names node is judged at every package.json at or below its folder,
// against the repository's own ignore list; and a part that names node with
// no package.json fails in words.
//
// Kills: the check running for a Go-only part, probing a monorepo part's
// output at the repository root instead of under its folder, a negated
// keep-file line read as uncovering the whole dist folder, and a package.json
// inside node_modules counted as the part's manifest.
func TestFrontendIgnoreCheckWaitsForAFrontend(t *testing.T) {
	t.Parallel()
	evaluator := NewEvaluator(localChecks{}, nil, stock.Versions())
	check := sourcedNamed(t, "frontend/vite-react", "gitignore-names-build-output")
	run := func(files map[string]string, name string) Results {
		dir := t.TempDir()
		writeTree(t, dir, files)
		results, _, _ := evaluator.runShChecks(context.Background(), stock.Images().Sh, []conformance.DeclaredCheck{check}, dir, name)
		return results
	}
	if results := run(map[string]string{".apptown/app.yaml": webContract("go"), ".gitignore": "/app\n", "go.mod": "module app\n", "main.go": "package main\n"}, "t-gi-go"); results[0].Verdict != workflow.VerdictNotApplicable {
		t.Fatalf("a Go-only part is not judged by the frontend check: %+v", results)
	}
	if results := run(map[string]string{".apptown/app.yaml": webContract("go", "node"), "go.mod": "module app\n", "frontend/package.json": "{}",
		".gitignore": "/app\nfrontend/node_modules/\nfrontend/dist/*\n!frontend/dist/.keep\n"}, "t-gi-go-node"); results[0].Verdict != workflow.VerdictPass {
		t.Fatalf("a root part naming go and node, its frontend's output ignored, passes: %+v", results)
	}
	monorepo := "version: 2\napp:\n  web:\n    build: web\n    public: true\n    ports: [\"8080\"]\n    tools: [node]\n"
	if results := run(map[string]string{".apptown/app.yaml": monorepo, "web/package.json": "{}", ".gitignore": "web/node_modules/\nweb/dist/\n"}, "t-gi-monorepo"); results[0].Verdict != workflow.VerdictPass {
		t.Fatalf("a node part in web/ whose root ignore list covers web/node_modules and web/dist passes: %+v", results)
	}
	if results := run(map[string]string{".apptown/app.yaml": webContract("node"), ".gitignore": "/app\n", "tools/node_modules/x/package.json": "{}"}, "t-gi-vendored"); results[0].Verdict != workflow.VerdictFail || !strings.Contains(results[0].Output, "there is no package.json") {
		t.Fatalf("a node part with no package.json of its own (one inside node_modules does not count) fails saying so: %+v", results)
	}
}

// buildCheckNeedsItsDockerfile: a check on a part built from a Dockerfile is
// not judged when no part is declared, and once one is, a missing Dockerfile
// is a failure that says so, never a pass.
func buildCheckNeedsItsDockerfile(t *testing.T, evaluator *Evaluator, check conformance.DeclaredCheck, tools ...string) {
	t.Helper()
	none := t.TempDir()
	writeTree(t, none, map[string]string{"README.md": "hi"})
	if results, _, errs := evaluator.runShChecks(context.Background(), stock.Images().Sh, []conformance.DeclaredCheck{check}, none, "t-df-nocontract"); results[0].Verdict != workflow.VerdictNotApplicable || errs != 0 {
		t.Fatalf("%s with no contract is not applicable: %+v", check.Check.Name, results)
	}
	missing := t.TempDir()
	writeTree(t, missing, map[string]string{".apptown/app.yaml": webContract(tools...)})
	if results, _, errs := evaluator.runShChecks(context.Background(), stock.Images().Sh, []conformance.DeclaredCheck{check}, missing, "t-df-missing"); results[0].Verdict != workflow.VerdictFail || errs != 0 || !strings.Contains(results[0].Output, "no Dockerfile") {
		t.Fatalf("%s with a built part and no Dockerfile fails saying so: %+v", check.Check.Name, results)
	}
}

// runs-as-non-root judges the final stage's USER of a built part's
// Dockerfile; a built part with no Dockerfile fails in words.
//
// Kills: a USER in a build stage credited to the runtime, root runnable
// scratch stages passed as files only, and a missing Dockerfile passed.
func TestRunsAsNonRootReadsTheFinalStage(t *testing.T) {
	t.Parallel()
	evaluator := NewEvaluator(localChecks{}, nil, stock.Versions())
	check := sourcedNamed(t, "build/container-image", "runs-as-non-root")
	buildCheckNeedsItsDockerfile(t, evaluator, check)
	cases := []struct {
		name, df string
		pass     bool
	}{
		{"single stage, app user", "FROM node:20-alpine\nUSER node\nCMD [\"node\"]\n", true},
		{"single stage, no user", "FROM node:20-alpine\nCMD [\"node\"]\n", false},
		{"explicit root", "FROM alpine:3.20\nUSER app\nUSER root\n", false},
		{"multi-stage, only the build stage dropped", "FROM golang:1.25 AS build\nUSER build\nRUN go build\nFROM alpine:3.20\nCOPY --from=build /out /app\n", false},
		{"multi-stage, runtime stage dropped", "FROM golang:1.25 AS build\nRUN go build\nFROM alpine:3.20\nuser 10001:10001\nCMD [\"/app\"]\n", true},
		// A final stage of files only (a static site's build output) runs nothing.
		{"a stage of files only", "FROM node:24-alpine AS build\nRUN npm run build\n\nFROM scratch AS site\n# the built files\nCOPY --from=build /app/dist /dist\n", true},
		// Each runnable scratch stage below is root and must not pass as files only
		// (the first two build and run as uid 0).
		{"scratch with a continued CMD", "FROM scratch\nCOPY --from=busybox:1.37 / /\nCMD\\\n [\"/bin/busybox\", \"id\"]\n", false},
		{"a heredoc that reads like a stage", "FROM busybox:1.37\nCOPY <<EOF /note\nFROM scratch\nEOF\nCMD [\"id\"]\n", false},
		{"a heredoc that reads like a stage of files", "FROM busybox:1.37\nCOPY <<COPY /note\nFROM scratch\nCOPY\n", false},
		{"scratch with a command", "FROM scratch\nCOPY app /app\nENTRYPOINT [\"/app\"]\n", false},
		{"scratch with an escape directive", "# escape=`\nFROM scratch\nCOPY app /app\nCMD`\n [\"/app\"]\n", false},
	}
	for _, c := range cases {
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{".apptown/app.yaml": webContract(), "Dockerfile": c.df})
		results, _, errs := evaluator.runShChecks(context.Background(), stock.Images().Sh, []conformance.DeclaredCheck{check}, dir, "t-user")
		got := results[0].Verdict == workflow.VerdictPass
		if got != c.pass || errs != 0 {
			t.Fatalf("%s: want pass=%v, got %+v", c.name, c.pass, results)
		}
	}
}

// build/container-image: AppTown mounts an app's data folder itself, so any
// VOLUME in the image, even at the dataDir, only makes Docker create a new
// unnamed volume in every container started without that mount. The final
// stage is read as Docker reads it; app.yaml grants nothing. A built part
// with no Dockerfile fails in words. What a run reports is prefixed with the
// part it judged.
//
// Kills: a VOLUME at the dataDir excused, a build stage's VOLUME charged to
// the runtime, a heredoc's body read as instructions, and a missing
// Dockerfile passed.
func TestNoVolumeReadsTheFinalStage(t *testing.T) {
	t.Parallel()
	evaluator := NewEvaluator(localChecks{}, nil, stock.Versions())
	check := sourcedNamed(t, "build/container-image", "no-volume")
	buildCheckNeedsItsDockerfile(t, evaluator, check)
	const dataDir = "version: 2\napp:\n  web:\n    build: .\n    public: true\n    dataDir: /data\n"
	cases := []struct {
		name, df string
		names    []string // the paths a failure names, one line each
	}{
		{"no volume", "FROM alpine:3.20\nCMD [\"/app\"]\n", nil},
		{"a volume", "FROM alpine:3.20\nVOLUME /data\n", []string{"/data"}},
		{"JSON form, two paths", "FROM alpine:3.20\nVOLUME [\"/data\", \"/cache\"]\n", []string{"/data", "/cache"}},
		{"shell form, two paths, trailing slash", "FROM alpine:3.20\nVOLUME /data/ /cache\n", []string{"/data", "/cache"}},
		{"a variable path", "FROM alpine:3.20\nENV DATA=/data\nVOLUME $DATA\n", []string{"$DATA"}},
		{"lower case, CRLF", "FROM alpine:3.20\r\nvolume [\"/data\"]\r\n", []string{"/data"}},
		{"only in a build stage", "FROM golang:1.25 AS build\nVOLUME /cache\nRUN go build\nFROM alpine:3.20\nCOPY --from=build /out /app\n", nil},
		{"in a stage the final stage starts from", "FROM alpine:3.20 AS base\nVOLUME /cache\nFROM base\nCMD [\"/app\"]\n", []string{"/cache"}},
		{"a continued VOLUME", "FROM alpine:3.20\nVOLUME \\\n    /cache\n", []string{"/cache"}},
		{"a comment inside a continued VOLUME", "FROM alpine:3.20\nVOLUME /data \\\n# the uploads\n    /cache\n", []string{"/data", "/cache"}},
		{"a commented VOLUME", "FROM alpine:3.20\n# VOLUME /cache\n", nil},
		{"VOLUME in a heredoc body", "FROM alpine:3.20\nCOPY <<EOF /notes\nVOLUME /cache\nEOF\n", nil},
	}
	for _, c := range cases {
		dir := t.TempDir()
		// The app declares /data as its dataDir; the check fails a VOLUME
		// there all the same.
		writeTree(t, dir, map[string]string{".apptown/app.yaml": dataDir, "Dockerfile": c.df})
		results, _, errs := evaluator.runShChecks(context.Background(), stock.Images().Sh, []conformance.DeclaredCheck{check}, dir, "t-volume")
		pass := results[0].Verdict == workflow.VerdictPass
		if pass != (len(c.names) == 0) || errs != 0 || len(results[0].Occurrences) != len(c.names) {
			t.Fatalf("%s: want %v named, got %+v", c.name, c.names, results)
		}
		for i, path := range c.names {
			if !strings.HasPrefix(results[0].Occurrences[i], "web: the Dockerfile declares VOLUME "+path+". Remove the VOLUME line.") {
				t.Fatalf("%s: occurrence %d does not name %s: %q", c.name, i, path, results[0].Occurrences[i])
			}
		}
	}
	// A heredoc whose end cannot be found, or two in one instruction, is not
	// judged: an error, never a pass or a finding.
	for name, df := range map[string]string{
		"an unfinished heredoc": "FROM alpine:3.20\nRUN echo 'cat <<EOF' > /s.sh\nVOLUME /cache\n",
		"two heredocs":          "FROM alpine:3.20\nCOPY <<FIRST <<SECOND /\na\nFIRST\nb\nSECOND\nVOLUME /cache\n",
	} {
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{".apptown/app.yaml": dataDir, "Dockerfile": df})
		results, _, errs := evaluator.runShChecks(context.Background(), stock.Images().Sh, []conformance.DeclaredCheck{check}, dir, "t-volume-not-judged")
		if errs != 1 || results[0].Verdict == workflow.VerdictPass || results[0].Verdict == workflow.VerdictFail || !strings.Contains(results[0].Output, "not judged") {
			t.Fatalf("%s: %d errors, %+v", name, errs, results)
		}
	}
}

// Each compiled stack names the image its seed builds with, and its
// runtime-stage-not-build-image check refuses that image as the final
// stage's base (hello-rust shipped rust:1.84.1 whole: 1.47 GB, 964 fixable
// high or critical findings). The final stage is read through stage names,
// platform flags, digests, the Docker Hub prefix, continued lines, CRLF
// endings, comments and heredocs, as Docker reads them; a base given by a
// build argument is not judged. The check applies to a part built with
// rust; such a part with no Dockerfile fails in words.
//
// Kills: the final stage misread through any of those forms, and a missing
// Dockerfile passed.
func TestRuntimeStageNotBuildImageReadsTheFinalStage(t *testing.T) {
	t.Parallel()
	evaluator := NewEvaluator(localChecks{}, nil, stock.Versions())
	check := sourcedNamed(t, "lang/selection/rust", "runtime-stage-not-build-image")
	buildCheckNeedsItsDockerfile(t, evaluator, check, "rust")
	cases := []struct {
		name, df string
		pass     bool
	}{
		{"single stage on the build image", "FROM rust:1.84.1\nRUN cargo build --release\nCMD [\"/src/target/release/app\"]\n", false},
		{"build stage, then a runtime stage", "FROM rust:1.84 AS build\nRUN cargo install --path . --root /out\nFROM debian:bookworm-slim\nCOPY --from=build /out/bin/ /usr/local/bin/\n", true},
		{"distroless runtime", "FROM rust:1.84 AS build\nRUN cargo build\nFROM gcr.io/distroless/cc-debian12:nonroot\nCOPY --from=build /src/target/release/app /app\n", true},
		{"final stage named after the build stage", "FROM rust:1.84 AS build\nRUN cargo build\nFROM build AS run\nCMD [\"/app\"]\n", false},
		{"lower-case keywords", "from rust:1.84 as Build\nrun cargo build\nfrom build\n", false},
		{"platform flag and digest", "FROM --platform=linux/amd64 rust@sha256:0123\nCMD [\"/app\"]\n", false},
		{"Docker Hub prefix", "FROM docker.io/library/rust:1.84-slim\nCMD [\"/app\"]\n", false},
		{"the build image only in an earlier stage", "FROM debian:bookworm-slim AS base\nFROM rust:1.84 AS build\nFROM base\nCOPY --from=build /out /app\n", true},
		{"a base from a build argument", "ARG BASE=rust:1.84\nFROM ${BASE}\nCMD [\"/app\"]\n", true},
		// Docker joins continued lines and reads CRLF files; so does the check.
		{"a continued FROM", "FROM --platform=linux/amd64 \\\n    rust:1.84\nCMD [\"/app\"]\n", false},
		{"a continued stage name", "FROM rust:1.84 \\\n    AS build\nFROM build AS runtime\nCMD [\"/app\"]\n", false},
		{"CRLF stage names", "FROM rust:1.84 AS build\r\nFROM build AS runtime\r\nCMD [\"/app\"]\r\n", false},
		// Whole-line comments are not instructions and do not continue one;
		// heredoc bodies are file content; a final continued line still counts.
		{"a commented continued line before the runtime", "FROM rust:1.84 AS build\n# RUN build \\\nFROM debian:bookworm-slim AS runtime\n", true},
		{"a commented continued line before the build image", "FROM debian:bookworm-slim AS base\n# RUN build \\\nFROM rust:1.84 AS runtime\n", false},
		{"a comment inside a continued FROM", "FROM --platform=linux/amd64 \\\n# pinned toolchain\n    rust:1.84 AS runtime\n", false},
		{"a continued FROM at the end of the file", "FROM rust:1.84 AS runtime \\", false},
		{"a heredoc with FROM in its body", "FROM rust:1.84 AS runtime\nCOPY <<SQL /query.sql\ncount rows\nFROM users;\nSQL\n", false},
		{"a quoted, indented heredoc naming the build image", "FROM debian:bookworm-slim\nRUN <<-\"EOT\" sh\n\techo\n\tFROM rust:1.84\n\tEOT\nCMD [\"/x\"]\n", true},
		{"an indented heredoc ends at its tab-indented delimiter", "FROM rust:1.84 AS build\nFROM debian:bookworm-slim\nRUN <<-EOT sh\n\techo\n\tEOT\nFROM build\n", false},
		{"a spaced, quoted heredoc with FROM in its body", "FROM rust:1.84 AS runtime\nRUN cat << 'SQL' > /query.sql\ncount rows\nFROM users;\nSQL\n", false},
		{"a punctuated heredoc delimiter, then the runtime", "FROM rust:1.84 AS build\nRUN <<END-SCRIPT\necho hello\nEND-SCRIPT\nFROM debian:bookworm-slim AS runtime\n", true},
		{"a punctuated heredoc delimiter, then the build image", "FROM debian:bookworm-slim AS base\nRUN <<END-SCRIPT\necho hello\nEND-SCRIPT\nFROM rust:1.84 AS runtime\n", false},
		{"a shift and a here-string are not heredocs", "FROM rust:1.84 AS b\nRUN echo $((1<<20)) && cat <<< \"y\"\nFROM debian:bookworm-slim\n", true},
		{"CRLF runtime stage", "FROM rust:1.84 AS build\r\nRUN cargo build \\\r\n  --release\r\nFROM debian:bookworm-slim\r\nCOPY --from=build /app /app\r\n", true},
	}
	for _, c := range cases {
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{".apptown/app.yaml": webContract("rust"), "Dockerfile": c.df})
		results, _, errs := evaluator.runShChecks(context.Background(), stock.Images().Sh, []conformance.DeclaredCheck{check}, dir, "t-runtime-stage")
		got := results[0].Verdict == workflow.VerdictPass
		if got != c.pass || errs != 0 {
			t.Fatalf("%s: want pass=%v, got %+v", c.name, c.pass, results)
		}
	}
	// Where the check cannot establish a heredoc's end (two heredocs in one
	// instruction; heredoc-like text in a quoted string, whose "end" never
	// comes) it says it could not judge, in either stage order, rather than
	// judge an earlier stage.
	for name, df := range map[string]string{
		"two heredocs in one instruction":   "FROM debian:bookworm-slim\nCOPY <<FIRST <<SECOND /\na\nFIRST\nb\nSECOND\nFROM rust:1.84\n",
		"quoted text, then the build image": "FROM debian:bookworm-slim AS build\nRUN echo 'cat <<EOF' > /script.sh\nFROM rust:1.84 AS runtime\n",
		"quoted text, then the runtime":     "FROM rust:1.84 AS build\nRUN echo 'cat <<EOF' > /script.sh\nFROM debian:bookworm-slim AS runtime\n",
	} {
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{".apptown/app.yaml": webContract("rust"), "Dockerfile": df})
		results, _, errs := evaluator.runShChecks(context.Background(), stock.Images().Sh, []conformance.DeclaredCheck{check}, dir, "t-runtime-stage-not-judged")
		if errs != 1 || results[0].Verdict == workflow.VerdictPass || results[0].Verdict == workflow.VerdictFail || !strings.Contains(results[0].Output, "not judged") {
			t.Fatalf("%s: %d errors, %+v", name, errs, results)
		}
	}
}

// deploy/health-declared: the check's own message tells authors to write
// health: { readiness: { http: /healthz } }, so that flow style must pass
// as well as the block style (a journey following the advice failed it).
// It applies to the parts run as containers: a static part is served by
// AppTown's own file server and declares nothing (html-game failed it for
// that), so a contract of static parts alone, or none, is not judged, while a
// built part beside a static one still must declare.
//
// Kills: the flow style refused, liveness taken for readiness, a static
// part's missing probe charged to the app, and a served container part
// without readiness passed.
func TestReadinessDeclaredAcceptsTheFormItRecommends(t *testing.T) {
	t.Parallel()
	evaluator := NewEvaluator(localChecks{}, nil, stock.Versions())
	check := sourcedNamed(t, "deploy/health-declared", "readiness-declared")
	const pass, fail, na = workflow.VerdictPass, workflow.VerdictFail, workflow.VerdictNotApplicable
	cases := []struct {
		name, contract string
		want           workflow.CheckVerdict
	}{
		{"no contract", "", na},
		{"a container part that serves nothing", "app:\n  site:\n    static: web\n    public: true\n  worker:\n    build: .\n", pass},
		{"served, no readiness", "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n", fail},
		{"static part: AppTown's file server, nothing to declare", "app:\n  web:\n    static: site\n    public: true\n", na},
		{"static front, built api without readiness", "app:\n  site:\n    static: web\n    public: true\n  api:\n    build: .\n    ports: [\"8080\"]\n", fail},
		{"every built part declares", "app:\n    api:\n        build: .\n        ports: [\"8080\"]\n        health: { readiness: { http: /healthz } }\n    site:\n        static: web\n        public: true\nneeds:\n  postgres: { version: \"16\" }\n", pass},
		{"block style", "app:\n  web:\n    build: .\n    public: true\n    ports: [\"8080\"]\n    health:\n      readiness:\n        http: /healthz\n", pass},
		{"flow style, as recommended", "app:\n  web:\n    build: .\n    public: true\n    ports: [\"8080\"]\n    health: { readiness: { http: /healthz } }\n", pass},
		{"liveness is not readiness", "app:\n  web:\n    build: .\n    public: true\n    health: { liveness: { tcp: 8080 } }\n", fail},
	}
	for _, c := range cases {
		dir := t.TempDir()
		if c.contract != "" {
			writeTree(t, dir, map[string]string{".apptown/app.yaml": c.contract})
		}
		results, _, errs := evaluator.runShChecks(context.Background(), stock.Images().Sh, []conformance.DeclaredCheck{check}, dir, "t-ready")
		if results[0].Verdict != c.want || errs != 0 {
			t.Fatalf("%s: want %s, got %+v", c.name, c.want, results)
		}
	}
}

// The Django and Rails front-door checks: an app behind AppTown's front
// door is told where it is served in APPTOWN_ORIGINS, wildcards included
// (https://*.preview.<domain>). A host list must be built from it with the
// wildcard written in the framework's leading-dot form; reading the
// variable alone lets a raw origin, which matches no Host, pass, and a
// wide-open host list is no trust at all. The lines each guidance block
// recommends must pass. Each applies to a part built with its framework's
// language: with no contract it is not judged, and a part naming the
// language with no settings file fails in words.
//
// Kills: a raw origin, a wide-open host list or a cleared host list passed,
// the recommended lines refused, and an absent settings file passed.
func TestFrontDoorChecksWantTranslatedOrigins(t *testing.T) {
	t.Parallel()
	evaluator := NewEvaluator(localChecks{}, nil, stock.Versions())
	djangoGood := "import os\nfrom urllib.parse import urlsplit\n" +
		"APPTOWN_ORIGINS = [o for o in os.environ.get(\"APPTOWN_ORIGINS\", \"\").split(\",\") if o]\n" +
		"ALLOWED_HOSTS = [\n    \".\" + h[2:] if h.startswith(\"*.\") else h\n    for h in (urlsplit(o).hostname for o in APPTOWN_ORIGINS)\n] or [\"localhost\", \"127.0.0.1\"]\n" +
		"CSRF_TRUSTED_ORIGINS = APPTOWN_ORIGINS\n"
	railsGood := "Rails.application.configure do\n" +
		"  config.hosts = ENV.fetch(\"APPTOWN_ORIGINS\", \"\").split(\",\").map { |o| URI(o).host.sub(/\\A\\*\\./, \".\") }\n" +
		"  config.host_authorization = { exclude: ->(request) { request.path == \"/up\" } }\nend\n"
	cases := []struct {
		name, block, file, body string
		pass                    bool
	}{
		{"django: no settings yet", "lang/selection/django-fullstack", "", "", false},
		{"django: the recommended lines", "lang/selection/django-fullstack", "app/settings.py", djangoGood, true},
		{"django: origins never read", "lang/selection/django-fullstack", "app/settings.py", "ALLOWED_HOSTS = []\n", false},
		{"django: every host trusted", "lang/selection/django-fullstack", "app/settings.py", "ALLOWED_HOSTS = ['*']\n" + djangoGood, false},
		{"django: every host trusted without APPTOWN_ORIGINS", "lang/selection/django-fullstack", "app/settings.py",
			strings.Replace(djangoGood, `["localhost", "127.0.0.1"]`, `['*']`, 1), false},
		{"django: a comment naming the wildcard fallback", "lang/selection/django-fullstack", "app/settings.py",
			"# Never fall back to or [\"*\"]: an unset APPTOWN_ORIGINS must not trust every host.\n" + djangoGood, true},
		{"django: origins used untranslated", "lang/selection/django-fullstack", "app/settings.py",
			"ALLOWED_HOSTS = os.environ.get(\"APPTOWN_ORIGINS\", \"\").split(\",\")\n", false},
		{"rails: no app yet", "lang/selection/rails", "", "", false},
		{"rails: the recommended lines", "lang/selection/rails", "config/environments/production.rb", railsGood, true},
		{"rails: origins never read", "lang/selection/rails", "config/environments/production.rb", "Rails.application.configure do\nend\n", false},
		{"rails: hosts cleared", "lang/selection/rails", "config/environments/production.rb", railsGood + "config.hosts.clear\n", false},
		{"rails: origins used untranslated", "lang/selection/rails", "config/environments/production.rb",
			"config.hosts = ENV.fetch(\"APPTOWN_ORIGINS\", \"\").split(\",\")\nconfig.host_authorization = { exclude: ->(r) { r.path == \"/up\" } }\n", false},
		{"rails: probe not excluded", "lang/selection/rails", "config/environments/production.rb",
			"config.hosts = ENV.fetch(\"APPTOWN_ORIGINS\", \"\").split(\",\").map { |o| URI(o).host.sub(/\\A\\*\\./, \".\") }\n", false},
	}
	toolOf := map[string]string{"lang/selection/django-fullstack": "python", "lang/selection/rails": "ruby"}
	for block := range toolOf {
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{"README.md": "hi"})
		if results, _, _ := evaluator.runShChecks(context.Background(), stock.Images().Sh, []conformance.DeclaredCheck{sourcedNamed(t, block, "trusts-front-door")}, dir, "t-front-door-na"); results[0].Verdict != workflow.VerdictNotApplicable {
			t.Fatalf("%s with no contract is not judged: %+v", block, results)
		}
	}
	for _, c := range cases {
		dir := t.TempDir()
		files := map[string]string{".apptown/app.yaml": webContract(toolOf[c.block])}
		if c.file != "" {
			files[c.file] = c.body
		}
		writeTree(t, dir, files)
		check := sourcedNamed(t, c.block, "trusts-front-door")
		results, _, errs := evaluator.runShChecks(context.Background(), stock.Images().Sh, []conformance.DeclaredCheck{check}, dir, "t-front-door")
		if got := results[0].Verdict == workflow.VerdictPass; got != c.pass || errs != 0 {
			t.Fatalf("%s: want pass=%v, got %+v", c.name, c.pass, results)
		}
		if c.file == "" && !strings.Contains(results[0].Output, "there is no") {
			t.Fatalf("%s: the failure names the missing file: %+v", c.name, results)
		}
	}
}

// data/networked-database's db-client-has-need runs in a part's folder but
// reads the needs from the repository root's contract: an api part in api/
// whose go.mod requires a postgres driver passes when the root contract
// declares postgres, and fails naming the need when it does not.
//
// Kills: the needs read from the part's folder instead of the root (the
// pass case fails), and the part's manifest not read at all (the fail case
// passes).
func TestDBClientNeedIsReadFromTheRootContract(t *testing.T) {
	t.Parallel()
	evaluator := NewEvaluator(localChecks{}, nil, stock.Versions())
	check := sourcedNamed(t, "data/networked-database", "db-client-has-need")
	const api = "version: 2\napp:\n  api:\n    build: api\n    public: true\n    ports: [\"8080\"]\n    tools: [go]\n"
	gomod := "module api\n\ngo 1.25\n\nrequire github.com/jackc/pgx/v5 v5.7.1\n"
	for _, c := range []struct {
		name, contract string
		want           workflow.CheckVerdict
	}{
		{"the root declares postgres", api + "needs:\n  postgres: {}\n", workflow.VerdictPass},
		{"no postgres need", api, workflow.VerdictFail},
	} {
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{".apptown/app.yaml": c.contract, "api/go.mod": gomod, "api/main.go": "package main\n"})
		results, _, errs := evaluator.runShChecks(context.Background(), stock.Images().Sh, []conformance.DeclaredCheck{check}, dir, "t-db-need")
		if results[0].Verdict != c.want || errs != 0 {
			t.Fatalf("%s: want %s, got %+v", c.name, c.want, results)
		}
		if c.want == workflow.VerdictFail && !strings.Contains(results[0].Output, "declares no postgres need") {
			t.Fatalf("%s: the failure names the need: %+v", c.name, results)
		}
	}
}

func TestStockToolChecksNameStockScanners(t *testing.T) {
	t.Parallel()
	manual := map[string]bool{}
	for _, m := range loadScannerManifests(stock.FS) {
		manual[m.Name] = m.Manual
	}
	items, _ := wow.ReadStockGuidance(stock.FS, nil)
	seen := 0
	for _, it := range items {
		var cs []conformance.CheckDeclaration
		if json.Unmarshal([]byte(it.ChecksJSON), &cs) != nil {
			continue
		}
		for _, c := range cs {
			if c.Evaluator != "tool" {
				continue
			}
			seen++
			if isManual, ok := manual[c.Tool]; !ok {
				t.Errorf("%s · %s names scanner %q, which stock/scanners.yaml does not ship", it.Name, c.Name, c.Tool)
			} else if !isManual {
				t.Errorf("%s · %s names scanner %q, which is not manual and would fan out at every scan point", it.Name, c.Name, c.Tool)
			}
		}
	}
	if seen < 4 {
		t.Fatalf("expected the stock tool checks (django, go, godot, hadolint), saw %d", seen)
	}
}
