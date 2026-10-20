package docker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func selectedSourceRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	repo := t.TempDir()
	for name, body := range files {
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init"}, {"config", "user.name", "Test"}, {"config", "user.email", "test@example.invalid"}, {"add", "."}, {"commit", "-m", "selected source"}} {
		if _, err := gitForDockerTest(repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

func TestDockerSourceIsSelfContained(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"../outside", "/tmp/outside"} {
		t.Run(path, func(t *testing.T) {
			repo := selectedSourceRepo(t, map[string]string{".apptown/app.yaml": "app:\n  web:\n    public: true\n    static: " + path + "\n"})
			if _, cleanup, err := PrepareSource(context.Background(), repo, "HEAD", "p", nil); err == nil {
				cleanup()
				t.Fatal("escaping source accepted")
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		repo := selectedSourceRepo(t, map[string]string{".apptown/app.yaml": "app:\n  web:\n    public: true\n    static: site\n"})
		if err := os.Symlink(t.TempDir(), filepath.Join(repo, "site")); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"add", "."}, {"commit", "-m", "external link"}} {
			if _, err := gitForDockerTest(repo, args...); err != nil {
				t.Fatal(err)
			}
		}
		if _, cleanup, err := PrepareSource(context.Background(), repo, "HEAD", "p", nil); err == nil {
			cleanup()
			t.Fatal("external symlink accepted")
		}
	})

}
