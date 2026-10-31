package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/execution"
)

// From-source + Dockerfile is intranet-ready: AppTown packages at
// release, so the signal must say so everywhere it shows.
func TestSourcePackagedSignals(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	dataDir := t.TempDir()
	authority := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := authority.SeedAdministrator(t.Context(), "reader"); err != nil {
		t.Fatal(err)
	}
	ctx := access.WithCaller(t.Context(), access.Caller{Verified: true, Principal: testAdmittedPrincipal(t, authority, "reader"), Receipt: "reader"})
	h := NewProjectViews(project.New(db, authority), nil, nil, nil, execution.Runner{}, scm.NewBranches(db, &scm.RepositoryLocks{}), dataDir)

	sourceID := httpTestProject(t, db, "src", "S")
	repo := filepath.Join(dataDir, "workspaces", "project-"+sourceID.String())
	_ = os.MkdirAll(filepath.Join(repo, ".apptown"), 0o755)
	_ = os.WriteFile(filepath.Join(repo, ".apptown", "app.yaml"), []byte("app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n"), 0o644)

	check := func(wantReady bool) {
		var out struct {
			RuntimeForm   string `json:"runtimeForm"`
			IntranetReady bool   `json:"intranetReady"`
		}
		req := httptest.NewRequest("GET", "/api/v1/projects/src", nil).WithContext(ctx)
		w := httptest.NewRecorder()
		req.SetPathValue("slug", "src")
		h.GetProject(w, req)
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if out.RuntimeForm != "runs from source" || out.IntranetReady != wantReady {
			t.Fatalf("form %q ready %v (want ready %v)", out.RuntimeForm, out.IntranetReady, wantReady)
		}
	}
	check(false)
	_ = os.WriteFile(filepath.Join(repo, "Dockerfile"), []byte("FROM scratch\n"), 0o644)
	check(true)

	// A static declaration already names its complete source; the release
	// renderer packages it with the same file server as a local preview.
	staticID := httpTestProject(t, db, "st", "T")
	repo2 := filepath.Join(dataDir, "workspaces", "project-"+staticID.String())
	_ = os.MkdirAll(filepath.Join(repo2, ".apptown"), 0o755)
	_ = os.WriteFile(filepath.Join(repo2, ".apptown", "app.yaml"), []byte("app:\n  web:\n    static: .\n    public: true\n"), 0o644)
	var out2 struct {
		RuntimeForm   string `json:"runtimeForm"`
		IntranetReady bool   `json:"intranetReady"`
	}
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/api/v1/projects/st", nil).WithContext(ctx)
	req2.SetPathValue("slug", "st")
	h.GetProject(w2, req2)
	_ = json.Unmarshal(w2.Body.Bytes(), &out2)
	if out2.RuntimeForm != "static site" || !out2.IntranetReady {
		t.Fatalf("declared static files should be intranet-ready: %+v", out2)
	}
	_ = os.WriteFile(filepath.Join(repo2, "Dockerfile"), []byte("FROM caddy:2-alpine\n"), 0o644)
	w2 = httptest.NewRecorder()
	h.GetProject(w2, req2)
	_ = json.Unmarshal(w2.Body.Bytes(), &out2)
	if !out2.IntranetReady {
		t.Fatal("static with Dockerfile must be ready")
	}
}
