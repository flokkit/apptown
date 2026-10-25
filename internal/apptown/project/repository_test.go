package project

import (
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"os"
	"path/filepath"
	"testing"
)

func TestRepositoryLocationRefusesMissingIdentityAndUnsafeAddresses(t *testing.T) {
	for _, ref := range []RepositoryRef{{Slug: "valid"}, {ID: idstest.N(1)}, {ID: idstest.N(1), Slug: "../neighbor"}, {ID: idstest.N(1), Slug: "a/b"}, {ID: idstest.N(1), Slug: "Upper"}} {
		if path, err := ref.Path(t.TempDir()); err == nil || path != "" {
			t.Fatalf("unsafe reference %+v resolved to %q: %v", ref, path, err)
		}
	}
	root := t.TempDir()
	for _, id := range []ids.ID{idstest.N(1), idstest.N(2)} {
		path, err := (RepositoryRef{ID: id, Slug: "existing-address"}).Path(root)
		if err != nil || path != filepath.Join(root, "workspaces", "project-existing-address") {
			t.Fatalf("P1 changed installed layout: %q %v", path, err)
		}
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("location lookup created files: %v %v", entries, err)
	}
}
