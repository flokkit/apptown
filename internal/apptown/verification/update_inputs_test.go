package verification

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/stock"
	"gopkg.in/yaml.v3"
)

// Only external image versions and digests may move. Stage and build inputs
// must survive exactly, including aliases that also name registry images.
func TestUpdateInputsBoundDockerfileOperands(t *testing.T) {
	t.Parallel()
	const nextup = `FROM node:24.22.2-alpine AS frontend
WORKDIR /src/frontend
ARG NPM_CONFIG_REGISTRY
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
COPY openapi.yaml /src/openapi.yaml
RUN npm run build

FROM golang:1.27.3-alpine3.24 AS build
WORKDIR /src
ARG GOPROXY
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /src/frontend/dist ./frontend/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /nextup .

# Distroless has no package manager; update this tag/digest when scans find fixes.
FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="nextup" \
      org.opencontainers.image.description="A private kanban board shared by a person and their assistant."
COPY --from=build /nextup /nextup
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/nextup"]
`
	digest := "@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, before, after string
		allowed             bool
	}{
		{"split FROM keyword", "FROM\\\n alpine:3", "FROM\\\n busybox:3", false},
		{"split ARG keyword", "FROM alpine:3\nARG\\\n VALUE=old", "FROM alpine:3\nARG\\\n VALUE=new", false},
		{"LABEL patch", nextup, strings.Replace(nextup, "golang:1.27.3-alpine3.24", "golang:1.27.4-alpine3.24"+digest, 1), true},
		{"LABEL digest", nextup, strings.Replace(nextup, "node:24.22.2-alpine", "node:24.22.2-alpine"+digest, 1), true},
		{"continued RUN hides FROM", "FROM alpine:3.24\nRUN a \\\nFROM node:22", "FROM alpine:3.25" + digest + "\nRUN a \\\nFROM other:22", true},
		{"continued RUN skips comments and blanks", "FROM alpine:3.24\nRUN a \\\n# comment\n\nFROM node:22", "FROM alpine:3.25" + digest + "\nRUN a \\\n# comment\n\nFROM other:22", true},
		{"continuation trailing whitespace", "FROM alpine:3.24\nRUN a \\ \t\nFROM node:22", "FROM alpine:3.25" + digest + "\nRUN a \\ \t\nFROM other:22", true},
		{"escaped backslash before FROM", "FROM alpine:3.24\nRUN echo \\\\\nFROM golang:1.27.3-alpine3.24 AS build\nRUN go version", "FROM alpine:3.24\nRUN echo \\\\\nFROM other:1.27.3-alpine3.24 AS build\nRUN go version", false},
		{"escaped backslash before ARG", "FROM alpine:3.24\nRUN echo \\\\\nARG VALUE=old", "FROM alpine:3.24\nRUN echo \\\\\nARG VALUE=new", false},
		{"multiline ARG", "FROM node:22\nARG VALUE=\\\n old", "FROM node:23\nARG VALUE=\\\n old", false},
		{"backslash in ARG", "FROM node:22\nARG VALUE=a\\b", "FROM node:23\nARG VALUE=a\\b", false},
		{"backslash in FROM", "FROM ${NODE\\NAME}\nRUN echo a", "FROM ${NODE\\NAME}\nRUN echo b", false},
		{"escape directive", "# EsCaPe = \\\nFROM node:22", "# EsCaPe = \\\nFROM node:23", false},
		{"backtick in RUN, tag moved", "FROM node:22\nRUN echo `date`", "FROM node:23" + digest + "\nRUN echo `date`", true},
		{"backtick in comment, digest added", "# `app` here\nFROM python:3.12-slim", "# `app` here\nFROM python:3.12-slim" + digest, true},
		{"escape directive backtick", "# EsCaPe = `\nFROM node:22", "# EsCaPe = `\nFROM node:23", false},
		{"node slim", "FROM node:22-slim", "FROM node:22.2-slim" + digest, true},
		{"alpine", "FROM alpine:3.22", "FROM alpine:3.23" + digest, true},
		{"same-tag digest refreshed", "FROM eclipse-temurin:21-jre" + digest, "FROM eclipse-temurin:21-jre@sha256:" + strings.Repeat("b", 64), true},
		{"digest removed", "FROM node:22-alpine" + digest, "FROM node:22-alpine", false},
		{"runtime tag", "FROM gcr.io/distroless/static:nonroot", "FROM gcr.io/distroless/static:nonroot" + digest, true},
		{"repository", "FROM node:22-slim", "FROM nodejs:22-slim", false},
		{"variant", "FROM node:20-slim", "FROM node:22-alpine", false},
		{"alpine variant", "FROM node:22-alpine3.21", "FROM node:22-alpine3.22", false},
		{"flags", "FROM --platform=linux/amd64 node:22", "FROM --platform=linux/arm64 node:22", false},
		{"alias", "FROM node:22 AS build", "FROM node:22 AS other", false},
		{"count", "FROM node:22", "FROM node:22\nFROM scratch", false},
		{"arg order", "ARG NODE=22\nFROM ${NODE}", "FROM ${NODE}\nARG NODE=22", false},
		{"arg", "ARG NODE=22\nFROM ${NODE}", "ARG NODE=23\nFROM ${NODE}", false},
		{"variable", "FROM ${NODE}", "FROM ${OTHER}", false},
		{"scratch", "FROM scratch", "FROM alpine:3", false},
		{"stage becomes registry", "FROM node:22-slim AS node\nFROM node AS runtime", "FROM node:22-slim AS node\nFROM node" + digest + " AS runtime", false},
		{"external with stage", "FROM node:22-slim AS node\nFROM node AS runtime", "FROM node:22-slim" + digest + " AS node\nFROM node AS runtime", true},
		{"complex", "FROM node:22 \\\n AS build\nRUN echo a", "FROM node:22 \\\n AS build\nRUN echo b", false},
		{"control byte", "FROM node:22\nRUN echo \x7fa", "FROM node:22\nRUN echo \x7fb", false},
		{"heredoc", "FROM node:22\nRUN <<EOF\na", "FROM node:23\nRUN <<EOF\na", false},
		{"directive", "# syntax=docker/dockerfile:1\nFROM node:22", "# syntax=docker/dockerfile:2\nFROM node:22", false},
	} {
		t.Run(tc.name, func(t *testing.T) {

			err := CompareUpdateInputs(t.Context(), map[string]any{"Dockerfile": dockerBaseInputs([]byte(tc.before))}, map[string]any{"Dockerfile": dockerBaseInputs([]byte(tc.after))}, true, ImageUpdates{Security: true})
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%t: %v", tc.allowed, err)
			}
			if err != nil && !strings.HasPrefix(err.Error(), "Changes the Dockerfile beyond image versions; your settings allow only image version changes.") {
				t.Fatal(err)
			}
		})
	}
}

func TestUpdateInputsPermitToolchainPatchesByChoice(t *testing.T) {
	for _, file := range []string{"mise.toml", ".tool-versions", ".go-version"} {
		for _, after := range []string{"1.27.4", "1.28.0", "1.28", "2.27.3", "1.27.4-rc1"} {
			for _, patches := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/patches=%t", file, after, patches), func(t *testing.T) {
					tree := t.TempDir()
					put := func(version string) map[string]any {
						raw := version + "\n"
						if file == "mise.toml" {
							raw = "[tools]\ngo = \"" + version + "\"\n[env]\nUNCHANGED = \"yes\"\n"
						}
						if file == ".tool-versions" {
							raw = "go " + raw
						}
						if err := os.WriteFile(filepath.Join(tree, file), []byte(raw), 0600); err != nil {
							t.Fatal(err)
						}
						inputs, err := UpdateInputs(tree)
						if err != nil {
							t.Fatal(err)
						}
						return inputs
					}
					before := put("1.27.3")
					next := put(after)
					err := CompareUpdateInputs(t.Context(), before, next, true, ImageUpdates{ToolchainPatches: patches})
					if (err == nil) != (patches && after == "1.27.4") {
						t.Fatalf("patches=%t: %v", patches, err)
					}
				})
			}
		}
	}
	if CompareUpdateInputs(t.Context(), map[string]any{".go-version": "1.27"}, map[string]any{".go-version": "1.28"}, true, ImageUpdates{ToolchainPatches: true, Security: true}) == nil {
		t.Fatal("minor toolchain versions moved")
	}
	if CompareUpdateInputs(t.Context(), map[string]any{"mise.toml:env": map[string]any{"GO": "1.27.3"}}, map[string]any{"mise.toml:env": map[string]any{"GO": "1.27.4"}}, true, ImageUpdates{ToolchainPatches: true, Security: true}) == nil {
		t.Fatal("env changes passed")
	}
}

func TestBaseImageChoiceGuardsFromVersionMoves(t *testing.T) {
	before := map[string]any{"Dockerfile": dockerBaseInputs([]byte("FROM node:24.1-alpine\n"))}
	after := map[string]any{"Dockerfile": dockerBaseInputs([]byte("FROM node:24.2-alpine@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"))}
	if err := CompareUpdateInputs(t.Context(), before, after, false, ImageUpdates{Security: true}); err == nil || !strings.HasPrefix(err.Error(), "Moves a base image; base image updates are turned off.") {
		t.Fatal("base images off allowed a FROM version move")
	}
	if err := CompareUpdateInputs(t.Context(), before, after, true, ImageUpdates{Security: true}); err != nil {
		t.Fatal(err)
	}
	if err := CompareUpdateInputs(t.Context(), map[string]any{".go-version": "1.27.3"}, map[string]any{".go-version": "1.27.4"}, true, ImageUpdates{Security: true}); err == nil || !strings.HasPrefix(err.Error(), "Changes the toolchain version or build inputs; your settings do not allow this change.") {
		t.Fatal("toolchain patches off allowed a patch")
	}
}

// The deterministic boundary does not decide everything about a dependency
// update. Manifest toolchain declarations (go.mod's toolchain line) are not
// extracted, and a security fix's image moves and toolchain patches are only
// checked for repository, variant, digest and minor. The shipped rubrics must
// keep those questions with the judge, and only janitor-update may tell it
// that AppTown already checked its image lines.
func TestRubricsJudgeWhatTheUpdateBoundaryLeavesOpen(t *testing.T) {
	tree := t.TempDir()
	inputs := func(gomod string) map[string]any {
		if err := os.WriteFile(filepath.Join(tree, "go.mod"), []byte(gomod), 0600); err != nil {
			t.Fatal(err)
		}
		in, err := UpdateInputs(tree)
		if err != nil {
			t.Fatal(err)
		}
		return in
	}
	before := inputs("module example\n\ngo 1.27\n\ntoolchain go1.27.3\n")
	after := inputs("module example\n\ngo 1.27\n\ntoolchain go1.28.1\n")
	if err := CompareUpdateInputs(t.Context(), before, after, true, ImageUpdates{}); err != nil {
		t.Fatalf("the boundary now reads go.mod's toolchain line; revisit the rubrics: %v", err)
	}
	node := func(tag string) map[string]any {
		return map[string]any{"Dockerfile": dockerBaseInputs([]byte("FROM " + tag + "\n"))}
	}
	if err := CompareUpdateInputs(t.Context(), node("node:24.1-alpine"), node("node:26.13.1-alpine@sha256:"+strings.Repeat("a", 64)), true, ImageUpdates{ToolchainPatches: true, Security: true}); err != nil {
		t.Fatalf("a security image move is now checked for its line; revisit the fix rubric: %v", err)
	}
	if err := CompareUpdateInputs(t.Context(), map[string]any{".go-version": "1.27.3"}, map[string]any{".go-version": "1.27.4"}, true, ImageUpdates{ToolchainPatches: true, Security: true}); err != nil {
		t.Fatalf("a security toolchain patch is refused without its target: %v", err)
	}

	rubric := func(name string) string {
		raw, err := stock.FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Spec struct {
				Stages []struct {
					Steps []struct {
						Uses string `yaml:"uses"`
						With struct {
							Rubric string `yaml:"rubric"`
						} `yaml:"with"`
					} `yaml:"steps"`
				} `yaml:"stages"`
			} `yaml:"spec"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		for _, stage := range doc.Spec.Stages {
			for _, step := range stage.Steps {
				if step.Uses == "routine" {
					return strings.Join(strings.Fields(step.With.Rubric), " ")
				}
			}
		}
		t.Fatalf("%s has no routine rubric", name)
		return ""
	}
	update, fix := rubric("janitor-update.yaml"), rubric("janitor-fix.yaml")
	// The fly-q2 judge read golang:1.27-alpine → 1.27.4-alpine as a toolchain
	// change (18 Nov); the dp-roundtrip judge had no toolchain-patch exception.
	for _, want := range []string{"Toolchain versions stay unchanged, including toolchain declarations in manifests such as go.mod, except, when the facts say toolchain patches are on, a patch release within the same minor", "do not judge those image lines", "A FROM tag that names a toolchain (golang, node, python) is such an image move, not a toolchain change"} {
		if !strings.Contains(update, want) {
			t.Errorf("janitor-update rubric lost %q", want)
		}
	}
	for _, want := range []string{"is the smallest release that clears a named image finding", "including declarations in manifests such as go.mod", "when a named finding is in that toolchain and it is the smallest patch that fixes it"} {
		if !strings.Contains(fix, want) {
			t.Errorf("janitor-fix rubric lost %q", want)
		}
	}
	if strings.Contains(fix, "do not judge") {
		t.Error("janitor-fix rubric tells the judge to skip what AppTown does not check")
	}
	// AppTown's stable-direct-* steps decide pre-releases from the package
	// manager's own list of direct dependencies; the judge saw only a diff
	// and refused Pocket Planet's indirect @emnapi beta (18 Nov). Overrides
	// stay with the judge.
	for name, text := range map[string]string{"janitor-update": update, "janitor-fix": fix} {
		for _, gone := range []string{"never a pre-release for a direct dependency", "declared range requires it"} {
			if strings.Contains(text, gone) {
				t.Errorf("%s rubric judges direct and indirect pre-releases again (%q)", name, gone)
			}
		}
		for _, want := range []string{"never an override or resolution that pins a pre-release", "AppTown itself checks npm, uv, Go module and Bundler projects", "With any other package manager, a direct dependency never moves to a pre-release"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s rubric lost %q", name, want)
			}
		}
	}
	for name, text := range map[string]string{"janitor-update": update, "janitor-fix": fix} {
		for _, gone := range []string{"final message", "agent's report", "A reference to a file is not a report", "nothing can move"} {
			if strings.Contains(text, gone) {
				t.Errorf("%s rubric judges the agent's account again (%q)", name, gone)
			}
		}
	}
}
