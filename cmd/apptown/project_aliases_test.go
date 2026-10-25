package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckoutAliasResolvesAfterRename(t *testing.T) {
	t.Setenv("APPTOWN_PROJECT", "")
	dir := filepath.Join(t.TempDir(), "shop")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	fakeAppTown(t, func(method, path string) (int, string) {
		switch path {
		case "/projects":
			return 200, `[{"slug":"renamed"}]`
		case "/projects/shop":
			return 200, `{"id":"00000000-0001-7000-8000-000000000000","slug":"renamed"}`
		default:
			return 404, `{"error":"not found"}`
		}
	})
	if got, err := resolveProject(""); err != nil || (got != "shop" && got != "renamed") {
		t.Fatalf("existing checkout alias no longer resolves: %q, %v", got, err)
	}
}

func TestDeleteAliasConfirmsCanonicalAddress(t *testing.T) {
	typing(t, "renamed\n")
	seen := fakeAppTown(t, func(method, path string) (int, string) {
		if method == "DELETE" {
			return 202, `{"state":"deleting"}`
		}
		switch path {
		case "/projects/shop", "/projects/renamed":
			return 200, `{"id":"00000000-0001-7000-8000-000000000000","slug":"renamed"}`
		case "/projects/shop/deployments", "/projects/renamed/deployments":
			return 200, `[]`
		default:
			return 404, `{"error":"not found"}`
		}
	})
	if _, err := run(t, "delete", "--no-wait", "shop"); err != nil {
		t.Fatalf("deletion through alias did not accept canonical confirmation: %v", err)
	}
	for _, request := range *seen {
		if request.Method == "DELETE" && request.Body["confirm"] != "renamed" {
			t.Fatalf("server requires canonical confirmation, got %v", request.Body)
		}
	}
}
