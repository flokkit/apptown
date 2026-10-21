package forge

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
)

// rejectingTransport loses the removal of one preview before it reaches
// the worker.
type reviewPreviewTransport struct {
	base   http.RoundTripper
	reject string
}

func (r reviewPreviewTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodDelete && strings.HasSuffix(req.URL.Path, "/"+r.reject) {
		return nil, fmt.Errorf("review: removal request never reached the worker")
	}
	return r.base.RoundTrip(req)
}

// A workflow's preview step decides whether there is an app from the run's
// commit it boots, never the folder: an uncommitted deletion or a malformed
// uncommitted contract does not skip a committed app, and an uncommitted
// contract over a commit without one boots nothing.
func TestAPreviewStepDecidesFromTheCommitItBoots(t *testing.T) {
	t.Parallel()
	for _, onWorker := range []bool{false, true} {
		t.Run(fmt.Sprint(onWorker), func(t *testing.T) {
			for name, edit := range map[string]string{"deleted": "", "malformed": "app: [this is not a contract"} {
				_, verdict, detail := runtimeGate(t, gatePlace{onWorker: onWorker}, func(_ *App, _ ids.ID, ws string, _ *fakePreviewWorker) {
					path := filepath.Join(ws, ".apptown/app.yaml")
					var err error
					if edit == "" {
						err = os.Remove(path)
					} else {
						err = os.WriteFile(path, []byte(edit), 0o644)
					}
					if err != nil {
						t.Fatal(err)
					}
				}, nil)
				if verdict != "pass" {
					t.Fatalf("an uncommitted contract %s suppressed the committed preview: %s: %s", name, verdict, detail)
				}
			}
			booted := -1
			runtimeGate(t, gatePlace{onWorker: onWorker}, func(_ *App, _ ids.ID, ws string, _ *fakePreviewWorker) {
				path := filepath.Join(ws, ".apptown/app.yaml")
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				commitFolder(t, ws) // the run's commit declares no app
				if err := os.WriteFile(path, body, 0o644); err != nil {
					t.Fatal(err)
				}
			}, func(_ *App, _ ids.ID, _ string, f *fakePreviewWorker) {
				if f == nil {
					return // here, nothing is on a worker to count
				}
				f.mu.Lock()
				booted = len(f.boots)
				f.mu.Unlock()
			})
			if onWorker && booted != 0 {
				t.Fatalf("an uncommitted contract over a commit without one booted %d previews", booted)
			}
		})
	}
}

// Abandoning a change reports a run preview's removal that the worker did
// not confirm, so its job tries again, and a later reconciliation removes it
// once the worker answers, however long the job's tries lasted.
func TestAnAbandonmentKeepsARunPreviewsRemovalUntilItIsMade(t *testing.T) {
	t.Parallel()
	var a *App
	var name string
	_, verdict, detail := runtimeGate(t, gatePlace{onWorker: true}, nil, func(app *App, _ ids.ID, _ string, f *fakePreviewWorker) {
		a = app
		f.mu.Lock()
		name = f.boots[len(f.boots)-1].spec.Name
		f.mu.Unlock()
	})
	if verdict != "pass" {
		t.Fatalf("fixture gate: %s %s", verdict, detail)
	}
	runID, err := ids.Parse(strings.TrimPrefix(name, "apptown-preview-run-"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := workflow.ReadRunIn(context.Background(), a.DB, runID)
	if err != nil {
		t.Fatal(err)
	}
	a.addGrant("user", "reviewer", access.RoleProjectOwner, "", access.ScopeProject, fmt.Sprint(run.ProjectID), "fixture", "fixture", "")
	if _, err := a.Work.Abandon(asPerson("reviewer"), workflow.StopWork{ChangeID: run.ChangeID, Reason: "review cleanup"}); err != nil {
		t.Fatal(err)
	}
	client := a.workerPreviews.Client
	original := client.Transport
	client.Transport = reviewPreviewTransport{base: original, reject: name}
	defer func() { client.Transport = original }()
	_, err = workflowFollowUps(a).Abandon(context.Background(), fmt.Sprintf("change/%d/cleanup", run.ChangeID), map[string]any{"runIds": []ids.ID{runID}})
	state, stateErr := a.workerPreviews.State(context.Background(), docker.PreviewRunName(runID))
	if stateErr != nil || !state.Running {
		t.Fatalf("fixture did not preserve run preview: %+v %v", state, stateErr)
	}
	if err == nil {
		t.Fatal("abandonment acknowledged cleanup while its run preview removal failed and preview still runs")
	}
	client.Transport = original
	if err := a.Preview.ReconcileWorkerPreviews(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state, err := a.workerPreviews.State(context.Background(), docker.PreviewRunName(runID)); err != nil || state.Running {
		t.Fatalf("the run preview was not removed once the worker answered: %+v %v", state, err)
	}
}
