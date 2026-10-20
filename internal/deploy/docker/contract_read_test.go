package docker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReadAppContract(t *testing.T) {
	t.Parallel()
	// Only .apptown/app.yaml supplies the runtime; absence is ErrNoContract.
	if _, err := ReadWorkspaceContract(t.TempDir()); err != ErrNoContract {
		t.Fatalf("missing contract must be ErrNoContract, got %v", err)
	}
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".apptown/app.json"), []byte(`{"static":"."}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadWorkspaceContract(repo); err != ErrNoContract {
		t.Fatalf("retired file must not supply a runtime: %v", err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".apptown/app.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadWorkspaceContract(repo); err == nil || err == ErrNoContract {
		t.Fatalf("unreadable native declaration must stay an error: %v", err)
	}
	if _, err := ReadContractAt(context.Background(), repo, "missing"); err == nil || err == ErrNoContract {
		t.Fatalf("unreadable Git source must stay an error: %v", err)
	}
}
