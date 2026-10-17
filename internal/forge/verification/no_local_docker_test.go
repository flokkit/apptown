package verification

import (
	"context"
	"errors"
	"strings"
	"testing"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/deployment"
)

// refusalSpy stands for no local Docker, as docker.Refusing does, and
// records every call that reaches it.
type refusalSpy struct {
	docker.Refusing
	asked [][]string
}

func (s *refusalSpy) Run(_ context.Context, _ string, c docker.Command) ([]byte, error) {
	s.asked = append(s.asked, c.Args)
	return nil, docker.ErrNoLocalBackend
}

// On a forge with no local Docker, a scan's baseline says the serving
// release image is unknown, never that nothing serves, and asks no Docker,
// wherever its checks run.
func TestAScanBaselineWithNoLocalDockerIsUnknown(t *testing.T) {
	t.Parallel()
	for name, exec := range map[string]Executor{"on the worker": {}, "on this machine": {Local: true}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newScannerOwnerFixture(t)
			spy := &refusalSpy{}
			repo, sha := baselineTree(t, "")
			deployments := deployment.NewDeployments(f.DB)
			servingMain(t, f, deployments, sha)
			f.Scanners.deployments, f.Scanners.docker, f.Scanners.exec = deployments, &docker.Scope{Client: spy}, exec
			base := f.Scanners.baseImageFingerprints(f.Context, f.ProjectID, repo, sha, check.Scanner{}, "scanner", t.TempDir(), nil, "scan")
			if base.OK || !strings.Contains(base.Note, "this forge runs no Docker of its own, so it cannot read the image the app runs now") || strings.Contains(base.Note, "no local Docker") || len(spy.asked) != 0 {
				t.Fatalf("baseline: %+v, asked %v", base, spy.asked)
			}
		})
	}
}

// On a forge with no local Docker, a check's image is never a release
// image reused from the forge's own Docker, which it does not have.
func TestAScanWithNoLocalDockerReusesNoReleaseImage(t *testing.T) {
	t.Parallel()
	f := newScannerOwnerFixture(t)
	spy := &refusalSpy{}
	repo, sha := baselineTree(t, "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n")
	f.Scanners.deployments = deployment.NewDeployments(f.DB)
	servingMain(t, f, f.Scanners.deployments, sha)
	// The executor stays this machine's, with no shared build cached.
	f.Scanners.docker = &docker.Scope{Client: spy}
	f.Daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "image" {
			return nil, errors.New("no such image"), true
		}
		return nil, nil, false
	})
	_, note, status, err := f.Scanners.imageForCommit(f.Context, f.ProjectID, repo, sha, false)
	if err != nil || status != "deferred" || strings.Contains(note, "release image") || len(spy.asked) != 0 {
		t.Fatalf("image: %q %q %v, asked %v", status, note, err, spy.asked)
	}
}

// servingMain records the project's main deployment, with a container, as
// serving sha from a recorded image.
func servingMain(t *testing.T, f scannerOwnerFixture, deployments *deployment.Deployments, sha string) {
	t.Helper()
	target, err := deployment.NewTargets(f.DB, nil).Put(context.Background(), &deployment.Target{Name: "built-in", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	env, err := deployments.EnsureStanding(t.Context(), f.ProjectID, "main", target.ID, (deployment.Attributes{}).WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	outcomes := deployment.NewOutcomes(f.DB, nil)
	at, _, err := outcomes.Admit(context.Background(), deployment.Start{DeploymentID: env.ID, TargetID: target.ID, Key: "serving", Inputs: deployment.Inputs{Revision: sha, ImageDigest: "sha256:released"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outcomes.Complete(context.Background(), deployment.Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := deployments.ReserveHostPort(t.Context(), f.ProjectID, "main", "forge-env-p-main", 40001); err != nil {
		t.Fatal(err)
	}
	if e, err := deployments.ByName(f.Context, f.ProjectID, "main"); err != nil || e.ReleaseSHA != sha || e.ImageDigest == "" || e.Container == "" {
		t.Fatalf("serving main: %+v %v", e, err)
	}
}
