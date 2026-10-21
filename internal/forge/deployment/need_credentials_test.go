package deployment

import (
	"testing"

	"flokkit.io/forge/internal/deploy/docker"
)

// Each deployment's needs are started with, and its app given, a password
// of its own: not the catalogue's, not another deployment's, and the same
// at its next boot. A preview's is its own as well, the same each time it
// is prepared, so its identity does not move between preparations.
func TestEachDeploymentAndPreviewHasItsOwnNeedCredentials(t *testing.T) {
	t.Parallel()
	s, db, client, _, projectID := hostWorld(t, "")
	c, ws := llmContract(t, "app:\n  web:\n    image: reg.example/app:1\n    ports: [\"8080\"]\n    public: true\nneeds:\n  postgres: {}\n")
	booted := func(dep *Deployment) string {
		t.Helper()
		res, err := bootDeployment(s, client, dep, c, ws)
		if err != nil {
			t.Fatal(err)
		}
		need := ""
		for _, svc := range res.Stack.Services {
			if svc.Name == "postgres" {
				need = svc.Env["POSTGRES_PASSWORD"]
			}
		}
		app := res.Stack.Plan.PublicPart().Env["PGPASSWORD"]
		if len(need) != 64 || app != need {
			t.Fatalf("%s: the need was started with %q and the app given %q", dep.Name, need, app)
		}
		return need
	}
	main, staging := llmDeployment(t, s, db, projectID, "main"), llmDeployment(t, s, db, projectID, "staging")
	first := booted(main)
	if again := booted(main); again != first {
		t.Fatal("the deployment's password changed at its next boot")
	}
	if other := booted(staging); other == first {
		t.Fatal("two deployments share a password")
	}
	prepared := func() docker.Prepared {
		t.Helper()
		p, err := s.env.PrepareBoot(t.Context(), projectID, "p", "preview", ws, c, s.env.NeedCredentials(PreviewNeedScope("apptown-preview-change-7")))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	a, b := prepared(), prepared()
	preview := a.NeedEnv["PGPASSWORD"]
	if len(preview) != 64 || preview == first || a.Public().Digest() != b.Public().Digest() {
		t.Fatalf("the preview's password %q (main's %q), its identity %s then %s", preview, first, a.Public().Digest(), b.Public().Digest())
	}
}
