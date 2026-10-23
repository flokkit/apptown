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
	"github.com/flokkit/apptown/internal/deploy/appcontract"
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
	evaluator := NewEvaluator(localChecks{}, nil)
	check := sourcedNamed(t, "repo/no-build-artifacts", "no-ignored-files-tracked")

	clean := t.TempDir()
	writeTree(t, clean, map[string]string{".gitignore": "target/\n.apptown/*\n!.apptown/app.yaml\n", "src/Main.java": "class Main {}", ".apptown/app.yaml": "app: {}"})
	results, findings, errs := evaluator.runShChecks(context.Background(), []conformance.DeclaredCheck{check}, clean, "t-ign-clean")
	if len(results) != 1 || results[0].Verdict != workflow.VerdictPass || len(findings) != 0 || errs != 0 {
		t.Fatalf("a tree that honours its own ignore list passes: %+v %+v %d", results, findings, errs)
	}

	bad := t.TempDir()
	writeTree(t, bad, map[string]string{".gitignore": "target/\n.apptown/*\n!.apptown/app.yaml\n", "src/Main.java": "class Main {}",
		"target/surefire-reports/TEST-x.xml": "<xml/>", ".apptown/CHANGE.md": "report", ".apptown/app.yaml": "app: {}"})
	results, findings, errs = evaluator.runShChecks(context.Background(), []conformance.DeclaredCheck{check}, bad, "t-ign-bad")
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
	results, _, errs = evaluator.runShChecks(context.Background(), []conformance.DeclaredCheck{check}, none, "t-ign-none")
	if results[0].Verdict != workflow.VerdictPass || errs != 0 {
		t.Fatalf("with no declaration the check has nothing to say: %+v", results)
	}
}
func TestNoBuildArtifactsTrackedNamesPrograms(t *testing.T) {
	t.Parallel()
	evaluator := NewEvaluator(localChecks{}, nil)
	check := sourcedNamed(t, "repo/no-build-artifacts", "no-build-artifacts-tracked")
	big := strings.Repeat("x", 3<<20)
	data := t.TempDir()
	writeTree(t, data, map[string]string{"assets/map.png": "\x89PNG\r\n" + big, "fixtures/rows.csv": big, "notes.txt": "tiny"})
	if results, _, errs := evaluator.runShChecks(context.Background(), []conformance.DeclaredCheck{check}, data, "t-prog-data"); results[0].Verdict != workflow.VerdictPass || errs != 0 {
		t.Fatalf("data and media of any size pass: %+v", results)
	}
	built := t.TempDir()
	writeTree(t, built, map[string]string{"server": "\x7fELF" + "\x02\x01\x01", "Greeting.class": "\xca\xfe\xba\xbe\x00\x00", "assets/map.png": "\x89PNG" + big})
	results, findings, errs := evaluator.runShChecks(context.Background(), []conformance.DeclaredCheck{check}, built, "t-prog-built")
	if results[0].Verdict != workflow.VerdictFail || errs != 0 || len(findings) != 1 || len(findings[0].Items) != 2 {
		t.Fatalf("an ELF and a class file are named, the image is not: %+v %+v", results, findings)
	}
}
func TestLaneGitignoreChecksNameTheirOwnBuildOutput(t *testing.T) {
	t.Parallel()
	evaluator := NewEvaluator(localChecks{}, nil)
	cases := []struct {
		block string
		good  string
	}{
		{"lang/selection/java-spring-boot", "target/\n"},
		{"lang/selection/rust", "/target\n"},
		{"lang/selection/node-fullstack", "node_modules/\ndist/\n"},
		{"lang/selection/typescript-for-web", "**/node_modules/\ndist/**\n"},
		{"lang/selection/django-fullstack", "*.py[cod]\n"},
		{"lang/selection/dotnet-aspnet", "[Bb]in/\n[Oo]bj/\n"},
		{"lang/selection/rails", "/tmp/*\n/log/*\n"},
	}
	for _, c := range cases {
		check := sourcedNamed(t, c.block, "gitignore-names-build-output")
		good := t.TempDir()
		writeTree(t, good, map[string]string{".gitignore": c.good})
		if results, _, errs := evaluator.runShChecks(context.Background(), []conformance.DeclaredCheck{check}, good, "t-gi-good"); results[0].Verdict != workflow.VerdictPass || errs != 0 {
			t.Fatalf("%s: %q names the build output: %+v", c.block, c.good, results)
		}
		bare := t.TempDir()
		writeTree(t, bare, map[string]string{".gitignore": "*.log\n", "web/package.json": "{}"})
		if results, findings, errs := evaluator.runShChecks(context.Background(), []conformance.DeclaredCheck{check}, bare, "t-gi-bare"); results[0].Verdict != workflow.VerdictFail || errs != 0 || len(findings) != 1 {
			t.Fatalf("%s: an ignore list without it fails: %+v", c.block, results)
		}
		missing := t.TempDir()
		writeTree(t, missing, map[string]string{"web/package.json": "{}"})
		if results, _, _ := evaluator.runShChecks(context.Background(), []conformance.DeclaredCheck{check}, missing, "t-gi-none"); results[0].Verdict != workflow.VerdictFail {
			t.Fatalf("%s: no ignore list at all fails: %+v", c.block, results)
		}
	}
}

// typescript-for-web sits in the Go house style, where a service may have
// no frontend at all: with no package.json there is nothing for Node to
// write, so the check has nothing to judge (go-small failed it, 2026-10).
func TestFrontendIgnoreCheckWaitsForAFrontend(t *testing.T) {
	t.Parallel()
	evaluator := NewEvaluator(localChecks{}, nil)
	check := sourcedNamed(t, "lang/selection/typescript-for-web", "gitignore-names-build-output")
	goOnly := t.TempDir()
	writeTree(t, goOnly, map[string]string{".gitignore": "/app\n", "go.mod": "module app\n", "main.go": "package main\n"})
	if results, _, errs := evaluator.runShChecks(context.Background(), []conformance.DeclaredCheck{check}, goOnly, "t-gi-go"); results[0].Verdict != workflow.VerdictPass || errs != 0 {
		t.Fatalf("a Go service with no frontend passes: %+v", results)
	}
	vendored := t.TempDir()
	writeTree(t, vendored, map[string]string{".gitignore": "/app\n", "tools/node_modules/x/package.json": "{}"})
	if results, _, errs := evaluator.runShChecks(context.Background(), []conformance.DeclaredCheck{check}, vendored, "t-gi-vendored"); results[0].Verdict != workflow.VerdictPass || errs != 0 {
		t.Fatalf("a package.json inside node_modules is not a frontend: %+v", results)
	}
}

func TestRunsAsNonRootReadsTheFinalStage(t *testing.T) {
	t.Parallel()
	evaluator := NewEvaluator(localChecks{}, nil)
	check := sourcedNamed(t, "build/container-image", "runs-as-non-root")
	cases := []struct {
		name, df string
		pass     bool
	}{
		{"no dockerfile", "", true},
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
		if c.df != "" {
			writeTree(t, dir, map[string]string{"Dockerfile": c.df})
		}
		results, _, errs := evaluator.runShChecks(context.Background(), []conformance.DeclaredCheck{check}, dir, "t-user")
		got := results[0].Verdict == workflow.VerdictPass
		if got != c.pass || errs != 0 {
			t.Fatalf("%s: want pass=%v, got %+v", c.name, c.pass, results)
		}
	}
}

// build/container-image: a VOLUME in the image makes Docker create a new
// unnamed volume for every container started from it (checks, previews,
// releases), which AppTown does not back up or move. Only the public part's
// dataDir, where AppTown mounts the app's named data volume, may be one. The
// final stage is read as Docker reads it; app.yaml's dataDir in either form.
func TestNoVolumeOutsideDataDirReadsTheFinalStage(t *testing.T) {
	t.Parallel()
	evaluator := NewEvaluator(localChecks{}, nil)
	check := sourcedNamed(t, "build/container-image", "no-volume-outside-data-dir")
	const blockDataDir = "version: 2\napp:\n  web:\n    build: .\n    public: true\n    dataDir: /data # uploads\n"
	cases := []struct {
		name, df, app string
		pass          bool
		names         string // the path a failure names
	}{
		{"no dockerfile", "", "", true, ""},
		{"no volume", "FROM alpine:3.20\nCMD [\"/app\"]\n", "", true, ""},
		{"a volume, no app.yaml", "FROM alpine:3.20\nVOLUME /data\n", "", false, "VOLUME /data and .apptown/app.yaml declares no dataDir"},
		{"a volume, no dataDir", "FROM alpine:3.20\nVOLUME /data\n", "version: 2\napp:\n  web:\n    build: .\n    public: true\n", false, "declare dataDir: /data"},
		{"a dataDir only in a comment", "FROM alpine:3.20\nVOLUME /data\n", "app:\n  web:\n    build: .\n    # dataDir: /data\n    public: true\n", false, "VOLUME /data"},
		{"the dataDir, JSON form", "FROM alpine:3.20\nVOLUME [\"/data\"]\n", blockDataDir, true, ""},
		{"the dataDir, quoted, trailing slash", "FROM alpine:3.20\nVOLUME /data/\n", "app:\n  web:\n    build: .\n    public: true\n    dataDir: \"/data\"\n", true, ""},
		{"a part in braces that names no dataDir", "FROM alpine:3.20\nVOLUME /data\n", "app:\n  web: { build: ., public: true, environment: { LABEL: 'a # b' } }\n", false, "VOLUME /data and"},
		{"the dataDir and another path", "FROM alpine:3.20\nVOLUME /data /cache\n", blockDataDir, false, "VOLUME /cache, which is not the app's dataDir (/data)"},
		{"a JSON list with the dataDir and another path", "FROM alpine:3.20\nVOLUME [\"/data\", \"/cache\"]\n", blockDataDir, false, "VOLUME /cache, which"},
		{"CRLF, the dataDir", "FROM alpine:3.20\r\nVOLUME [\"/data\"]\r\n", strings.ReplaceAll(blockDataDir, "\n", "\r\n"), true, ""},
		{"the dataDir in a later part", "FROM alpine:3.20\nVOLUME /data\n", "app:\n  worker:\n    build: .\n  web:\n    build: .\n    public: true\n    environment: { A: b }\n    dataDir: /data\n", true, ""},
		// Only the part's own dataDir field counts: the same words in its
		// environment, as a key or inside a value, declare nothing.
		{"dataDir as an environment key", "FROM alpine:3.20\nVOLUME /cache\n", "version: 2\napp:\n  web:\n    build: .\n    public: true\n    environment:\n      dataDir: /cache\n", false, "VOLUME /cache and"},
		{"dataDir inside an environment value", "FROM alpine:3.20\nVOLUME /cache\n", "version: 2\napp:\n  web:\n    build: .\n    public: true\n    environment:\n      CONFIG: |\n        dataDir: /cache\n", false, "VOLUME /cache and"},
		{"a comment inside a continued VOLUME", "FROM alpine:3.20\nVOLUME /data \\\n# the uploads\n    /data/\n", blockDataDir, true, ""},
		{"lower case, CRLF, JSON list", "FROM alpine:3.20\r\nvolume [\"/data\", \"/cache\"]\r\n", strings.ReplaceAll(blockDataDir, "\n", "\r\n"), false, "VOLUME /cache"},
		{"only in a build stage", "FROM golang:1.25 AS build\nVOLUME /cache\nRUN go build\nFROM alpine:3.20\nCOPY --from=build /out /app\n", "", true, ""},
		{"in a stage the final stage starts from", "FROM alpine:3.20 AS base\nVOLUME /cache\nFROM base\nCMD [\"/app\"]\n", "", false, "VOLUME /cache"},
		{"a continued VOLUME", "FROM alpine:3.20\nVOLUME \\\n    /cache\n", "", false, "VOLUME /cache"},
		{"a commented VOLUME", "FROM alpine:3.20\n# VOLUME /cache\n", "", true, ""},
		{"VOLUME in a heredoc body", "FROM alpine:3.20\nCOPY <<EOF /notes\nVOLUME /cache\nEOF\n", "", true, ""},
	}
	for _, c := range cases {
		dir := t.TempDir()
		files := map[string]string{}
		if c.df != "" {
			files["Dockerfile"] = c.df
		}
		if c.app != "" {
			files[".apptown/app.yaml"] = c.app
		}
		if c.app != "" {
			// Every fixture is a contract AppTown accepts.
			if _, err := appcontract.Parse([]byte(c.app)); err != nil {
				t.Fatalf("%s: the fixture is not a valid contract: %v", c.name, err)
			}
		}
		writeTree(t, dir, files)
		results, _, errs := evaluator.runShChecks(context.Background(), []conformance.DeclaredCheck{check}, dir, "t-volume")
		got := results[0].Verdict == workflow.VerdictPass
		if got != c.pass || errs != 0 || !strings.Contains(results[0].Output, c.names) {
			t.Fatalf("%s: want pass=%v naming %q, got %+v", c.name, c.pass, c.names, results)
		}
		if !c.pass && strings.Contains(results[0].Output, "VOLUME /data,") {
			t.Fatalf("%s: the dataDir itself is named as a finding: %+v", c.name, results)
		}
	}
	// A path given by a variable, a contract this reader cannot follow, or a
	// heredoc whose end cannot be found, is not judged: an error, never a
	// pass or a finding.
	for name, tree := range map[string]map[string]string{
		"a variable path":            {"Dockerfile": "FROM alpine:3.20\nENV DATA=/data\nVOLUME $DATA\n", ".apptown/app.yaml": blockDataDir},
		"an app section on one line": {"Dockerfile": "FROM alpine:3.20\nVOLUME /data\n", ".apptown/app.yaml": "app: { web: { build: ., public: true, dataDir: /data } }\n"},
		// The dataDir is read in block form only: a part in braces that
		// names it is not judged, whether the field or a value holds it.
		"a part in braces with a dataDir": {"Dockerfile": "FROM alpine:3.20\nVOLUME /data\n", ".apptown/app.yaml": "app:\n  web: { build: ., public: true, environment: { LABEL: 'a # b' }, dataDir: /data }\n"},
		"dataDir in a braced environment": {"Dockerfile": "FROM alpine:3.20\nVOLUME /cache\n", ".apptown/app.yaml": "app:\n  web: { build: ., public: true, environment: { TEMPLATE_END: '}}', dataDir: /cache, OTHER: x } }\n"},
		"a part in braces across lines":   {"Dockerfile": "FROM alpine:3.20\nVOLUME /data\n", ".apptown/app.yaml": "app:\n  web: { build: ., public: true,\n    dataDir: /data }\n"},
		"an unfinished heredoc":           {"Dockerfile": "FROM alpine:3.20\nRUN echo 'cat <<EOF' > /s.sh\nVOLUME /cache\n"},
		"two heredocs":                    {"Dockerfile": "FROM alpine:3.20\nCOPY <<FIRST <<SECOND /\na\nFIRST\nb\nSECOND\nVOLUME /cache\n"},
	} {
		if app, ok := tree[".apptown/app.yaml"]; ok {
			if _, err := appcontract.Parse([]byte(app)); err != nil {
				t.Fatalf("%s: the fixture is not a valid contract: %v", name, err)
			}
		}
		dir := t.TempDir()
		writeTree(t, dir, tree)
		results, _, errs := evaluator.runShChecks(context.Background(), []conformance.DeclaredCheck{check}, dir, "t-volume-not-judged")
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
// build argument is not judged.
func TestRuntimeStageNotBuildImageReadsTheFinalStage(t *testing.T) {
	t.Parallel()
	evaluator := NewEvaluator(localChecks{}, nil)
	check := sourcedNamed(t, "lang/selection/rust", "runtime-stage-not-build-image")
	cases := []struct {
		name, df string
		pass     bool
	}{
		{"no dockerfile", "", true},
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
		if c.df != "" {
			writeTree(t, dir, map[string]string{"Dockerfile": c.df})
		}
		results, _, errs := evaluator.runShChecks(context.Background(), []conformance.DeclaredCheck{check}, dir, "t-runtime-stage")
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
		writeTree(t, dir, map[string]string{"Dockerfile": df})
		results, _, errs := evaluator.runShChecks(context.Background(), []conformance.DeclaredCheck{check}, dir, "t-runtime-stage-not-judged")
		if errs != 1 || results[0].Verdict == workflow.VerdictPass || results[0].Verdict == workflow.VerdictFail || !strings.Contains(results[0].Output, "not judged") {
			t.Fatalf("%s: %d errors, %+v", name, errs, results)
		}
	}
}

// deploy/health-declared: the check's own message tells authors to write
// health: { readiness: { http: /healthz } }, so that flow style must pass
// as well as the block style (a journey following the advice failed it).
// It is judged per part: a static part is served by AppTown's own file
// server and declares nothing (html-game failed it for that), while a
// built part beside it still must.
func TestReadinessDeclaredAcceptsTheFormItRecommends(t *testing.T) {
	t.Parallel()
	evaluator := NewEvaluator(localChecks{}, nil)
	check := sourcedNamed(t, "deploy/health-declared", "readiness-declared")
	cases := []struct {
		name, contract string
		pass           bool
	}{
		{"no contract", "", true},
		{"nothing served", "app:\n  worker:\n    build: .\n", true},
		{"served, no readiness", "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n", false},
		{"static part: AppTown's file server, nothing to declare", "app:\n  web:\n    static: site\n    public: true\n", true},
		{"static front, built api without readiness", "app:\n  site:\n    static: web\n    public: true\n  api:\n    build: .\n    ports: [\"8080\"]\n", false},
		{"every built part declares", "app:\n    api:\n        build: .\n        ports: [\"8080\"]\n        health: { readiness: { http: /healthz } }\n    site:\n        static: web\n        public: true\nneeds:\n  postgres: { version: \"16\" }\n", true},
		{"block style", "app:\n  web:\n    ports: [\"8080\"]\n    health:\n      readiness:\n        http: /healthz\n", true},
		{"flow style, as recommended", "app:\n  web:\n    static: site\n    public: true\n    health: { readiness: { http: /healthz } }\n", true},
		{"liveness is not readiness", "app:\n  web:\n    public: true\n    health: { liveness: { tcp: 8080 } }\n", false},
	}
	for _, c := range cases {
		dir := t.TempDir()
		if c.contract != "" {
			writeTree(t, dir, map[string]string{".apptown/app.yaml": c.contract})
		}
		results, _, errs := evaluator.runShChecks(context.Background(), []conformance.DeclaredCheck{check}, dir, "t-ready")
		if got := results[0].Verdict == workflow.VerdictPass; got != c.pass || errs != 0 {
			t.Fatalf("%s: want pass=%v, got %+v", c.name, c.pass, results)
		}
	}
}

// The Django and Rails front-door checks: an app behind AppTown's front
// door is told where it is served in APPTOWN_ORIGINS, wildcards included
// (https://*.preview.<domain>). A host list must be built from it with the
// wildcard written in the framework's leading-dot form; reading the
// variable alone lets a raw origin, which matches no Host, pass, and a
// wide-open host list is no trust at all. The lines each guidance block
// recommends must pass.
func TestFrontDoorChecksWantTranslatedOrigins(t *testing.T) {
	t.Parallel()
	evaluator := NewEvaluator(localChecks{}, nil)
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
		{"django: no settings yet", "lang/selection/django-fullstack", "", "", true},
		{"django: the recommended lines", "lang/selection/django-fullstack", "app/settings.py", djangoGood, true},
		{"django: origins never read", "lang/selection/django-fullstack", "app/settings.py", "ALLOWED_HOSTS = []\n", false},
		{"django: every host trusted", "lang/selection/django-fullstack", "app/settings.py", "ALLOWED_HOSTS = ['*']\n" + djangoGood, false},
		{"django: every host trusted without APPTOWN_ORIGINS", "lang/selection/django-fullstack", "app/settings.py",
			strings.Replace(djangoGood, `["localhost", "127.0.0.1"]`, `['*']`, 1), false},
		{"django: a comment naming the wildcard fallback", "lang/selection/django-fullstack", "app/settings.py",
			"# Never fall back to or [\"*\"]: an unset APPTOWN_ORIGINS must not trust every host.\n" + djangoGood, true},
		{"django: origins used untranslated", "lang/selection/django-fullstack", "app/settings.py",
			"ALLOWED_HOSTS = os.environ.get(\"APPTOWN_ORIGINS\", \"\").split(\",\")\n", false},
		{"rails: no app yet", "lang/selection/rails", "", "", true},
		{"rails: the recommended lines", "lang/selection/rails", "config/environments/production.rb", railsGood, true},
		{"rails: origins never read", "lang/selection/rails", "config/environments/production.rb", "Rails.application.configure do\nend\n", false},
		{"rails: hosts cleared", "lang/selection/rails", "config/environments/production.rb", railsGood + "config.hosts.clear\n", false},
		{"rails: origins used untranslated", "lang/selection/rails", "config/environments/production.rb",
			"config.hosts = ENV.fetch(\"APPTOWN_ORIGINS\", \"\").split(\",\")\nconfig.host_authorization = { exclude: ->(r) { r.path == \"/up\" } }\n", false},
		{"rails: probe not excluded", "lang/selection/rails", "config/environments/production.rb",
			"config.hosts = ENV.fetch(\"APPTOWN_ORIGINS\", \"\").split(\",\").map { |o| URI(o).host.sub(/\\A\\*\\./, \".\") }\n", false},
	}
	for _, c := range cases {
		dir := t.TempDir()
		if c.file != "" {
			writeTree(t, dir, map[string]string{c.file: c.body})
		}
		check := sourcedNamed(t, c.block, "trusts-front-door")
		results, _, errs := evaluator.runShChecks(context.Background(), []conformance.DeclaredCheck{check}, dir, "t-front-door")
		if got := results[0].Verdict == workflow.VerdictPass; got != c.pass || errs != 0 {
			t.Fatalf("%s: want pass=%v, got %+v", c.name, c.pass, results)
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
