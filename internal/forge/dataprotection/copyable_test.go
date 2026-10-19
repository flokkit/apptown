package dataprotection

import (
	"context"
	"errors"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/ids"
)

// A release of new code onto a deployment that holds data the forge cannot
// copy now (an App Platform database with no copy runner or backup store)
// is refused whatever the code changes; a person may release anyway, and the
// release records the choice. The first release, onto a deployment that
// holds nothing yet, a release of the bundle it already runs, and a release
// once the data can be copied, are not asked.
func TestAReleaseOverDataTheForgeCannotCopyIsAPersonsChoice(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	ctx := context.Background()
	if _, err := w.bind(w.project, "prod", "production", ids.Nil); err != nil {
		t.Fatal(err)
	}
	if rel, log := w.release("prod"); rel.State != deployment.Released {
		t.Fatalf("the first release %s:\n%s", rel.State, log)
	}
	// The bundle prod runs, again (an override's redeploy): no new code.
	if rel, log := w.release("prod"); rel.State != deployment.Released {
		t.Fatalf("the running bundle again %s:\n%s", rel.State, log)
	}
	newer, err := w.s.Bundles.Cut(ctx, w.project, w.bundle.ContractRevision, map[string]string{"web": "sha256:newer"}, nil, w.contract, deployment.BundleOrigin{})
	if err != nil {
		t.Fatal(err)
	}
	again := deployment.DeploySelection{ProjectID: w.project, DeploymentName: "prod", BundleID: newer}
	if _, err := w.s.RequestRelease(ctx, again); !errors.Is(err, deployment.ErrRefusedContract) || !strings.Contains(err.Error(), "cannot copy the data prod holds") {
		t.Fatalf("a release over data with no copy: %v", err)
	}
	again.AcceptUncopiedData = true
	accepted, err := w.s.RequestRelease(ctx, again)
	if err != nil {
		t.Fatalf("the person's choice: %v", err)
	}
	events, err := w.s.Releases.Events(ctx, accepted.ID)
	if err != nil || len(events) == 0 || !strings.Contains(events[0].Text, "released by alice accepting that AppTown cannot copy the data prod holds") {
		t.Fatalf("the release's record: %+v %v", events, err)
	}
	w.protect()
	if why := w.s.CannotCopy(ctx, mustDeployment(t, w, "prod"), w.target); why != "" {
		t.Fatalf("with a store and a copy runner the data can be copied: %q", why)
	}
}

func mustDeployment(t *testing.T, w *dataWorld, name string) *deployment.Deployment {
	t.Helper()
	dep, err := w.s.Deployments.ByName(context.Background(), w.project, name)
	if err != nil {
		t.Fatal(err)
	}
	return dep
}

// On a Docker host the data cannot be copied when a need the running
// release declares has no kind the catalogue can read: the answer names it.
// A deployment that has served no release holds nothing yet, whatever
// could be read of it.
func TestADockerHostDeploymentWithAnUnreadNeedCannotBeCopied(t *testing.T) {
	t.Parallel()
	s, _, _, _, pid := backupWorld(t, "cc", answeringPort(t))
	local, err := s.Targets.ByName(context.Background(), "local")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := s.Deployments.EnsureStanding(context.Background(), pid, "fresh", local.ID, deployment.Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if why := s.CannotCopy(context.Background(), fresh, local); why != "" {
		t.Fatalf("a deployment that has served no release: %q", why)
	}
	releaseMain(t, s, "cc", "app:\n  web:\n    image: registry.example/cc:1\n    ports: [\"8080\"]\n    public: true\nneeds:\n  postgres: {}\n  quantum: {}\n")
	dep, err := s.Deployments.ByName(context.Background(), pid, "main")
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.Targets.ByID(context.Background(), dep.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	if why := s.CannotCopy(context.Background(), dep, target); !strings.Contains(why, "quantum") {
		t.Fatalf("an unread need: %q", why)
	}
}
