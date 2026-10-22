package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// A workflow whose model steps would have no model is refused at admission,
// before a run exists, with the port's words; once the port has no
// objection the same start is admitted. The port is asked about the
// definition being admitted and the request's inputs (a catalog pin).
//
// Kills: admission that never asks, which started an agent with no key on a
// fresh AppTown only for it to fail with its own "/login" advice.
func TestAWorkflowWithNoModelIsRefusedBeforeItsRunExists(t *testing.T) {
	t.Parallel()
	admissions, req, def := runAdmissionFixture(t)
	db := admissions.records.db
	refusal := "no AI model is set up"
	var asked *schema.Spec
	var inputs string
	starter := newStarter(db, NewStages(nil, func(string) bool { return false }), nil, startRuntime{
		Source:  func(context.Context, StartRequest) (StartSource, error) { return StartSource{}, nil },
		Resolve: func(context.Context, StartRequest, StartSource) (PreparedRun, error) { return def, nil },
		NoModel: func(_ context.Context, projectID ids.ID, spec *schema.Spec, inputsJSON string) string {
			if projectID != req.ProjectID {
				t.Errorf("asked about project %s, not %s", projectID, req.ProjectID)
			}
			asked, inputs = spec, inputsJSON
			return refusal
		},
		Prepare:  func(context.Context, *Run) error { return nil },
		Dispatch: func(context.Context, *Run) error { return nil },
	}, clocked{})
	service := newService(db, authorityFunc(func(context.Context, operationPermission) (string, error) { return "operator", nil }), Runtime{Starter: starter})
	start := StartChangeRun{ChangeID: req.ChangeID, Workflow: req.WorkflowName, Inputs: []byte(req.InputsJSON)}
	_, err := service.StartRun(context.Background(), start)
	if err == nil || !strings.Contains(err.Error(), refusal) || fault.KindOf(err) != fault.Unprocessable {
		t.Fatalf("a workflow with no model was not refused as unprocessable: %v", err)
	}
	if asked == nil || asked.Stages[0].Name != def.Spec.Stages[0].Name || inputs != req.InputsJSON {
		t.Fatalf("the port was asked about %+v with inputs %q", asked, inputs)
	}
	if run, err := admissions.records.existing(context.Background(), req); err != nil || run != nil {
		t.Fatalf("a refused start left a run: %+v %v", run, err)
	}
	refusal = ""
	if _, err := service.StartRun(context.Background(), start); err != nil {
		t.Fatalf("a workflow whose model steps have models was refused: %v", err)
	}
}
