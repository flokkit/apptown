package console

import (
	"os"
	"path/filepath"
	"testing"
)

// The files a wake stages replace those the last one placed, by the
// inventory it left. An inventory that cannot be read, or a file it names
// that cannot be removed, fails the wake: the file (a provider
// configuration, a credential) would otherwise stay, and drop out of the
// inventory that removes it later.
//
// Kills: an unreadable inventory taken as none; a failed removal ignored.
func TestStagingFailsWhenItCannotRemoveWhatTheLastWakePlaced(t *testing.T) {
	t.Parallel()
	t.Run("unreadable inventory", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := stage(dir, map[string]Staged{"pi/models.json": {Data: []byte(`{"key":"old"}`)}}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, StagedFile), []byte("not an inventory"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := stage(dir, map[string]Staged{}); err == nil {
			t.Fatal("a wake staged over an inventory it could not read")
		}
		if _, err := os.Stat(filepath.Join(dir, "pi/models.json")); err != nil {
			t.Fatalf("the earlier file is gone, so the test proves nothing: %v", err)
		}
		if raw, _ := os.ReadFile(filepath.Join(dir, StagedFile)); string(raw) != "not an inventory" {
			t.Fatalf("the inventory was rewritten without the earlier file: %q", raw)
		}
	})
	t.Run("a removal that fails", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := stage(dir, map[string]Staged{"pi/auth.json": {Data: []byte(`{"token":"old"}`)}}); err != nil {
			t.Fatal(err)
		}
		// A folder with something in it where the file was: removing it fails.
		path := filepath.Join(dir, "pi/auth.json")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(path, "held"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := stage(dir, map[string]Staged{"pi/models.json": {Data: []byte("{}")}}); err == nil {
			t.Fatal("a wake staged though a file the last one placed could not be removed")
		}
	})
	t.Run("no inventory yet", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := stage(dir, map[string]Staged{"pi/models.json": {Data: []byte("{}")}}); err != nil {
			t.Fatalf("a first wake: %v", err)
		}
	})
}
