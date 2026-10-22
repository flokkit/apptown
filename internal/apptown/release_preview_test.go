package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/preview"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/gitexec"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

func TestReleasePreviewUsesSelectedTreeAndOriginalImage(t *testing.T) {
	t.Parallel()
	a, f := bootWorld(t)
	repo := a.projectRepoPath("p")
	writeBootFixture(t, repo)
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "selected release source"}} {
		if _, err := gitexec.Run(t.Context(), repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	source, err := (deploy.SelectedSource{Repository: repo, Revision: "HEAD"}).Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".apptown/app.yaml"), []byte("invalid: current worktree must not supply the contract\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cut := releasedPreviewFixture(t, a, idstest.N(1), source)
	// A historical receipt need not name the runnable source. Deliberately poison
	// just that field; lifecycle creation and settlement use Deployment operations.
	// sql-guarantee: Poison only the immutable release source while preserving the original image receipt; release admission fixes commit_sha and settlement cannot rewrite that historical identity.
	completionExec(t, a.DB, `UPDATE releases SET commit_sha='original-unavailable' WHERE id=?`, cut.ID)
	if _, err := a.Access.EnsureUser(context.Background(), "preview-owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Access.Grant(context.Background(), identity.Spec{SubjectType: "user", Subject: "preview-owner", Capability: string(access.CapDeploymentOperate), Scope: access.ScopeProject, ScopeID: idstest.N(1).String(), By: "setup"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := project.New(a.DB, nil).Contracts().Snapshot(context.Background(), idstest.N(1), source, "present", "app-yaml", "invalid: a snapshot must never supply launch inputs", ""); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/v1/projects/p/releases/1/preview", nil)
	r.SetPathValue("slug", "p")
	r.SetPathValue("number", "1")
	r = testRequestAs(r, "preview-owner")
	w := httptest.NewRecorder()
	httpapi.NewReleaseOperations(a.Projects, a.Access, a.Deployment, a.Preview, a.Work, a.DB, a.cfg.dataDir, releaseTargetName, nil).ReleasePreview(w, r.WithContext(callerContext(r)))
	if w.Code != 200 {
		t.Fatalf("release preview: %d %s", w.Code, w.Body.String())
	}
	runs := expectBootSequence(t, f, docker.ReleaseStackID("p", 1), docker.PreviewLabelName(docker.ReleasePreviewLabel("p", 1)), false)
	if !strings.HasSuffix(runs[3].call, " registry.example/original@sha256:receipt") {
		t.Fatalf("release preview substituted its image: %s", runs[3].call)
	}
	if len(f.matching("build ")) != 0 {
		t.Fatal("release preview rebuilt historical source instead of using its recorded image")
	}
	releases, err := a.Deployment.Releases.ForProject(r.Context(), idstest.N(1), 100)
	if err != nil {
		t.Fatal(err)
	}
	recorded := 0
	for _, release := range releases {
		if release.CommitSHA == "original-unavailable" && release.ExternalRef == "registry.example/original@sha256:receipt" {
			recorded++
		}
	}
	if recorded != 1 {
		t.Fatalf("%d releases keep their recorded receipt", recorded)
	}
	if receipts := previewReceipts(t, a); len(receipts) != 1 || receipts[0].Source != source {
		t.Fatalf("preview receipts: %+v", receipts)
	}

	// A failed boot of another numbered version cleans its own disposable stack,
	// while the earlier preview remains alive and its release receipt unchanged.
	failedCut := releasedPreviewFixture(t, a, idstest.N(1), source)
	failedName := docker.PreviewLabelName(docker.ReleasePreviewLabel("p", failedCut.Number))
	f.reset()
	f.reply = func(args []string) (string, error, bool) {
		if len(args) > 0 && args[0] == "run" && containerNameOf(strings.Join(args, " ")) == failedName {
			return "provider refused this version", errFakeExit, true
		}
		return "", nil, false
	}
	r.SetPathValue("number", fmt.Sprint(failedCut.Number))
	failed := httptest.NewRecorder()
	httpapi.NewReleaseOperations(a.Projects, a.Access, a.Deployment, a.Preview, a.Work, a.DB, a.cfg.dataDir, releaseTargetName, nil).ReleasePreview(failed, r.WithContext(callerContext(r)))
	if failed.Code != 502 || f.isStarted(failedName) {
		t.Fatalf("failed preview not cleaned: %d %s", failed.Code, failed.Body.String())
	}
	if len(f.matching("volume rm "+docker.Current.DataVolume(failedName))) < 2 || len(f.matching("network rm "+docker.Current.Network(docker.ReleaseStackID("p", failedCut.Number)))) < 2 {
		t.Fatalf("missing before/after cleanup: %v", f.joined())
	}
	if !f.isStarted(docker.PreviewLabelName(docker.ReleasePreviewLabel("p", 1))) {
		t.Fatal("failed release preview removed a different version")
	}
	if n := len(previewReceipts(t, a)); n != 1 {
		t.Fatalf("%d preview receipts after a failed boot", n)
	}

}

// Construct the historical receipt through the same admission and settlement
// operations as deployment. No raw release lifecycle writes in preview fixtures.
func releasedPreviewFixture(t *testing.T, a *App, projectID ids.ID, source string) deployment.NumberedRelease {
	t.Helper()
	target, err := a.Deployment.Targets.Put(context.Background(), &deployment.Target{Name: "preview-fixture", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	env, err := a.Deployment.Deployments.EnsureStanding(t.Context(), projectID, "main", target.ID, (deployment.Attributes{}).WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	cut, err := a.Deployment.Releases.Start(context.Background(), deployment.ReleaseDraft{Source: deployment.ReleaseFromBranch, ProjectID: projectID, DeploymentID: env.ID, Deployment: "main", Revision: source, Actor: "preview-owner"}, "preview fixture")
	if err != nil {
		t.Fatal(err)
	}
	store := deployment.NewOutcomes(a.DB, nil)
	at, _, err := store.Admit(context.Background(), deployment.Start{DeploymentID: env.ID, TargetID: target.ID, Key: fmt.Sprintf("fixture-%d", cut.ID), Inputs: deployment.Inputs{ReleaseID: cut.ID, Revision: source, ImageDigest: "registry.example/original@sha256:receipt"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Complete(context.Background(), deployment.Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active", URL: "http://original"}}); err != nil {
		t.Fatal(err)
	}
	return cut
}

type releasePreviewTransportHost struct {
	calls  int
	reused bool
	err    error
}

func (h *releasePreviewTransportHost) BootReleasePreview(_ context.Context, selected preview.Release) (preview.ReleaseResult, error) {
	h.calls++
	return preview.ReleaseResult{Result: preview.Result{URL: "http://historical-preview", Revision: selected.SourceRevision, HostPort: 1234}, Reused: h.reused}, h.err
}

func TestReleasePreviewChecksCurrentAuthorityAndHTTP(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	var pid, other ids.ID
	if err := a.DB.InTx(context.Background(), func(tx *database.Tx) error {
		var err error
		pid, err = project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "preview", Name: "Preview"})
		if err != nil {
			return err
		}
		other, err = project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "other", Name: "Other"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	host := &releasePreviewTransportHost{}
	deps := previewDependenciesForTest(a)
	deps.ReleaseHost = host
	a.Preview = preview.New(a.DB, deps)
	rebuildConsoleOwners(a)

	ctx := context.Background()
	boss, err := a.Access.Bootstrap(ctx, identity.NewAccount{Username: "boss", Password: "test-password"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.EnsureAuthorization(ctx); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"preview-owner", "outsider"} {
		if _, err := a.Access.EnsureUser(ctx, who); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Access.Grant(ctx, identity.Spec{SubjectType: "user", Subject: "preview-owner", Capability: string(access.CapDeploymentOperate), Scope: access.ScopeProject, ScopeID: fmt.Sprint(pid), By: "boss"}, ""); err != nil {
		t.Fatal(err)
	}
	cut := releasedPreviewFixture(t, a, pid, "historical-source")
	_ = releasedPreviewFixture(t, a, other, "other-source")
	owner := access.Principal{Kind: "user", Name: "preview-owner", Via: "session"}
	in := preview.ReleaseSelection{ProjectID: pid, Number: cut.Number}
	call := func(pr access.Principal) (preview.ReleaseResult, error) {
		return a.Preview.RequestReleasePreview(verifiedContext(ctx, pr), in)
	}
	for _, pr := range []access.Principal{{}, {Kind: "user", Name: "outsider", Via: "session"}, {Kind: "user", Name: "preview-owner", Via: "oauth", Scopes: []string{"apptown:user:read"}}} {
		if out, err := call(pr); err == nil || out != (preview.ReleaseResult{}) || host.calls != 0 {
			t.Fatalf("unauthorized preview: %+v %v", out, err)
		}
	}
	if _, err := call(access.Principal{}); !errors.Is(err, access.ErrPrincipalRequired) {
		t.Fatal(err)
	}
	for _, statement := range []string{"-- name: UserByName :one", "-- name: InsertDecision"} {
		dbtest.FailNextStatement(t, a.DB, statement)
		if out, err := call(owner); err == nil || out != (preview.ReleaseResult{}) || host.calls != 0 {
			t.Fatalf("failed authority/receipt: %+v %v", out, err)
		}
	}
	if out, err := a.Preview.RequestReleasePreview(verifiedContext(ctx, owner), preview.ReleaseSelection{ProjectID: other, Number: 1}); err == nil || out != (preview.ReleaseResult{}) || host.calls != 0 {
		t.Fatalf("foreign project's release: %+v %v", out, err)
	}
	h := a.Handler()
	for _, number := range []string{"0", "-1", "1junk", "9223372036854775808"} {
		w := authzCall(h, "preview-owner", "POST", "/api/v1/projects/preview/releases/"+number+"/preview", "")
		if w.Code != 400 || host.calls != 0 {
			t.Fatalf("bad number %q: %d %s", number, w.Code, w.Body.String())
		}
	}
	path := fmt.Sprintf("/api/v1/projects/preview/releases/%d/preview", cut.Number)
	w := authzCall(h, "preview-owner", "POST", path, "")
	var response struct{ URL string }
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 || response.URL != "http://historical-preview" {
		t.Fatalf("preview: %d %s %v", w.Code, w.Body.String(), err)
	}
	if receipts := previewReceipts(t, a); len(receipts) != 1 || receipts[0].Source != "historical-source" || receipts[0].Ref != "registry.example/original@sha256:receipt" {
		t.Fatalf("preview receipts: %+v", receipts)
	}
	host.reused = true
	if w := authzCall(h, "preview-owner", "POST", path, ""); w.Code != 200 {
		t.Fatalf("reuse: %d %s", w.Code, w.Body.String())
	}
	if n := len(previewReceipts(t, a)); n != 1 {
		t.Fatalf("%d preview receipts after reuse", n)
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{&preview.ReleaseSourceError{Cause: errors.New("native contract unavailable")}, 422}, {errors.New("provider did not boot"), 502}} {
		host.err = tc.err
		if w := authzCall(h, "preview-owner", "POST", path, ""); w.Code != tc.status || !strings.Contains(w.Body.String(), tc.err.Error()) {
			t.Fatalf("boot error: %d %s", w.Code, w.Body.String())
		}
	}
	host.err = nil
	disabled := true
	if err := a.Access.SaveUser(ctx, identity.AccountChange{Username: "preview-owner", Disabled: &disabled}, boss.Principal); err != nil {
		t.Fatal(err)
	}
	before := host.calls
	if out, err := call(owner); err == nil || out != (preview.ReleaseResult{}) || host.calls != before {
		t.Fatalf("disabled caller: %+v %v", out, err)
	}
}

type previewReceipt struct {
	Source string `json:"source"`
	Ref    string `json:"ref"`
}

// previewReceipts is every release.preview ledger line's source and image.
func previewReceipts(t *testing.T, a *App) []previewReceipt {
	t.Helper()
	var out []previewReceipt
	for _, e := range ledgerEntries(t, a, ids.Nil, "release.preview") {
		out = append(out, previewReceipt{Source: exactText(e.Payload, "source"), Ref: exactText(e.Payload, "ref")})
	}
	return out
}
