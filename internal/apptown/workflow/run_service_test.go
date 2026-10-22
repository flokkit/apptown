package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type actorAuthorityFunc func(context.Context, operationPermission) (commandActor, error)

func (f actorAuthorityFunc) Require(ctx context.Context, p operationPermission) (commandActor, error) {
	return f(ctx, p)
}

func TestRunServiceAdmitsScopeBeforeResolutionAndBindsInferenceOwner(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"allow", "deny", "moved", "missing billing identity"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			admissions, req, def := runAdmissionFixture(t)
			db := admissions.records.db
			resolved := 0
			starter := newStarter(db, NewStages(nil, func(string) bool { return false }), nil, startRuntime{
				Source: func(context.Context, StartRequest) (StartSource, error) { return StartSource{}, nil },
				Resolve: func(_ context.Context, in StartRequest, _ StartSource) (PreparedRun, error) {
					resolved++
					if in.RequestedBy != "payer" || in.ReceiptActor != "payer via instrument" || !in.ParentRunID.IsZero() || !in.AcceptedRunID.IsZero() {
						t.Fatalf("authority leaked command identity: %+v", in)
					}
					return def, nil
				},
				Prepare: func(context.Context, *Run) error { return nil }, Dispatch: func(context.Context, *Run) error { return nil },
			}, clocked{})
			auth := actorAuthorityFunc(func(_ context.Context, p operationPermission) (commandActor, error) {
				who := commandActor{Receipt: "payer via instrument", InferenceOwner: "payer"}
				if p.Action == actionIdentify {
					return who, nil
				}
				if !reflect.DeepEqual(p, operationPermission{Action: actionStartRun, ProjectID: req.ProjectID, ChangeID: req.ChangeID}) {
					t.Fatalf("wrong permission: %+v", p)
				}
				switch scenario {
				case "deny":
					return who, &access.Denied{Reason: "no lane permission"}
				case "moved":
					other := workflowTestProject(t, db, "other", "Other", "")
					// sql-guarantee: No owner mutation rehomes a Change; change its project inside authority before resolution to test the admission scope fence.
					completionExec(t, db, `UPDATE changes SET project_id=?,number=number+100000 WHERE id=?`, other, req.ChangeID)
				case "missing billing identity":
					who.InferenceOwner = ""
				}
				return who, nil
			})
			service := newService(db, auth, Runtime{Starter: starter})
			run, err := service.StartRun(context.Background(), StartChangeRun{ChangeID: req.ChangeID, Workflow: req.WorkflowName, Inputs: json.RawMessage(req.InputsJSON)})
			if scenario != "allow" {
				if run != nil || err == nil || resolved != 0 {
					t.Fatalf("unauthorized resolution: %+v %v calls=%d", run, err, resolved)
				}
				wantRunCount(t, db, 0, nil)
				return
			}
			if err != nil || run == nil || resolved != 1 {
				t.Fatalf("start: %+v %v", run, err)
			}
			wantInferenceOwner(t, db, run.ID, "payer")
			wantJSONField(t, "inputs", json.RawMessage(storedRun(t, db, run.ID).InputsJSON), "precise", "9007199254740993")
		})
	}
}

func TestRunServiceRequiresCallerAndDistinctLandingPermission(t *testing.T) {
	t.Parallel()
	for _, call := range []func(*Service) error{
		func(s *Service) error {
			_, err := s.StartRun(context.Background(), StartChangeRun{ChangeID: idstest.N(1), Workflow: "land"})
			return err
		},
		func(s *Service) error { _, err := s.RequestLanding(context.Background(), idstest.N(1)); return err },
	} {
		if err := call(newService(nil, nil, Runtime{})); !errors.Is(err, ErrCallerRequired) {
			t.Fatalf("missing authority: %v", err)
		}
	}
	admissions, req, _ := runAdmissionFixture(t)
	var action commandAction
	s := newService(admissions.records.db, authorityFunc(func(_ context.Context, p operationPermission) (string, error) {
		if p.Action == actionIdentify {
			return "person", nil
		}
		action = p.Action
		return "", &access.Denied{Reason: "landing not allowed"}
	}), Runtime{})
	if run, err := s.RequestLanding(context.Background(), req.ChangeID); run != nil || err == nil || action != actionRequestLanding {
		t.Fatalf("landing permission: %+v %v action=%v", run, err, action)
	}
}

func (f actorAuthorityFunc) VisibleProjects(context.Context, commandAction) ([]ids.ID, error) {
	return nil, ErrCallerRequired
}
