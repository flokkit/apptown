package verification

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

func TestUpdateInputMovesKeepToolchainsBeforeImages(t *testing.T) {
	t.Parallel()
	before := map[string]any{"mise.toml": map[string]any{"node": "24.1.0"}, "Dockerfile": dockerBaseInputs([]byte("FROM caddy:2.10.1\n")), ".apptown/app.yaml": map[string]any{"/app/api/image": "redis:7.4.0"}}
	after := map[string]any{"mise.toml": map[string]any{"node": "24.1.1"}, "Dockerfile": dockerBaseInputs([]byte("FROM caddy:2.10.2\n")), ".apptown/app.yaml": map[string]any{"/app/api/image": "redis:7.4.1"}}
	got := UpdateInputMoves(before, after)
	want := []workflow.PackageMove{{Ecosystem: "toolchain", Package: "node", From: "24.1.0", To: "24.1.1"}, {Ecosystem: "docker", Package: "redis", From: "redis:7.4.0", To: "redis:7.4.1"}, {Ecosystem: "docker", Package: "caddy", From: "caddy:2.10.1", To: "caddy:2.10.2"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("observed moves: %+v", got)
	}
	if got := UpdateInputMoves(before, before); len(got) != 0 {
		t.Fatal(got)
	}
}

// A structured mise tool ([tools.node] with a version key and other options)
// is a toolchain the policy already lets move; its move is recorded too.
func TestUpdateInputMovesReadStructuredToolVersions(t *testing.T) {
	t.Parallel()
	inputs := func(version string) map[string]any {
		t.Helper()
		tree := t.TempDir()
		if err := os.WriteFile(filepath.Join(tree, "mise.toml"), []byte("[tools.node]\nversion = \""+version+"\"\npostinstall = \"corepack enable\"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		facts, err := UpdateInputs(tree)
		if err != nil {
			t.Fatal(err)
		}
		return facts
	}
	before, after := inputs("24.1.0"), inputs("24.1.1")
	if err := CompareUpdateInputs(t.Context(), before, after, false, ImageUpdates{ToolchainPatches: true}); err != nil {
		t.Fatalf("toolchain patch refused: %v", err)
	}
	got := UpdateInputMoves(before, after)
	want := []workflow.PackageMove{{Ecosystem: "toolchain", Package: "node", From: "24.1.0", To: "24.1.1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("moves: %+v", got)
	}
}
