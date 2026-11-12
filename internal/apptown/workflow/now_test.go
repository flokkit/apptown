package workflow

import (
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/ids"
)

// Called workflows keep the holding child's identity and common change wording.
func TestWaitingOnNamesRunningWork(t *testing.T) {
	for _, tc := range []struct{ role, kind, want string }{
		{"landing", "build", "checking"},
		{"working", "agent/pi", "building · helper working"},
		{"working", "preview", "checking"},
		{"working", "tests", "checking"},
		{"working", "scan", "checking"},
		{"working", "guidance/conformance", "checking"},
		{"working", "agent/review", "checking"},
		{"landing", "reconcile", "checking"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			root, child := ids.New(time.Now()), ids.New(time.Now())
			project, call := ids.New(time.Now()), ids.New(time.Now())
			g := workflowActivityGraph{
				root: {id: root, projectID: project, state: RunRunning, steps: []workflowActivityStep{{id: call, kind: "workflow/call", state: StatusRunning, evidence: []byte(`{"childRunId":"` + child.String() + `","workflow":"verify"}`)}}},
				child: {id: child, projectID: project, parentID: root, parentStepID: call, state: RunRunning, steps: []workflowActivityStep{
					{state: StatusPass}, {state: StatusPass}, {id: ids.New(time.Now()), name: "security", kind: tc.kind, state: StatusRunning}, {state: StatusPending},
				}},
			}
			a, err := g.classify(root, nil, map[ids.ID]bool{})
			if err != nil {
				t.Fatal(err)
			}
			w := waitingOn(nil, &RunSnapshot{Run: &Run{ID: root, Status: RunRunning}, Definition: &schema.Spec{Role: tc.role}, activity: a})
			if w.Kind != "work" || w.Says != tc.want || w.Run != child || w.Step != g[child].steps[2].id {
				t.Fatalf("waiting = %+v, want %s on child step", w, tc.want)
			}
		})
	}
}
