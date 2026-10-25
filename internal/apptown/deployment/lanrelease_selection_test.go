package deployment

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
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
	var key atomic.Value
	key.Store("unused")
	var calls atomic.Int32
	server := providerScopeServer(t, &key, &calls)
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("PORTAINER_URL="+server.URL+"\nPORTAINER_API_KEY=unused\nAPPTOWN_APPS_DOMAIN=apps.example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "workspaces", "project-"+p.ID.String())
	if err := os.MkdirAll(filepath.Join(repo, ".apptown"), 0755); err != nil {
		t.Fatal(err)
	}
	declaration := "app:\n  web:\n    image: game:v1\n    public: true\n    ports: [\"8080\"]\n"
	path := filepath.Join(repo, ".apptown", "app.yaml")
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
	selected, err := releaseGit(t.Context(), repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	publishedBundleFixture(t, db, id, selected, map[string]string{"web": "registry.example/team/app@sha256:" + strings.Repeat("a", 64)})
	if err := os.WriteFile(path, []byte(declaration), 0600); err != nil {
		t.Fatal(err)
	}
	secrets := secret.NewService(db, secret.Config{DataDir: dir})
	integrations := integration.NewService(db, integration.Dependencies{Projects: projects, Secrets: secrets})
	owner := New(context.Background(), db, assembled(db, testRuntime{DataDir: dir, Projects: projects, Secrets: secrets, Integrations: integrations, Needs: dependency.NewCatalog(db, nil)}, Dependencies{Authority: &requestAuthority{}}))
	if _, err := owner.AttachPortainerTarget(t.Context(), PortainerAttachment{Name: IntranetTargetName, EndpointID: 3, Environment: "docker", URLFrom: "PORTAINER_URL", APIKeyFrom: "PORTAINER_API_KEY", Domain: "apps.example"}); err != nil {
		t.Fatal(err)
	}
	_, code, err := owner.ReleaseLAN(t.Context(), &p, "test", LANReleaseRequest{})
	if code != 422 || err == nil || !strings.Contains(err.Error(), "unknown-service") {
		t.Fatalf("preflight used live checkout: %d %v", code, err)
	}
}
