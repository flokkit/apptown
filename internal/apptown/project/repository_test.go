package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestRepositoryLocationUsesImmutableIdentityWithoutFilesystemEffects(t *testing.T) {
	root := t.TempDir()
	if path, err := (RepositoryRef{Slug: "valid"}).Path(root); err == nil || path != "" {
		t.Fatalf("missing identity resolved: %q %v", path, err)
	}
	for _, id := range []ids.ID{idstest.N(1), idstest.N(2)} {
		for _, slug := range []string{"existing-address", "renamed", "../neighbor", ""} {
			path, err := (RepositoryRef{ID: id, Slug: slug}).Path(root)
			if err != nil || path != filepath.Join(root, "workspaces", "project-"+id.String()) {
				t.Fatalf("address influenced identity path: %q %v", path, err)
			}
		}
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("lookup changed files: %v %v", entries, err)
	}
}
