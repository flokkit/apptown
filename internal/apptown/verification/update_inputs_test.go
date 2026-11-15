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
	digest := "@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, before, after string
		allowed             bool
	}{
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
			err := CompareUpdateInputs(map[string]any{"Dockerfile": dockerBaseInputs([]byte(tc.before))}, map[string]any{"Dockerfile": dockerBaseInputs([]byte(tc.after))}, false)
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
					err := CompareUpdateInputs(before, next, security)
					if (err == nil) != (security && after == "1.27.4") {
						t.Fatalf("security=%t: %v", security, err)
					}
				})
			}
		}
	}
	if CompareUpdateInputs(map[string]any{".go-version": "1.27"}, map[string]any{".go-version": "1.28"}, true) == nil {
		t.Fatal("minor toolchain versions moved")
	}
	if CompareUpdateInputs(map[string]any{"mise.toml:env": map[string]any{"GO": "1.27.3"}}, map[string]any{"mise.toml:env": map[string]any{"GO": "1.27.4"}}, true) == nil {
		t.Fatal("env changes passed")
	}
}
