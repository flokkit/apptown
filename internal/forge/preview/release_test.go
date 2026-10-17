package preview

import (
	"context"
	"errors"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/dbtest"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/forge/project"
)

func TestReleasePreviewEligibility(t *testing.T) {
	for _, name := range []string{"released", "releasing", "failed", "missing-image", "missing-source"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := deployment.Release{ProjectID: idstest.N(3), Number: 7, State: deployment.Released, ExternalRef: "recorded-image", SourceSHA: "selected-tree", CommitSHA: "unavailable-receipt"}
			var want error
			switch name {
			case "releasing":
				r.State = deployment.Releasing
				want = ErrReleasedVersionMissing
			case "failed":
				r.State = deployment.ReleaseFailed
				want = ErrReleasedVersionMissing
			case "missing-image":
				r.ExternalRef = ""
				want = ErrReleaseInputs
			case "missing-source":
				r.SourceSHA = ""
				want = ErrReleaseInputs
			}
			got, err := recordedImage(&r)
			if !errors.Is(err, want) {
				t.Fatalf("eligibility: %+v %v", got, err)
			}
			if want != nil {
				if got != (Release{}) {
					t.Fatal("invalid release supplied launch inputs")
				}
				return
			}
			if got != (Release{ProjectID: idstest.N(3), Number: 7, Image: "recorded-image", SourceRevision: "selected-tree"}) {
				t.Fatalf("selection substituted receipt: %+v", got)
			}
		})
	}
}

type releasePreviewAuthorityFunc func(context.Context, ids.ID) error

func (f releasePreviewAuthorityFunc) Require(ctx context.Context, p access.Permission) (access.Caller, error) {
	return access.Caller{Principal: access.Principal{Name: "operator"}, Verified: true}, f(ctx, p.Resource.ProjectID)
}

type releasePreviewHostFunc func(context.Context, Release) (ReleaseResult, error)

func (f releasePreviewHostFunc) BootReleasePreview(ctx context.Context, r Release) (ReleaseResult, error) {
	return f(ctx, r)
}

func TestReleasePreviewAdmissionBeforeStoredOrProviderInputs(t *testing.T) {
	for _, name := range []string{"denied", "cancelled", "unconfigured", "invalid-number", "invalid-project"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			denied := errors.New("decision receipt failed")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			deps := Dependencies{Authority: releasePreviewAuthorityFunc(func(context.Context, ids.ID) error {
				calls++
				if name == "denied" {
					return denied
				}
				return nil
			}), ReleaseHost: releasePreviewHostFunc(func(context.Context, Release) (ReleaseResult, error) {
				t.Fatal("refused request reached host")
				return ReleaseResult{}, nil
			})}
			in := ReleaseSelection{ProjectID: idstest.N(3), Number: 7}
			switch name {
			case "cancelled":
				cancel()
			case "unconfigured":
				deps.Authority = nil
			case "invalid-number":
				in.Number = 0
			case "invalid-project":
				in.ProjectID = ids.Nil
			}
			// No database is available: refusal must precede even release lookup.
			got, err := New(nil, deps).RequestReleasePreview(ctx, in)
			if err == nil || got != (ReleaseResult{}) {
				t.Fatalf("admission: %+v %v", got, err)
			}
			if name == "denied" && !errors.Is(err, denied) {
				t.Fatal(err)
			}
			if name == "cancelled" && (!errors.Is(err, context.Canceled) || calls != 0) {
				t.Fatalf("cancelled: %v calls=%d", err, calls)
			}
		})
	}
}

func TestReleasePreviewStoredSelectionAndExecution(t *testing.T) {
	t.Parallel()
	w := newWorld(t, "", "")
	db, s := w.db, w.service(Dependencies{})
	var pid ids.ID
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		var err error
		pid, err = project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "preview", Name: "Preview"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	target, err := s.targets.Put(context.Background(), &deployment.Target{Name: "host", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := s.deployments.EnsureStanding(t.Context(), pid, "main", target.ID, (deployment.Attributes{}).WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	cut, err := s.releases.Start(context.Background(), deployment.ReleaseDraft{Source: deployment.ReleaseFromBranch, ProjectID: pid, DeploymentID: dep.ID, Deployment: "main", Revision: "selected", Actor: "owner"}, "preview fixture")
	if err != nil {
		t.Fatal(err)
	}
	runtime := &s.runtime
	calls := 0
	hostErr := errors.New("host failed after partial boot")
	s.authority = releasePreviewAuthorityFunc(func(context.Context, ids.ID) error { return nil })
	s.releaseHost = releasePreviewHostFunc(func(ctx context.Context, selected Release) (ReleaseResult, error) {
		calls++
		if selected != (Release{ProjectID: pid, Number: cut.Number, Image: "original-image", SourceRevision: "selected"}) {
			t.Errorf("wrong stored inputs: %+v", selected)
		}
		if runtime.Inspect(context.Background(), ReleaseKey(pid, cut.Number), previewFacts{}).State != Starting {
			t.Error("source/host ran outside ownership")
		}
		return ReleaseResult{Result: Result{ProjectID: idstest.N(999), URL: "http://preview", Revision: "resolved", HostPort: 1234}, Image: "substitution", Reused: true}, hostErr
	})
	in := ReleaseSelection{ProjectID: pid, Number: cut.Number}
	for _, request := range []ReleaseSelection{in, {ProjectID: pid, Number: cut.Number + 1}, {ProjectID: idstest.N(999999), Number: cut.Number}} {
		if out, err := s.RequestReleasePreview(context.Background(), request); !errors.Is(err, ErrReleasedVersionMissing) || out != (ReleaseResult{}) || calls != 0 {
			t.Fatalf("unreleased/absent/foreign: %+v %v calls=%d", out, err, calls)
		}
	}
	outcomes := deployment.NewOutcomes(db, nil)
	at, _, err := outcomes.Admit(context.Background(), deployment.Start{DeploymentID: dep.ID, TargetID: target.ID, Key: "fixture", Inputs: deployment.Inputs{ReleaseID: cut.ID, Revision: "selected", ImageDigest: "original-image"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outcomes.Complete(context.Background(), deployment.Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active", URL: "http://original"}}); err != nil {
		t.Fatal(err)
	}
	dbtest.FailNextStatement(t, db, "-- name: ReleaseByNumber")
	if out, err := s.RequestReleasePreview(context.Background(), in); err == nil || errors.Is(err, ErrReleasedVersionMissing) || out != (ReleaseResult{}) || calls != 0 {
		t.Fatalf("unreadable release: %+v %v", out, err)
	}
	out, err := s.RequestReleasePreview(context.Background(), in)
	var execution *ExecutionError
	if !errors.Is(err, hostErr) || !errors.As(err, &execution) || out != (ReleaseResult{}) {
		t.Fatalf("failed boot returned success: %+v %v", out, err)
	}
	if runtime.Inspect(context.Background(), ReleaseKey(pid, cut.Number), previewFacts{}).State != Broken {
		t.Fatal("failure did not settle runtime")
	}
	hostErr = nil
	out, err = s.RequestReleasePreview(context.Background(), in)
	if err != nil || out.ProjectID != pid || out.Image != "original-image" || out.Revision != "resolved" || out.URL != "http://preview" || out.HostPort != 1234 || !out.Reused {
		t.Fatalf("retry result: %+v %v", out, err)
	}
	stored, err := s.releases.ByNumber(context.Background(), pid, cut.Number)
	if err != nil || stored.ExternalRef != "original-image" || stored.SourceSHA != "selected" || stored.State != deployment.Released {
		t.Fatalf("preview changed receipt: %+v %v", stored, err)
	}

	owner, err := runtime.Begin(context.Background(), ReleaseKey(pid, cut.Number))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Finish(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	admitted := make(chan struct{})
	s.authority = releasePreviewAuthorityFunc(func(context.Context, ids.ID) error { close(admitted); return nil })
	done := make(chan error, 1)
	before := calls
	go func() { _, err := s.RequestReleasePreview(ctx, in); done <- err }()
	select {
	case <-admitted:
	case <-time.After(time.Second):
		t.Fatal("request did not reach authority")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter did not cancel")
	}
	if calls != before || runtime.Inspect(context.Background(), ReleaseKey(pid, cut.Number), previewFacts{}).State != Starting {
		t.Fatal("cancelled request touched another boot")
	}
}
