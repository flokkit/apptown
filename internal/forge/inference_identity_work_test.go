package forge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

// A step attempt's runtime token opens two doors, the inference gateway and
// the API, through one validation: the MAC names the attempt, and current
// state decides. The same attempt gets the same token after a restart that
// reads the same key; a settled, cancelled or superseded attempt, a disabled
// owner, a read failure or a token whose MAC does not hold opens neither.
//
// One forge holds every scenario. Those that change nothing (a bad MAC, a
// read failure) share one live run; each that ends an attempt or a run has
// its own; the disabled owner, which closes every door, comes last.
func TestStepRuntimeTokenOpensBothDoorsOnlyWhileItsAttemptIsLive(t *testing.T) {
	t.Parallel()
	a, h := internalApp(t)
	_, ctx, ch := changeScenarioIn(t, a, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{{Name: "infer", Uses: "agent/pi"}, {Name: "other", Uses: "agent/pi"}}}}})
	changes := []ids.ID{ch.ID}
	for i := range 4 {
		more, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: ch.ProjectID, Title: fmt.Sprintf("Change %d", i+2)})
		if err != nil {
			t.Fatal(err)
		}
		changes = append(changes, more.ID)
	}
	var runs []*workflow.Run
	for _, change := range changes {
		run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change, Workflow: "test"})
		if err != nil {
			t.Fatal(err)
		}
		runs = append(runs, run)
	}
	a.Work.Wait()
	tasks := map[ids.ID]*workflow.Execution{}
	steps := map[ids.ID]*workflow.Step{}
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Automatic = []string{"agent/pi"}
		deps.Report = func(err error) {
			var verdict *workflow.StepError
			if !errors.As(err, &verdict) {
				t.Error(err)
			}
		}
		deps.Runners = map[string]workflow.Runner{"agent/pi": func(execution *workflow.Execution) {
			step, err := execution.Claim()
			if err != nil {
				t.Fatal(err)
			}
			tasks[step.ID], steps[step.ID] = execution, step
		}}
	})
	a.Work.Stop()
	// Each run's two steps are claimed; first is the one whose token is tried.
	type attempt struct {
		run           *workflow.Run
		first, second ids.ID
		step          *workflow.Step
		token         string
	}
	attempts := make([]attempt, len(runs))
	for i, run := range runs {
		snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
		if err != nil || len(snapshot.Steps) != 2 {
			t.Fatalf("inference steps: %+v %v", snapshot, err)
		}
		first, second := snapshot.Steps[0].ID, snapshot.Steps[1].ID
		for _, id := range []ids.ID{first, second} {
			if err := a.Work.ExecuteStep(ctx, id); err != nil {
				t.Fatal(err)
			}
		}
		step := steps[first]
		if step == nil || steps[second] == nil {
			t.Fatal("runner did not claim both steps")
		}
		attempts[i] = attempt{run: run, first: first, second: second, step: step, token: a.runTok.Step(step.ID, step.Attempt)}
	}
	shared := attempts[0]
	for _, c := range []struct {
		scenario string
		on       attempt
	}{
		{"live", shared}, {"apptownd mac", shared}, {"another step", shared}, {"another key", shared},
		{"work read failed", shared}, {"account read failed", shared},
		{"settled attempt", attempts[1]}, {"finished run", attempts[2]}, {"cancelled run", attempts[3]}, {"superseded attempt", attempts[4]},
		// The refusals above left the shared attempt live.
		{"still live", shared},
		{"disabled owner", shared},
	} {
		t.Run(c.scenario, func(t *testing.T) {
			scenario, run, first, second, step, token := c.scenario, c.on.run, c.on.first, c.on.second, c.on.step, c.on.token
			switch scenario {
			case "settled attempt", "finished run":
				for _, id := range []ids.ID{first, second} {
					if err := tasks[id].Finish(workflow.StepOutcome{Status: workflow.StatusPass}, nil); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "finished run" {
					result, err := a.Work.Advance(ctx, run.ID)
					if err != nil || result.RunStatus != workflow.RunDone {
						t.Fatalf("finish run: %+v %v", result, err)
					}
				}
			case "cancelled run":
				if _, err := a.Work.Abandon(a.asEngine(ctx), workflow.StopWork{RunID: run.ID, Reason: "test"}); err != nil {
					t.Fatal(err)
				}
			case "superseded attempt":
				if err := tasks[second].Finish(workflow.StepOutcome{Status: workflow.StatusPass}, nil); err != nil {
					t.Fatal(err)
				}
				if err := tasks[first].Finish(workflow.StepOutcome{Status: workflow.StatusError}, nil); err != nil {
					t.Fatal(err)
				}
				if _, err := a.Work.RetryStep(ctx, workflow.RetryCommand{StepID: first}); err != nil {
					t.Fatal(err)
				}
				if err := a.Work.ExecuteStep(ctx, first); err != nil {
					t.Fatal(err)
				}
				if steps[first].Attempt != step.Attempt+1 {
					t.Fatalf("newer attempt: %+v", steps[first])
				}
				// The newer attempt is live and admitted; the older is not.
				if who, err := inferenceIdentityFor(a).Authenticate(ctx, a.runTok.Step(first, steps[first].Attempt)); err != nil || who.Attempt != step.Attempt+1 {
					t.Fatalf("live newer attempt refused: %+v %v", who, err)
				}
			case "disabled owner":
				testAdministrator(t, a, "admin")
				disabled := true
				if err := a.Access.SaveUser(ctx, identity.AccountChange{Username: "alice", Disabled: &disabled}, access.Principal{Kind: "user", Name: "admin", Via: "session"}); err != nil {
					t.Fatal(err)
				}
			case "apptownd mac":
				flipped := "0"
				if strings.HasSuffix(token, "0") {
					flipped = "1"
				}
				token = token[:len(token)-1] + flipped
			case "another step":
				// The other step is live too; this token's MAC is for the first.
				mac := token[strings.LastIndex(token, "_")+1:]
				token = fmt.Sprintf("rt_%d_%d_%s", second, steps[second].Attempt, mac)
			case "another key":
				other := &identity.RuntimeTokens{}
				if err := other.Open(filepath.Join(t.TempDir(), identity.RuntimeTokenKeyFile)); err != nil {
					t.Fatal(err)
				}
				token = other.Step(step.ID, step.Attempt)
			}
			failing := map[string]string{"work read failed": "-- name: StepAttemptObservation :one", "account read failed": "-- name: UserByPrincipal :one"}[scenario]
			if failing != "" {
				dbtest.FailNextStatement(t, a.DB, failing)
			}
			got, err := inferenceIdentityFor(a).Authenticate(ctx, token)
			if scenario != "live" && scenario != "still live" {
				if err == nil {
					t.Fatalf("inference door admitted: %+v", got)
				}
				if failing != "" {
					dbtest.FailNextStatement(t, a.DB, failing)
				}
				if code, body := stepTokenAPIDoor(h, token); code != 401 {
					t.Fatalf("API door admitted: %d %s", code, body)
				}
				return
			}
			if err != nil || got.Actor != "alice" || got.ProjectID != run.ProjectID || got.RunID != run.ID || got.StepID != step.ID || got.Attempt != step.Attempt {
				t.Fatalf("inference door: %+v %v", got, err)
			}
			if code, body := stepTokenAPIDoor(h, token); code != 200 || !strings.Contains(body, fmt.Sprintf(`"run/%d"`, run.ID)) {
				t.Fatalf("API door: %d %s", code, body)
			}
			if scenario == "still live" {
				return
			}
			// A restarted forge reads the same key: the attempt keeps its token
			// and both doors still open for it.
			restarted := newApp(t, a.DB, Config{DataDir: a.cfg.dataDir, AuthMode: "internal"})
			restarted.Work.Stop()
			if again := restarted.runTok.Step(step.ID, step.Attempt); again != token {
				t.Fatalf("token changed across restart: %s then %s", token, again)
			}
			if got, err := inferenceIdentityFor(restarted).Authenticate(ctx, token); err != nil || got.RunID != run.ID {
				t.Fatalf("inference door after restart: %+v %v", got, err)
			}
			if code, body := stepTokenAPIDoor(restarted.Handler(), token); code != 200 || !strings.Contains(body, fmt.Sprintf(`"run/%d"`, run.ID)) {
				t.Fatalf("API door after restart: %d %s", code, body)
			}
		})
	}
}

// stepTokenAPIDoor asks the API who the token's holder is, as the CLI does.
func stepTokenAPIDoor(h http.Handler, token string) (int, string) {
	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req.Header.Set("X-Forge-Session-Token", token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

// Work owns historical run-owner lookup (including absent and blank records).
// Forge owns normalization and account admission after receiving that identity.
func TestInferenceAttributionAllowsServiceIdentitiesAndNormalizesEmptyOwner(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	for actor, want := range map[string]string{"": "system", "run/123": "run/123"} {
		token := a.runTok.Mint(identity.Workload{Kind: "assist", Actor: actor})
		got, err := inferenceIdentityFor(a).Authenticate(context.Background(), token)
		if err != nil || got.Actor != want {
			t.Fatalf("attribution %q: %+v %v", actor, got, err)
		}
		a.runTok.Revoke(token)
	}
	// A process-local grant never stands in for a step: one naming a run
	// would skip the step attempt's checks, so it is refused.
	token := a.runTok.Mint(identity.Workload{Kind: "assist", Actor: "alice", RunID: idstest.N(1), ProjectID: idstest.N(1)})
	defer a.runTok.Revoke(token)
	if got, err := inferenceIdentityFor(a).Authenticate(context.Background(), token); err == nil {
		t.Fatalf("a grant naming a run was admitted: %+v", got)
	}
}
