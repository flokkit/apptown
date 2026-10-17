package forge

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/httpapi"
	"flokkit.io/forge/internal/forge/identity"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/workflow"
)

func TestWorkHTTPRefusesIdentitySpoofAndPrivateEvidence(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t)
	fx := fixtureWork(t, a)
	artifact, err := a.Work.AttachArtifact(verifiedContext(context.Background(), access.Principal{Kind: "user", Name: "owner", Via: "token"}), fx.Run,
		workflow.ArtifactDraft{Kind: "secret", Name: "private", Body: "private words"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/runs/"+fx.Run.String(), nil)
	req.SetPathValue("id", fx.Run.String())
	req.Header.Set("X-Forge-Actor", "owner")
	out := httptest.NewRecorder()
	httpapi.NewRuns(a.Work).GetRun(out, req.WithContext(callerContext(req)))
	if out.Code != 401 {
		t.Fatalf("unverified header: %d %s", out.Code, out.Body.String())
	}
	for _, path := range []string{"/api/v1/runs/" + fx.Run.String(), "/api/v1/runs/" + fx.Run.String() + "/trace", "/api/v1/artifacts/" + artifact.String(), "/api/v1/changes/" + fx.Change.String()} {
		out := authzCall(a.Handler(), "outsider", "GET", path, "")
		if out.Code != 403 || strings.Contains(out.Body.String(), "private words") {
			t.Fatalf("private %s: %d %s", path, out.Code, out.Body.String())
		}
	}
	out = authzCall(a.Handler(), "owner", "GET", "/api/v1/artifacts/"+artifact.String(), "")
	if out.Code != 200 || !strings.Contains(out.Body.String(), "private words") {
		t.Fatalf("owner evidence: %d %s", out.Code, out.Body.String())
	}
}

func workAuthorityFixture(t *testing.T, configs ...Config) *App {
	t.Helper()
	a := authzFixture(t, configs...)
	revokeReadingForAll(t, a)
	return a
}

// revokeReadingForAll revokes the fixture-wide read access (everyone's
// auditor role) to exercise private-project decisions.
func revokeReadingForAll(t *testing.T, a *App) {
	t.Helper()
	grants, err := a.Access.GrantsAt(context.Background(), access.ScopeInstance, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range grants {
		if grant.SubjectType == "group" && grant.Subject == identity.GroupAllUsers && grant.RoleName == access.RoleAuditor {
			if _, err := a.Access.Revoke(context.Background(), grant.ID, "fixture"); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestWorkAbandonHTTPRejectsUnverifiedActor(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t)
	fx := fixtureWork(t, a)
	req := httptest.NewRequest("POST", "/api/v1/runs/"+fx.Run.String()+"/abandon", strings.NewReader(`{"reason":"spoofed"}`))
	req.SetPathValue("id", fx.Run.String())
	req.Header.Set("X-Forge-Actor", "owner")
	out := httptest.NewRecorder()
	httpapi.NewRuns(a.Work).AbandonRun(out, req.WithContext(callerContext(req)))
	if out.Code != 401 {
		t.Fatalf("unverified stop: %d %s", out.Code, out.Body.String())
	}
	requireRuns(t, a, 1, func(r workflow.RunSummary) bool { return r.ID == fx.Run && r.Status == workflow.RunRunning })
}
