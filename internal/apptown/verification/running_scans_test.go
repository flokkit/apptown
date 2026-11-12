package verification

import (
	"context"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type runningDeploymentsFake struct{ rows []deployment.RunningImages }

func (f runningDeploymentsFake) RunningImages(context.Context, ids.ID) ([]deployment.RunningImages, error) {
	return f.rows, nil
}

type registryCredentialFake struct {
	t       *testing.T
	project ids.ID
}

func (f registryCredentialFake) Registry(_ context.Context, project ids.ID) (*integration.Registry, error) {
	if project != f.project {
		f.t.Fatalf("credential for project %s, wanted %s", project, f.project)
	}
	return &integration.Registry{Host: "registry.example", Username: "project-robot", Token: "private-token"}, nil
}

func TestRunningScansObserveEachDeploymentAndPullEachDigestOnce(t *testing.T) {
	f := newScannerOwnerFixture(t)
	for _, name := range []string{"trivy", "licenses"} {
		f.bind(t, name, map[string]any{"image": "scanner:test", "target": "image", "command": []string{"scan", "${IMAGE_TAR}"}, "report": ".apptown/semgrep.sarif", "format": "sarif", "kind": "vuln"})
	}
	digest := "registry.example/p@sha256:" + strings.Repeat("a", 64)
	deps := runningDeploymentsFake{[]deployment.RunningImages{
		{DeploymentID: idstest.N(1), Deployment: "prod", BundleID: idstest.N(2), Images: map[string]string{"web": digest}},
		{DeploymentID: idstest.N(3), Deployment: "test", BundleID: idstest.N(2), Images: map[string]string{"web": digest}},
		{DeploymentID: idstest.N(4), Deployment: "unknown", Reason: "the running bundle is not known"},
	}}
	pulls, scans := 0, 0
	steps := ScanSteps{db: f.DB, scanners: f.Scanners, deployments: deps, integrations: registryCredentialFake{t, f.ProjectID}, dataDir: t.TempDir(), exec: Executor{
		Image: func(_ context.Context, req execution.ImageRequest) execution.ImageAnswer {
			pulls++
			if req.Pull != digest || req.Registry == nil || req.Registry.Username != "project-robot" || req.Registry.Token != "private-token" || req.AllowBuild {
				t.Fatalf("pull: %+v", req)
			}
			return execution.ImageAnswer{ID: "sha256:local"}
		},
		Run: func(_ context.Context, req execution.Request) execution.Outcome {
			scans++
			if req.ExportImage != digest {
				t.Fatalf("scanned %q, wanted exact running digest %q", req.ExportImage, digest)
			}
			return execution.Outcome{Termination: execution.Exited, ExitCode: 1, Report: []byte(`{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"trivy"}},"results":[{"ruleId":"CVE-RUNNING","level":"error","message":{"text":"vulnerable running package"}}]}]}`)}
		},
	}}
	observations, status, _ := steps.observeRunningImages(t.Context(), &workflow.Run{ID: idstest.N(5), ProjectID: f.ProjectID}, &workflow.Step{ID: idstest.N(6), Config: []byte(`{"on":"running"}`)}, scanConfig{}, nil)
	if pulls != 1 || scans != 2 || len(observations) != 5 || status != workflow.StatusError {
		t.Fatalf("pulls=%d scans=%d observations=%+v status=%s", pulls, scans, observations, status)
	}
	for _, o := range observations {
		if o.Subject.Deployment == "unknown" {
			if o.Verdict != "error" {
				t.Fatalf("unknown deployment passed: %+v", o)
			}
		} else if o.Verdict != "fail" || len(o.Findings) != 1 || o.Subject.Digest != digest {
			t.Fatalf("vulnerable digest became clean: %+v", o)
		}
	}
}

func TestRunningScanWithNoDeploymentsIsNotApplicable(t *testing.T) {
	s := ScanSteps{deployments: runningDeploymentsFake{}}
	observations, status, _ := s.observeRunningImages(t.Context(), &workflow.Run{}, &workflow.Step{}, scanConfig{}, nil)
	if status != workflow.StatusNotApplicable || len(observations) != 1 {
		t.Fatalf("%s %+v", status, observations)
	}
}
