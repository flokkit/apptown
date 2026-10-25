package workflowexec

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/project/repositorylayout"
	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestRelocationPreservesAdmissionsAndRefusesTheirMovedLocalInputs(t *testing.T) {
	owner := idstest.N(1)
	refs := []repositorylayout.Reference{{Owner: owner, Before: "/data/workspaces/project-address", After: "/data/workspaces/project-id"}}
	for _, tc := range []struct {
		name   string
		spec   execution.TaskSpec
		refuse bool
	}{
		{"workspace", execution.TaskSpec{Workspace: "/data/workspaces/project-address"}, true},
		{"mount", execution.TaskSpec{Mounts: []string{"/data/workspaces/project-address:/work:ro"}}, true},
		{"remote source", execution.TaskSpec{Source: &execution.TaskSource{Repository: "/data/workspaces/project-address", Commit: "kept"}, SharedWorkspace: "opaque-step-key"}, true},
		{"unchanged workspace", execution.TaskSpec{Workspace: "/data/workspaces/change-id"}, false},
		{"opaque key", execution.TaskSpec{SharedWorkspace: "/data/workspaces/project-address"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.spec.Request = "immutable-digest"
			req := RelocationRequest{Attempt: tasks.Attempt{Admission: tasks.Admission{Operation: "attempt", Owner: tasks.Owner{ProjectID: owner}, Digest: tc.spec.Request}}, Task: tc.spec}
			before, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			err = req.RefuseMovedPaths(refs, "/data", "/data")
			if (err != nil) != tc.refuse {
				t.Fatalf("refusal %v; want %v", err, tc.refuse)
			}
			after, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("rewrote admitted identity or request")
			}
		})
	}
	req := RelocationRequest{Attempt: tasks.Attempt{Admission: tasks.Admission{Operation: "attempt", Owner: tasks.Owner{ProjectID: owner}}}, Task: execution.TaskSpec{Workspace: "/data/workspaces/change-id"}}
	if err := req.RefuseMovedPaths(refs, "/data", "/restored"); err == nil {
		t.Fatal("resumed a local request at the original restored root")
	}
}

type relocationTaskObserver struct {
	execution.TaskDriver
	observation execution.TaskObservation
	err         error
}

func (o relocationTaskObserver) Observe(context.Context, execution.TaskRef) (execution.TaskObservation, error) {
	return o.observation, o.err
}

func TestRelocationRefusesUnobservedOrSurvivingTaskWriters(t *testing.T) {
	req := RelocationRequest{Attempt: tasks.Attempt{Admission: tasks.Admission{Operation: "op", Backend: "original", Digest: "request"}, ProviderID: "provider-task"}}
	for _, tc := range []struct {
		name   string
		obs    execution.TaskObservation
		err    error
		refuse bool
	}{
		{"absent", execution.TaskObservation{State: execution.TaskAbsent}, nil, false},
		{"stopped own", execution.TaskObservation{State: execution.TaskExited, ID: "provider-task", Request: "request"}, nil, false},
		{"running", execution.TaskObservation{State: execution.TaskRunning, ID: "provider-task"}, nil, true},
		{"foreign", execution.TaskObservation{State: execution.TaskExited, ID: "other", Request: "other"}, nil, true},
		{"unknown", execution.TaskObservation{}, nil, true},
		{"unavailable", execution.TaskObservation{}, errors.New("offline"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := req.RefuseWriter(t.Context(), relocationTaskObserver{observation: tc.obs, err: tc.err}, "original")
			if (err != nil) != tc.refuse {
				t.Fatalf("refusal %v; want %v", err, tc.refuse)
			}
		})
	}
	if err := req.RefuseWriter(t.Context(), relocationTaskObserver{observation: execution.TaskObservation{State: execution.TaskAbsent}}, "another-backend"); err == nil {
		t.Fatal("asked another backend about the old task")
	}
	req.Task.Private = &execution.PrivateDocker{}
	if err := req.RefuseWriter(t.Context(), relocationTaskObserver{observation: execution.TaskObservation{State: execution.TaskAbsent}}, "original"); err == nil {
		t.Fatal("primary absence hid private writers")
	}
}
