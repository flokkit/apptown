package verification

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/imagelines"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/imageline"
)

type updateResolver struct {
	answer imagelines.Reading
	err    error
	scope  imageline.Scope
	ref    string
}

func (r *updateResolver) Answer(_ context.Context, ref string, scope imageline.Scope) (imagelines.Reading, error) {
	r.scope = scope
	r.ref = ref
	return r.answer, r.err
}
func TestRoutineImageMoveAcceptsRegistryBuildsBeforeNewest(t *testing.T) {
	t.Parallel()
	digest := "@sha256:" + strings.Repeat("a", 64)
	r := &updateResolver{answer: imagelines.Reading{Answer: imageline.Answer{Tag: "node:24.9-slim", Digest: strings.TrimPrefix(digest, "@")}}}
	for _, next := range []string{"node:24.9-slim" + digest, "node:24.8-slim" + digest, "node:24.9-slim@sha256:" + strings.Repeat("b", 64)} {
		before := map[string]any{"Dockerfile": dockerBaseInputs([]byte("FROM node:24.1-slim" + digest))}
		after := map[string]any{"Dockerfile": dockerBaseInputs([]byte("FROM " + next))}
		err := CompareUpdateInputs(t.Context(), before, after, true, ImageUpdates{Resolver: r, Scope: imageline.Minor})
		if (err == nil) != (!strings.Contains(next, strings.Repeat("b", 64))) {
			t.Fatalf("%s: %v", next, err)
		}
		if r.scope != imageline.Minor {
			t.Fatal(r.scope)
		}
		if err := CompareUpdateInputs(t.Context(), before, after, true, ImageUpdates{Security: true}); err != nil {
			t.Fatal(err)
		}
	}
	r.err = errors.New("registry offline")
	if err := CompareUpdateInputs(t.Context(), map[string]any{"staticServer": "caddy:2.1-alpine" + digest}, map[string]any{"staticServer": "caddy:2.9-alpine" + digest}, true, ImageUpdates{Resolver: r, Scope: imageline.Minor}); err == nil || !strings.Contains(err.Error(), "unknown") || fault.KindOf(err) != fault.Unavailable {
		t.Fatal(err)
	}
}
func TestStaticServerIsAnImageInputThatUpdatesNeverAdd(t *testing.T) {
	t.Parallel()
	digest := "sha256:" + strings.Repeat("a", 64)
	tree := t.TempDir()
	if err := os.Mkdir(filepath.Join(tree, ".apptown"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, ".apptown/app.yaml"), []byte("staticServer: caddy:2.9-alpine@"+digest+"\napp:\n  web: {static: ., public: true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	inputs, err := UpdateInputs(tree)
	if err != nil || inputs["staticServer"] != "caddy:2.9-alpine@"+digest {
		t.Fatalf("static server was not recorded: %v %v", inputs, err)
	}
	// An update moves an existing pin; one that adds a pin,
	// even the registry's own answer, is outside the update's bounds.
	r := &updateResolver{answer: imagelines.Reading{Answer: imageline.Answer{Tag: "caddy:2.9-alpine", Digest: digest}}}
	if err := CompareUpdateInputs(t.Context(), map[string]any{}, map[string]any{"staticServer": r.answer.Image()}, true, ImageUpdates{Resolver: r, Scope: imageline.Minor}); err == nil || !strings.Contains(err.Error(), "never adds") {
		t.Fatalf("an update added the static server pin: %v", err)
	}
}

func (r *updateResolver) Digest(context.Context, string) (string, error) {
	return r.answer.Digest, r.err
}

func TestDeclaredToolchainsBoundImageMovesThroughUpdateInputs(t *testing.T) {
	t.Parallel()
	digest := "@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, file, declaration, old, next, newest, line string
		scope                                            imageline.Scope
		pass                                             bool
	}{
		{"short exact node", "mise.toml", "[tools]\nnode = \"24.22.2\"", "node:24-slim", "node:24.22.2-slim", "node:24.22.2-slim", "node:24.22.2-slim", imageline.Line, true},
		{"short node off line", "mise.toml", "[tools]\nnode = \"24.22.2\"", "node:24-slim", "node:26.13.1-slim", "node:24.22.2-slim", "node:24.22.2-slim", imageline.Line, false},
		{"node before newest", "mise.toml", "[tools]\nnode = \"24\"", "node:24.22.2-alpine", "node:24.22.3-alpine", "node:24.23.0-alpine", "node:24-alpine", imageline.Line, true},
		{"node off line", "mise.toml", "[tools]\nnode = \"24\"", "node:24.22.2-alpine", "node:26.13.1-alpine", "node:24.23.0-alpine", "node:24-alpine", imageline.Line, false},
		{"downgrade", "mise.toml", "[tools]\nnode = \"24\"", "node:24.22.2-alpine", "node:24.22.1-alpine", "node:24.23.0-alpine", "node:24-alpine", imageline.Line, false},
		{"python mise", "mise.toml", "[tools]\npython = \"3.12\"", "python:3.12.8-slim", "python:3.12.9-slim", "python:3.12.10-slim", "python:3.12-slim", imageline.Line, true},
		{"python off line", "mise.toml", "[tools]\npython = \"3.12\"", "python:3.12.8-slim", "python:3.15.2-slim", "python:3.12.10-slim", "python:3.12-slim", imageline.Line, false},
		{"python version file", ".python-version", "3.12\n", "python:3.12.8-slim", "python:3.12.9-slim", "python:3.12.10-slim", "python:3.12-slim", imageline.Line, true},
		{"go exact", "mise.toml", "[tools]\ngo = \"1.27.4\"", "golang:1.27.4-alpine3.24", "golang:1.27.4-alpine3.24", "golang:1.27.4-alpine3.24", "golang:1.27.4-alpine3.24", imageline.Line, true},
		{"go short", ".tool-versions", "go 1.27.4\n", "golang:1.27", "golang:1.27.4", "golang:1.27.4", "golang:1.27.4", imageline.Line, true},
		{"non numeric", "mise.toml", "[tools]\nnode = \"lts\"", "node:24-alpine", "node:26.13.1-alpine", "node:26.13.1-alpine", "node:24-alpine", imageline.Major, true},
		{"no declaration", "", "", "node:24-alpine", "node:26.13.1-alpine", "node:26.13.1-alpine", "node:24-alpine", imageline.Major, true},
		{"coincident version", "mise.toml", "[tools]\npython = \"3.12\"", "rabbitmq:3.12", "rabbitmq:3.12.9", "rabbitmq:3.12.10", "rabbitmq:3.12", imageline.Line, true},
		{"unrelated", "mise.toml", "[tools]\npython = \"3.12\"", "node:24-alpine", "node:26.13.1-alpine", "node:26.13.1-alpine", "node:24-alpine", imageline.Major, true},
		{"longest", "mise.toml", "[tools]\nnode = \"24\"\nother = \"24.22.2\"", "node:24-slim", "node:24.22.2-slim", "node:24.22.2-slim", "node:24.22.2-slim", imageline.Line, true},
		// mise vendor versions declare their major (#124: the stock Java toolchain).
		{"temurin vendor refresh", "mise.toml", "[tools]\njava = \"temurin-21.0.5+11.0.LTS\"", "eclipse-temurin:21-jre-noble", "eclipse-temurin:21-jre-noble", "eclipse-temurin:21-jre-noble", "eclipse-temurin:21-jre-noble", imageline.Line, true},
		{"temurin vendor off line", "mise.toml", "[tools]\njava = \"temurin-21.0.5+11.0.LTS\"", "eclipse-temurin:21-jre-noble", "eclipse-temurin:26-jre-noble", "eclipse-temurin:26-jre-noble", "eclipse-temurin:21-jre-noble", imageline.Line, false},
		{"corretto vendor off line", "mise.toml", "[tools]\njava = \"corretto-21.0.5.11.1\"", "amazoncorretto:21-alpine", "amazoncorretto:26-alpine", "amazoncorretto:26-alpine", "amazoncorretto:21-alpine", imageline.Line, false},
		{"zulu vendor off line", "mise.toml", "[tools]\njava = \"zulu-21.38.21\"", "azul/zulu-openjdk:21-jre", "azul/zulu-openjdk:26-jre", "azul/zulu-openjdk:26-jre", "azul/zulu-openjdk:21-jre", imageline.Line, false},
		{"openjdk vendor off line", ".tool-versions", "java openjdk-21.0.2\n", "openjdk:21-jdk", "openjdk:26-jdk", "openjdk:26-jdk", "openjdk:21-jdk", imageline.Line, false},
		{"multi-word vendor off line", "mise.toml", "[tools]\njava = \"oracle-graalvm-21.0.5\"", "container-registry.oracle.com/graalvm/jdk:21", "container-registry.oracle.com/graalvm/jdk:26", "container-registry.oracle.com/graalvm/jdk:26", "container-registry.oracle.com/graalvm/jdk:21", imageline.Line, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree := t.TempDir()
			write := func(file, value string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(tree, file), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.file != "" {
				write(tc.file, tc.declaration)
			}
			write("Dockerfile", "FROM "+tc.old+digest)
			before, err := UpdateInputs(tree)
			if err != nil {
				t.Fatal(err)
			}
			// A rebuild of an exact tag still changes its digest.
			write("Dockerfile", "FROM "+tc.next+"@sha256:"+strings.Repeat("b", 64))
			after, err := UpdateInputs(tree)
			if err != nil {
				t.Fatal(err)
			}
			r := &updateResolver{answer: imagelines.Reading{Answer: imageline.Answer{Tag: tc.newest, Digest: "sha256:" + strings.Repeat("b", 64)}}}
			levels := []string{"aggressive"}
			if tc.scope == imageline.Line {
				levels = append(levels, "routine")
			}
			for _, level := range levels {
				var readings []imagelines.Reading
				err = CompareUpdateInputs(t.Context(), before, after, true, ImageUpdates{Resolver: r, Scope: UpdateScope(level), Readings: &readings})
				if (err == nil) != tc.pass {
					t.Fatalf("%s pass=%v: %v", level, tc.pass, err)
				}
				if tc.pass {
					got, want := strings.TrimPrefix(r.ref, "index.docker.io/library/"), tc.line
					got, _, _ = strings.Cut(got, "@")
					if got != want || r.scope != tc.scope || len(readings) != 1 {
						t.Fatalf("%s lookup %s/%s readings=%v; want %s/%s", level, got, r.scope, readings, want, tc.scope)
					}
				}
			}
		})
	}
}

// nextup on the demo: the Dockerfile names node:24.22.2-alpine without a
// digest and the update pins the registry's fully qualified answer for the
// same tag; that is a move on the declared line, not off it.
func TestDeclaredLineAcceptsTheQualifiedDigestPinOfAShortTag(t *testing.T) {
	t.Parallel()
	tree := t.TempDir()
	write := func(file, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(tree, file), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("mise.toml", "[tools]\nnode = \"24.22.2\"\ngo = \"1.27.4\"\n")
	write("Dockerfile", "FROM node:24.22.2-alpine AS frontend\n")
	before, err := UpdateInputs(tree)
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("c", 64)
	write("Dockerfile", "FROM index.docker.io/library/node:24.22.2-alpine@"+digest+" AS frontend\n")
	after, err := UpdateInputs(tree)
	if err != nil {
		t.Fatal(err)
	}
	r := &updateResolver{answer: imagelines.Reading{Answer: imageline.Answer{Tag: "index.docker.io/library/node:24.22.2-alpine", Digest: digest}}}
	if err := CompareUpdateInputs(t.Context(), before, after, true, ImageUpdates{Resolver: r, Scope: UpdateScope("aggressive")}); err != nil {
		t.Fatal(err)
	}
}

// Demo complaints of 18 Nov: dp-roundtrip (mise go 1.27.0) could not take
// golang 1.27.4 and tidewell (mise node 24.22.2) could not take
// 24.22.3-alpine. With Toolchain patches on, a declared line is its minor;
// off, it is the exact declaration. The declaration still wins over the
// dependency level, so aggressive never moves a declared toolchain's major,
// and that refusal says what would satisfy it.
func TestToolchainPatchesMoveADeclaredImageWithinItsMinor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, mise, old, next string
		patches, pass         bool
		why                   string
	}{
		{"go patch", `go = "1.27.0"`, "golang:1.27.0", "golang:1.27.4", true, true, ""},
		{"go patch, choice off", `go = "1.27.0"`, "golang:1.27.0", "golang:1.27.4", false, false, "golang:1.27.0 line"},
		{"node patch", `node = "24.22.2"`, "node:24.22.2-alpine", "node:24.22.3-alpine", true, true, ""},
		{"node patch, choice off", `node = "24.22.2"`, "node:24.22.2-alpine", "node:24.22.3-alpine", false, false, "node:24.22.2-alpine line"},
		{"node minor", `node = "24.22.2"`, "node:24.22.2-alpine", "node:24.23.0-alpine", true, false, "node:24.22-alpine line"},
		{"node downgrade", `node = "24.22.2"`, "node:24.22.2-alpine", "node:24.22.1-alpine", true, false, "without a downgrade"},
		{"short tag on declared minor", `go = "1.27"`, "golang:1.27-alpine", "golang:1.27.4-alpine", true, true, ""},
		{"short tag, no declaration", "", "golang:1.27-alpine", "golang:1.27.4-alpine", true, true, ""},
		{"aggressive major beside mise", `node = "24.22.2"`, "node:24-alpine", "node:26.13.1-alpine", true, false, "move the declaration and the image together in a change a person accepts"},
		{"aggressive major, no declaration", "", "node:24-alpine", "node:26.13.1-alpine", true, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree := t.TempDir()
			write := func(file, value string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(tree, file), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.mise != "" {
				write("mise.toml", "[tools]\n"+tc.mise+"\n")
			}
			write("Dockerfile", "FROM "+tc.old+" AS build\n")
			before, err := UpdateInputs(tree)
			if err != nil {
				t.Fatal(err)
			}
			digest := "sha256:" + strings.Repeat("d", 64)
			write("Dockerfile", "FROM "+tc.next+"@"+digest+" AS build\n")
			after, err := UpdateInputs(tree)
			if err != nil {
				t.Fatal(err)
			}
			r := &updateResolver{answer: imagelines.Reading{Answer: imageline.Answer{Tag: tc.next, Digest: digest}}}
			err = CompareUpdateInputs(t.Context(), before, after, true, ImageUpdates{Resolver: r, Scope: UpdateScope("aggressive"), ToolchainPatches: tc.patches})
			if (err == nil) != tc.pass || err != nil && !strings.Contains(err.Error(), tc.why) {
				t.Fatalf("pass=%t: %v", tc.pass, err)
			}
			if tc.pass && tc.mise != "" && r.scope != imageline.Patch {
				t.Fatalf("declared line read at %s, want patch", r.scope)
			}
		})
	}
}
