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
}

func (r *updateResolver) Answer(_ context.Context, _ string, scope imageline.Scope) (imagelines.Reading, error) {
	r.scope = scope
	return r.answer, r.err
}
func TestRoutineImageMoveMustEqualTheRegistryTagAndDigest(t *testing.T) {
	t.Parallel()
	digest := "@sha256:" + strings.Repeat("a", 64)
	r := &updateResolver{answer: imagelines.Reading{Answer: imageline.Answer{Tag: "node:24.9-slim", Digest: strings.TrimPrefix(digest, "@")}}}
	for _, next := range []string{"node:24.9-slim" + digest, "node:24.8-slim" + digest, "node:24.9-slim@sha256:" + strings.Repeat("b", 64)} {
		before := map[string]any{"Dockerfile": dockerBaseInputs([]byte("FROM node:24.1-slim" + digest))}
		after := map[string]any{"Dockerfile": dockerBaseInputs([]byte("FROM " + next))}
		err := CompareUpdateInputs(t.Context(), before, after, true, false, ImageUpdates{Resolver: r, Scope: imageline.Minor})
		if (err == nil) != (next == r.answer.Image()) {
			t.Fatalf("%s: %v", next, err)
		}
		if r.scope != imageline.Minor {
			t.Fatal(r.scope)
		}
		if err := CompareUpdateInputs(t.Context(), before, after, true, false, ImageUpdates{Security: true}); err != nil {
			t.Fatal(err)
		}
	}
	r.err = errors.New("registry offline")
	if err := CompareUpdateInputs(t.Context(), map[string]any{"staticServer": "caddy:2.1-alpine" + digest}, map[string]any{"staticServer": "caddy:2.9-alpine" + digest}, true, false, ImageUpdates{Resolver: r, Scope: imageline.Minor}); err == nil || !strings.Contains(err.Error(), "unknown") || fault.KindOf(err) != fault.Unavailable {
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
	if err := CompareUpdateInputs(t.Context(), map[string]any{}, map[string]any{"staticServer": r.answer.Image()}, true, false, ImageUpdates{Resolver: r, Scope: imageline.Minor}); err == nil || !strings.Contains(err.Error(), "never adds") {
		t.Fatalf("an update added the static server pin: %v", err)
	}
}
