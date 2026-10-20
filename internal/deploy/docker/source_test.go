package docker

import (
	"os"
	"path/filepath"
	"testing"

	"flokkit.io/forge/internal/deploy"
)

func TestSourceArtifactUsesTheAlreadySelectedDeclaration(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".apptown"), 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ws, ".apptown", "app.yaml")
	if err := os.WriteFile(path, []byte("app:\n  web:\n    image: selected:v1\n    public: true\n    ports: [\"8080\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := ReadSelectedContract(ws)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid: newer-declaration\n"), 0600); err != nil {
		t.Fatal(err)
	}
	art, err := SourceArtifact(deploy.Source{Workspace: ws, Revision: "selected", Slug: "demo", Contract: c}, nil)
	if err != nil || art.Source.Contract.PublicPart().Image != "selected:v1" || art.Source.Revision != "selected" {
		t.Fatalf("selected inputs changed: %+v %v", art, err)
	}
}
