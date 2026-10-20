package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"flokkit.io/forge/internal/forge/deployment"
)

func TestDeployHTTPRejectsBadInputAndReturnsQueuedSelectedBundle(t *testing.T) {
	t.Parallel()
	// doWorld's existing driver hooks require this fixture to stay serial.
	a, f, p, b := doWorld(t)
	testAdministrator(t, a, "boss")
	target, err := a.Deployment.Targets.ByName(t.Context(), "do-syd")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.Deployments.EnsureStanding(t.Context(), p.ID, "cloud", target.ID, deployment.Attributes{Feed: "promotion", Audience: "organisation", DataClass: "non-production", PromoteAfter: "main"}); err != nil {
		t.Fatal(err)
	}
	contract, err := os.ReadFile(filepath.Join(a.projectRepoPath(p.Slug), ".apptown", "app.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	newer, err := a.Deployment.Bundles.Cut(t.Context(), p.ID, b.ContractRevision, map[string]string{"web": "sha256:newer"}, b.Init, contract, deployment.BundleOrigin{})
	if err != nil || newer.Compare(b.ID) <= 0 {
		t.Fatalf("newer bundle: %d %v", newer, err)
	}
	h := a.Handler()
	path := "/api/v1/projects/expense/deployments/cloud/deploy"
	for _, tc := range []struct {
		who, body string
		code      int
	}{{"outsider", `{}`, 403}, {"boss", `{"bundleId":`, 400}, {"boss", `{"bundleId":"bad"}`, 400}, {"boss", `{"bundleId":"0000000f-423f-7000-8000-000000000000"}`, 404}} {
		w := authzCall(h, tc.who, "POST", path, tc.body)
		if w.Code != tc.code {
			t.Fatalf("rejected deploy: %d %s", w.Code, w.Body.String())
		}
		releases, err := a.Deployment.Releases.ForProject(context.Background(), p.ID, 50)
		if err != nil || len(releases) != 0 {
			t.Fatalf("rejected deploy admitted: %+v %v", releases, err)
		}
		if got := f.Snapshot(); len(got.Apps) != 0 || len(got.Databases) != 0 {
			t.Fatalf("rejected deploy reached provider: %+v", got)
		}
	}
	w := authzCall(h, "boss", "POST", path, fmt.Sprintf(`{"bundleId":%q}`, b.ID))
	var out struct {
		OK         bool
		Number     int64
		Deployment string
		State      deployment.ReleaseState
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 202 || !out.OK || out.Number == 0 || out.Deployment != "cloud" || out.State != deployment.Releasing {
		t.Fatalf("queued deploy: %d %s %v", w.Code, w.Body.String(), err)
	}
	release, err := a.Deployment.Releases.ByNumber(context.Background(), p.ID, out.Number)
	if err != nil || release.BundleID != b.ID || release.ApprovedBy != "boss" || release.State != deployment.Releasing {
		t.Fatalf("selected release: %+v %v", release, err)
	}
	if got := f.Snapshot(); len(got.Apps) != 0 {
		t.Fatal("admission claimed completion before worker execution")
	}
	if w := authzCall(h, "boss", "POST", path, fmt.Sprintf(`{"bundleId":%q}`, b.ID)); w.Code != 409 {
		t.Fatalf("in-flight conflict: %d %s", w.Code, w.Body.String())
	}
}
