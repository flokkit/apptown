package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/secret"
)

// A model binding's credentials are working only when every secret it
// names resolves; the reading names each one that does not, for its live
// check and for a Test connection of a draft.
func TestAModelBindingNamesTheCredentialsThatDoNotResolve(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("MODEL_KEY=k\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := testDB(t)
	s := NewService(db, Dependencies{Secrets: secret.NewService(db, secret.Config{DataDir: dir})})
	if h := s.CheckModelCredentials(context.Background(), []string{"MODEL_KEY"}); !h.OK || h.Detail != "credentials resolved (MODEL_KEY)" {
		t.Fatalf("resolved: %+v", h)
	}
	if h := s.CheckModelCredentials(context.Background(), []string{"MODEL_KEY", "OTHER_KEY"}); h.OK || !strings.Contains(h.Detail, "secret not resolved: OTHER_KEY") || strings.Contains(h.Detail, "MODEL_KEY") {
		t.Fatalf("missing: %+v", h)
	}
	if h := s.TestModelBinding(context.Background(), map[string]any{"secretEnv": []any{"MODEL_KEY"}}); !h.OK {
		t.Fatalf("draft resolved: %+v", h)
	}
	if h := s.TestModelBinding(context.Background(), map[string]any{"secretEnv": []any{"MODEL_KEY"}, "apiKeyFrom": "OTHER_KEY"}); h.OK || h.Detail != "secret not resolved: OTHER_KEY" {
		t.Fatalf("draft missing: %+v", h)
	}
}
