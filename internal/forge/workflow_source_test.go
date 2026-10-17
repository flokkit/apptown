package forge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/httpapi"

	"flokkit.io/forge/internal/forge/workflow"
)

func workflowSourceForTest(t *testing.T, a *App, run *workflow.Run) workflow.Source {
	t.Helper()
	source, err := workflow.ReadSourceIn(context.Background(), a.DB, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func TestWorkspaceReadFailureCannotPassRunnerOrServeFiles(t *testing.T) {
	t.Parallel()
	for _, typ := range []string{"script", "tests", "preview", "deploy", "reconcile"} {
		t.Run(typ, func(t *testing.T) {
			t.Parallel()
			a, run, step := attemptFixture(t, typ)
			// Admission reads the source to capture who holds the branch. If
			// that read fails, the attempt is not admitted: nothing runs.
			dbtest.FailNextStatement(t, a.DB, "name: WorkflowSource")
			a.Work.ExecuteStep(a.asEngine(context.Background()), step.ID)
			if unstarted, err := workflow.ReadStepIn(context.Background(), a.DB, step.ID); err != nil || unstarted.Status != workflow.StatusPending {
				t.Fatalf("a failed admission read left the step %+v, %v; want it pending", unstarted, err)
			}
			if current, err := workflow.ReadRunIn(context.Background(), a.DB, run.ID); err != nil || current.Status != workflow.RunRunning {
				t.Fatalf("a failed admission read settled the run: %+v %v", current, err)
			}
			// Admitted, the runner's own read fails: the step errors, never passes.
			// A script reads its folder once more before its claim (to wait
			// for the folder), so its runner's read comes one later.
			skip := 1
			if typ == "script" {
				skip = 2
			}
			t.Cleanup(a.DB.FailStatementAfter("name: WorkflowSource", skip))
			a.Work.ExecuteStep(a.asEngine(context.Background()), step.ID)
			requireSteps(t, a, 1, func(s *workflow.Step) bool {
				return s.ID == step.ID && s.Status == workflow.StatusError && strings.Contains(evidenceField(s, "reason"), "reading workflow source")
			})
			requireEvents(t, a, 0, eventKind("run.done"))
			dbtest.FailNextStatement(t, a.DB, "name: WorkflowSource")
			r := httptest.NewRequest("GET", "/workspace/", nil)
			r.SetPathValue("id", fmt.Sprint(run.ID))
			w := httptest.NewRecorder()
			httpapi.NewWorkspaces(a.DB, a.cfg.dataDir).Workspace(w, r.WithContext(callerContext(r)))
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("unavailable source served a directory: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
