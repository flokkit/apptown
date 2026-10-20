package agentenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy/docker"
)

func TestRuntimeInstructionsNameTheNativeContract(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".apptown"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".apptown", "app.yaml"), []byte("app: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := docker.ReadWorkspaceContract(ws); err == docker.ErrNoContract {
		t.Fatal("documented contract not recognized")
	}
	if _, err := docker.ReadWorkspaceContract(t.TempDir()); err != docker.ErrNoContract {
		t.Fatalf("empty workspace: %v", err)
	}
	if !strings.Contains(RuntimeContractInstructions, ".apptown/app.yaml") {
		t.Fatal("instructions lost the native contract path")
	}
}
