package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/scm"
)

// From-source + Dockerfile is intranet-ready: the forge packages at
// release, so the signal must say so everywhere it shows.
func TestSourcePackagedSignals(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	dataDir := t.TempDir()
	h := NewProjectViews(project.New(db, nil), nil, nil, nil, execution.Runner{}, scm.NewBranches(db, &scm.RepositoryLocks{}), dataDir)

	httpTestProject(t, db, "src", "S")
	repo := filepath.Join(dataDir, "workspaces", "project-src")
	_ = os.MkdirAll(filepath.Join(repo, ".apptown"), 0o755)
	_ = os.WriteFile(filepath.Join(repo, ".apptown", "app.yaml"), []byte("app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n"), 0o644)

	check := func(wantReady bool) {
		var out struct {
			RuntimeForm   string `json:"runtimeForm"`
			IntranetReady bool   `json:"intranetReady"`
		}
		req := httptest.NewRequest("GET", "/api/v1/projects/src", nil)
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
	httpTestProject(t, db, "st", "T")
	repo2 := filepath.Join(dataDir, "workspaces", "project-st")
	_ = os.MkdirAll(filepath.Join(repo2, ".apptown"), 0o755)
	_ = os.WriteFile(filepath.Join(repo2, ".apptown", "app.yaml"), []byte("app:\n  web:\n    static: .\n    public: true\n"), 0o644)
	var out2 struct {
		RuntimeForm   string `json:"runtimeForm"`
		IntranetReady bool   `json:"intranetReady"`
	}
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/api/v1/projects/st", nil)
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
