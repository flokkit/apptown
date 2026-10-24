package verification

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// An exec-image check whose container could not be cleaned up is an error.
func TestAnExecImageCheckWhoseCleanupFailedIsAnError(t *testing.T) {
	run := func(context.Context, execution.Request) execution.Outcome {
		return execution.Outcome{Termination: execution.Exited, ExitCode: 0, CleanupErr: errors.New("container removal failed")}
	}
	j := check.Judge{Scanners: check.ScannersFunc(func(string) (check.Scanner, bool) {
		return check.Scanner{Target: "exec-image", Command: []string{"true"}}, true
	}), RunImage: func(image string, argv, env []string) (int, string) {
		return runInImage(context.Background(), run, image, argv, env)
	}}
	got := j.EvaluateTool(check.Declared{Declaration: check.Declaration{Tool: "app"}}, check.Source{Image: "sha256:app"}, check.Gate, "review")
	if got.Outcome != check.Error {
		t.Fatalf("cleanup failure became %s", got.Outcome)
	}
}

// An exec-image check's receipt names the image that ran.
func TestAnExecImageReceiptNamesTheImageThatRan(t *testing.T) {
	f := newScannerOwnerFixture(t)
	f.bind(t, "app-check", map[string]any{"target": "exec-image", "command": []string{"true"}})
	tree, sha := baselineTree(t, "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n")
	const id = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var ran string
	f.Scanners.exec = Executor{Image: func(context.Context, execution.ImageRequest) execution.ImageAnswer {
		return execution.ImageAnswer{ID: id}
	}, Run: func(_ context.Context, req execution.Request) execution.Outcome {
		ran = req.Image
		return execution.Outcome{Termination: execution.Exited}
	}}
	results, _, errs := f.Scanners.RunToolChecks(t.Context(), []conformance.DeclaredCheck{{Block: "test", Version: 1, Check: check.Declaration{Name: "app", Tool: "app-check"}}}, ToolContext{BuildDir: tree, SHA: sha, ProjectID: f.ProjectID, AllowBuild: true}, "review")
	raw, err := json.Marshal(results)
	if err != nil || errs != 0 || ran != id {
		t.Fatalf("setup: ran=%q errors=%d results=%s marshal=%v", ran, errs, raw, err)
	}
	if !strings.Contains(string(raw), id) {
		t.Fatalf("ran %s, but receipt has no image identity: %s", ran, raw)
	}
}

// An image scan reads the image of the commit it checks.
func TestAnImageScanReadsTheImageOfItsCommit(t *testing.T) {
	cached := map[string]string{}
	builds := 0
	cli := func(_ context.Context, _ io.Reader, args ...string) ([]byte, error) {
		switch args[0] {
		case "image":
			if id := cached[args[len(args)-1]]; id != "" {
				return []byte(`"` + id + `" {}`), nil
			}
			return nil, errors.New("No such image")
		case "build":
			builds++
			body, err := os.ReadFile(filepath.Join(args[len(args)-1], "Dockerfile"))
			if err != nil {
				return nil, err
			}
			cached[args[slices.Index(args, "-t")+1]] = "sha256:" + strings.TrimSpace(string(body))
			return nil, nil
		}
		return nil, errors.New("unexpected Docker command")
	}
	exec := Executor{Image: func(ctx context.Context, req execution.ImageRequest) execution.ImageAnswer {
		return execution.PrepareImage(ctx, cli, req)
	}}
	steps := ScanSteps{exec: exec, scanners: &Scanners{exec: exec}}
	tree, _ := baselineTree(t, "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n")
	if err := os.WriteFile(filepath.Join(tree, "Dockerfile"), []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	first, _, err := steps.imageForScan(t.Context(), &workflow.Run{ID: idstest.N(17)}, tree, "1111111111111111111111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "Dockerfile"), []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	second, _, err := steps.imageForScan(t.Context(), &workflow.Run{ID: idstest.N(17)}, tree, "2222222222222222222222222222222222222222")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("two selected trees in the same run scanned the same cached image: first=%s second=%s builds=%d", first, second, builds)
	}
}

// On a worker, a baseline image that cannot be read is unavailable, never
// clean.
func TestAWorkersImageBaselineIsUnavailableNotClean(t *testing.T) {
	tree, sha := baselineTree(t, "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n")
	images := func(context.Context, execution.ImageRequest) execution.ImageAnswer {
		return execution.ImageAnswer{Err: "worker image store unavailable"}
	}
	// A project that has released: the first-release note does not apply.
	f := newScannerOwnerFixture(t)
	f.Scanners.deployments = deployment.NewDeployments(f.DB)
	servingMain(t, f, f.Scanners.deployments, sha)
	f.Scanners.exec = Executor{Image: images}
	base := f.Scanners.baseImageFingerprints(f.Context, f.ProjectID, tree, sha, check.Scanner{}, "scan", t.TempDir(), nil, "scan")
	if base.OK || !strings.Contains(base.Note, "every finding is treated as introduced") || !strings.Contains(base.Note, "not the worker") {
		t.Fatalf("worker baseline: %+v", base)
	}
	// No deployment/Docker collaborators are supplied: reaching local release
	// reuse or the local serving fallback would panic.
	scanners := &Scanners{exec: Executor{Image: images}}
	scanners.exec.Image = func(_ context.Context, req execution.ImageRequest) execution.ImageAnswer {
		if req.Context == "" {
			return execution.ImageAnswer{}
		}
		if req.AllowBuild {
			t.Fatal("exec-image baseline unexpectedly permitted a build")
		}
		return execution.ImageAnswer{NotBuilt: true}
	}
	ref, _, status, err := scanners.imageForCommit(t.Context(), idstest.N(1), tree, sha, false)
	if ref != "" || status != "deferred" || err != nil {
		t.Fatalf("uncached worker exec-image baseline: %q %q %v", ref, status, err)
	}
}
