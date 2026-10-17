package deployment

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/ids"
)

func TestReleasePreflightUsesCommittedDeclaration(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	dir := t.TempDir()
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "game", Name: "Game"})
	})
	if err != nil {
		t.Fatal(err)
	}
	projects := project.New(db, nil)
	if err := projects.SetDefaultBranch(context.Background(), id, "main"); err != nil {
		t.Fatal(err)
	}
	p, err := projects.ByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("PORTAINER_URL=https://unused.invalid\nPORTAINER_API_KEY=unused\nFORGE_APPS_DOMAIN=apps.example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "workspaces", "project-"+p.Slug)
	if err := os.MkdirAll(filepath.Join(repo, ".forge"), 0755); err != nil {
		t.Fatal(err)
	}
	declaration := "app:\n  web:\n    image: game:v1\n    public: true\n    ports: [\"8080\"]\n"
	path := filepath.Join(repo, ".forge", "app.yaml")
	if err := os.WriteFile(path, []byte(declaration+"needs:\n  unknown-service: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := releaseGit(t.Context(), repo, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	if _, err := releaseGit(t.Context(), repo, "add", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := releaseGit(t.Context(), repo, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "selected game"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(declaration), 0600); err != nil {
		t.Fatal(err)
	}
	secrets := secret.NewService(db, secret.Config{DataDir: dir})
	integrations := integration.NewService(db, integration.Dependencies{Projects: projects, Secrets: secrets})
	owner := New(context.Background(), db, assembled(db, testRuntime{DataDir: dir, Projects: projects, Secrets: secrets, Integrations: integrations, Needs: dependency.NewCatalog(db, nil)}, Dependencies{}))
	owner.SeedTargets(t.Context(), nil) // the intranet target, as a start records it from configuration
	_, code, err := owner.ReleaseLAN(t.Context(), &p, "test", LANReleaseRequest{})
	if code != 422 || err == nil || !strings.Contains(err.Error(), "unknown-service") {
		t.Fatalf("preflight used live checkout: %d %v", code, err)
	}
}
