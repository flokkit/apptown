package apptown

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/fault"
)

// A running agent or review step can be stopped through the product: its
// containers are removed and the run records who did it (complaint #71).
// Stopping a step is working on the change: a person who may only look at
// the project is refused before anything is stopped.
func TestStepInterruptStopsTheStepsContainers(t *testing.T) {
	t.Parallel()
	f := newFakeDocker()
	a := agentTestApp(t, Config{DockerClient: f})
	testAdministrator(t, a, "bls")
	runID, stepID := admittedRunnerStep(t, a, "ir", "review", "agent/review", false)
	f.reply = func(args []string) (string, error, bool) {
		if len(args) > 0 && args[0] == "ps" {
			for _, x := range args {
				if strings.HasPrefix(x, "name=apptown-review-") {
					return fmt.Sprintf("apptown-review-%d-63865\n", stepID), nil, true
				}
			}
			return "", nil, true
		}
		return "", nil, false
	}
	h := a.Handler()
	p, err := a.Projects.BySlug(context.Background(), "ir")
	if err != nil {
		t.Fatal(err)
	}
	for who, role := range map[string]string{"outsider": "", "vic": access.RoleProjectViewer, "jenny": access.RoleProjectContrib} {
		if _, err := a.Access.EnsureUser(context.Background(), who); err != nil {
			t.Fatal(err)
		}
		if role != "" && a.addGrant("user", who, role, "", access.ScopeProject, fmt.Sprint(p.ID), "fixture", "bls", "").IsZero() {
			t.Fatalf("granting %s", who)
		}
	}
	before := len(f.joined())
	for _, who := range []string{"outsider", "vic"} {
		denied := authzCall(h, who, "POST", fmt.Sprintf("/api/v1/steps/%d/interrupt", stepID), `{}`)
		if denied.Code != 403 || len(f.joined()) != before {
			t.Fatalf("%s's interruption reached Docker: %d %s calls=%v", who, denied.Code, denied.Body.String(), f.joined())
		}
	}
	// Work refuses a viewer itself, whichever door asks.
	if _, err := a.Work.InterruptStep(asPerson("vic"), stepID, func(context.Context, *workflow.Step) ([]string, error) {
		t.Fatal("a viewer's interruption reached the stop")
		return nil, nil
	}); fault.KindOf(err) != fault.Forbidden {
		t.Fatalf("Work let a viewer stop a step: %v", err)
	}
	w := authzCall(h, "jenny", "POST", fmt.Sprintf("/api/v1/steps/%d/interrupt", stepID), `{}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), fmt.Sprintf("apptown-review-%d-63865", stepID)) {
		t.Fatalf("interrupt: %d %s", w.Code, w.Body.String())
	}
	if idx := f.indexOf("rm -f -v apptown-review-"); idx < 0 {
		t.Fatalf("the container was not removed: %v", f.joined())
	}
	snap, err := a.Work.Snapshot(asPerson("bls"), runID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	event, err := a.Work.LatestChangeEvent(asPerson("bls"), snap.Run.ChangeID, "step.interrupted")
	if err != nil || event == nil || event.RunID != runID || !strings.Contains(string(event.Payload), `"by":"jenny"`) {
		t.Fatalf("the run records who stopped the step: %+v %v", event, err)
	}
	// A step that is not running cannot be interrupted.
	_, done := admittedRunnerStep(t, a, "ir-done", "review2", "agent/review", true)
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/steps/%d/interrupt", done), strings.NewReader("{}"))
	req.Header.Set("X-AppTown-Actor", "bls")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 409 {
		t.Fatalf("a finished step: %d %s", w.Code, w.Body.String())
	}
}
