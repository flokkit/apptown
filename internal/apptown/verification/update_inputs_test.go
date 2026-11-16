package verification

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		{"LABEL patch", nextup, strings.Replace(nextup, "golang:1.27.3", "golang:1.27.4", 1), true},
		{"LABEL digest", nextup, strings.Replace(nextup, "node:24.22.2-alpine", "node:24.22.2-alpine"+digest, 1), true},
		{"continued RUN hides FROM", "FROM alpine:3.24\nRUN a \\\nFROM node:22", "FROM alpine:3.25\nRUN a \\\nFROM other:22", true},
		{"continued RUN skips comments and blanks", "FROM alpine:3.24\nRUN a \\\n# comment\n\nFROM node:22", "FROM alpine:3.25\nRUN a \\\n# comment\n\nFROM other:22", true},
		{"continuation trailing whitespace", "FROM alpine:3.24\nRUN a \\ \t\nFROM node:22", "FROM alpine:3.25\nRUN a \\ \t\nFROM other:22", true},
		{"escaped backslash before FROM", "FROM alpine:3.24\nRUN echo \\\\\nFROM golang:1.27.3-alpine3.24 AS build\nRUN go version", "FROM alpine:3.24\nRUN echo \\\\\nFROM other:1.27.3-alpine3.24 AS build\nRUN go version", false},
		{"escaped backslash before ARG", "FROM alpine:3.24\nRUN echo \\\\\nARG VALUE=old", "FROM alpine:3.24\nRUN echo \\\\\nARG VALUE=new", false},
		{"multiline ARG", "FROM node:22\nARG VALUE=\\\n old", "FROM node:23\nARG VALUE=\\\n old", false},
		{"backslash in ARG", "FROM node:22\nARG VALUE=a\\b", "FROM node:23\nARG VALUE=a\\b", false},
		{"backslash in FROM", "FROM ${NODE\\NAME}\nRUN echo a", "FROM ${NODE\\NAME}\nRUN echo b", false},
		{"escape directive", "# EsCaPe = \\\nFROM node:22", "# EsCaPe = \\\nFROM node:23", false},
		{"backtick", "FROM node:22\nRUN echo `date`", "FROM node:23\nRUN echo `date`", false},
		{"node slim", "FROM node:22-slim", "FROM node:22.2-slim" + digest, true},
		{"alpine", "FROM alpine:3.22", "FROM alpine:3.23", true},
		{"digest removed", "FROM node:22-alpine" + digest, "FROM node:22-alpine", true},
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
			err := CompareUpdateInputs(map[string]any{"Dockerfile": dockerBaseInputs([]byte(tc.before))}, map[string]any{"Dockerfile": dockerBaseInputs([]byte(tc.after))}, true, false)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%t: %v", tc.allowed, err)
			}
			if err != nil && !strings.Contains(err.Error(), "Dockerfile") {
				t.Fatal(err)
			}
		})
	}
}

func TestUpdateInputsPermitOnlySecurityToolchainPatches(t *testing.T) {
	for _, file := range []string{"mise.toml", ".tool-versions", ".go-version"} {
		for _, after := range []string{"1.27.4", "1.28.0", "1.28", "2.27.3", "1.27.4-rc1"} {
			for _, security := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/security=%t", file, after, security), func(t *testing.T) {
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
					err := CompareUpdateInputs(before, next, true, security)
					if (err == nil) != (security && after == "1.27.4") {
						t.Fatalf("security=%t: %v", security, err)
					}
				})
			}
		}
	}
	if CompareUpdateInputs(map[string]any{".go-version": "1.27"}, map[string]any{".go-version": "1.28"}, true, true) == nil {
		t.Fatal("minor toolchain versions moved")
	}
	if CompareUpdateInputs(map[string]any{"mise.toml:env": map[string]any{"GO": "1.27.3"}}, map[string]any{"mise.toml:env": map[string]any{"GO": "1.27.4"}}, true, true) == nil {
		t.Fatal("env changes passed")
	}
}

func TestBaseImageChoiceGuardsFromVersionMoves(t *testing.T) {
	before := map[string]any{"Dockerfile": dockerBaseInputs([]byte("FROM node:24.1-alpine\n"))}
	after := map[string]any{"Dockerfile": dockerBaseInputs([]byte("FROM node:24.2-alpine\n"))}
	if err := CompareUpdateInputs(before, after, false, false); err == nil {
		t.Fatal("base images off allowed a FROM version move")
	}
	if err := CompareUpdateInputs(before, after, true, false); err != nil {
		t.Fatal(err)
	}
	if err := CompareUpdateInputs(map[string]any{".go-version": "1.27.3"}, map[string]any{".go-version": "1.27.4"}, true, false); err == nil {
		t.Fatal("toolchain patches off allowed a patch")
	}
}
