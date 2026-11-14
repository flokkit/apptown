package workflowexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
)

// stockInAdvance reads the sentence stock quick-change's acceptance gives a
// yes in advance, so the test exercises the shipped meaning.
func stockInAdvance(t *testing.T) (means, inAdvance string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "stock", "quick-change.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := schema.ParseWorkflow(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range doc.Spec.Stages {
		for _, step := range stage.Steps {
			if step.Name == "accept" {
				means, _ = step.With["means"].(string)
				inAdvance, _ = step.With["inAdvance"].(string)
			}
		}
	}
	if inAdvance == "" {
		t.Fatal("stock quick-change's accept gives no yes in advance")
	}
	return means, inAdvance
}

// InAdvance supplies the fixture's declared landing sentences, just as the
// adopted procedure owner supplies them to the assembled application.
func (f *taskAppTown) InAdvance(context.Context, adoption.Candidate) (workflow.InAdvanceQuestion, error) {
	q := workflow.InAdvanceQuestion{}
	for _, stage := range f.definitions["land"].Stages {
		for _, step := range stage.Steps {
			if sentence, _ := step.With["inAdvance"].(string); sentence != "" {
				q.Landing = append(q.Landing, sentence)
			}
		}
	}
	return q, nil
}

func (f *taskAppTown) CanLand(context.Context, ids.ID) (bool, error) {
	_, ok := f.definitions["land"]
	return ok, nil
}

// openAdoptionWithYes opens an adoption Change as the adoption door does,
// with the opener's yes in advance to the sentences shown.
func openAdoptionWithYes(shown []string, via string) func(*taskAppTown) (*workflow.Change, error) {
	return func(f *taskAppTown) (*workflow.Change, error) {
		directory := identity.New(f.db, identity.Config{Projects: project.NewCatalogue(f.db)})
		adoptions := adoption.New(f.db, adoption.Dependencies{Ways: agentCatalog{}, Authority: access.Checked{Authority: directory, Projects: project.New(f.db, directory)}})
		ctx := access.WithCaller(f.ctx, access.Caller{Principal: access.Principal{Kind: "user", Name: taskAppTownAdministrator, Via: via, Purpose: access.TokenAgent, Credential: "token:helper"}, Verified: true, Receipt: taskAppTownAdministrator})
		c, err := adoptions.Prepare(ctx, f.projectID, adoption.Request{})
		if err != nil {
			return nil, err
		}
		var given *workflow.InAdvanceGiven
		if shown != nil {
			given = &workflow.InAdvanceGiven{Workflow: "agent", Shown: shown}
		}
		return f.work.Uplift(ctx, workflow.Uplift{ProjectID: f.projectID, ExpectedRevision: c.ExpectedRevision, Acceptance: workflow.Acceptance{TargetDigest: c.Digest},
			Words: []string{"the fixture's own"}, Target: adoptionTarget{adoptions, *c}, InAdvance: given})
	}
}

// A person who opens an adoption may say yes in advance, to the sentence
// the acceptance declares for one. AppTown casts it as their vote only
// where the adoption needed no code and every step passed outright; then
// the vote records the sentence it meant, the person and the commit.
// Anything else leaves the acceptance waiting for a person, and says why.
//
// Kills: casting without the condition (code, a finding), casting for a
// step that declares no such sentence or one the person was not shown, and
// recording the round's sentence as what the yes meant.
func TestAYesInAdvanceAcceptsAnAdoptionThatNeedsNoCode(t *testing.T) {
	t.Parallel()
	means, sentence := stockInAdvance(t)
	landBody, err := os.ReadFile(filepath.Join("..", "..", "..", "stock", "land.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	land, err := schema.ParseWorkflow(landBody)
	if err != nil {
		t.Fatal(err)
	}
	var landingApproval schema.Step
	for _, stage := range land.Spec.Stages {
		for _, step := range stage.Steps {
			if step.Name == "approve-landing" {
				landingApproval = step
			}
		}
	}
	landingSentence, _ := landingApproval.With["inAdvance"].(string)
	if landingSentence == "" {
		t.Fatal("stock landing declares no yes in advance")
	}
	accept := func(inAdvance string) schema.Step {
		with := map[string]any{"approvers": []any{"role:project-approver"}, "means": means}
		if inAdvance != "" {
			with["inAdvance"] = inAdvance
		}
		return schema.Step{Name: "accept", Uses: "approval", With: with}
	}
	for _, c := range []struct {
		name     string
		shown    []string
		declared string
		code     bool
		findings string
		cast     bool
		withheld string
	}{
		{"needs no code, every step passed", []string{sentence}, sentence, false, `[]`, true, ""},
		{"static site container checks are not applicable", []string{sentence, landingSentence}, sentence, false, `[]`, true, ""},
		{"the change has code", []string{sentence}, sentence, true, `[]`, false, "has code of its own"},
		{"the review left a finding", []string{sentence}, sentence, false, `["the README is stale"]`, false, "left 1 finding"},
		{"the acceptance declares no yes in advance", []string{sentence}, "", false, `[]`, false, "says nothing of a yes given in advance"},
		{"the person was shown another sentence", []string{"something else"}, sentence, false, `[]`, false, "was not shown"},
		{"no yes was given", nil, sentence, false, `[]`, false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			also := []schema.Step{{Name: "review", Uses: ReviewKind, With: map[string]any{"of": "change-report"}}, accept(c.declared)}
			if strings.HasPrefix(c.name, "static site") {
				also = append([]schema.Step{{Name: "container-checks", Uses: "scan", With: map[string]any{"on": "image"}}}, also...)
			}
			a, runID, stepID := admittedAgentStepOn(t, openAdoptionWithYes(c.shown, "session"), `{"prompt":"adopt","output":{"kind":"change-report","file":".apptown/CHANGE.md"}}`, also...)
			if strings.HasPrefix(c.name, "static site") {
				a.definitions["land"] = schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{
					{Name: "gates", Steps: []schema.Step{{Name: "container-checks", Uses: "scan", With: map[string]any{"on": "image"}}}},
					{Name: "approval", Steps: []schema.Step{landingApproval}},
				}}
				a.scan = func(e *workflow.Execution) {
					if _, err := e.Claim(); err != nil {
						return
					}
					var checks []workflow.CheckReceipt
					for _, name := range []string{"readiness-declared", "memory-declared", "container-image"} {
						checks = append(checks, workflow.CheckReceipt{Check: name, Verdict: workflow.VerdictNotApplicable, Detail: "the app declares only a static site"})
					}
					pin, _, err := workflow.LandingCandidateIn(e.Context(), a.db, e.Run().ID)
					if err != nil {
						t.Fatal(err)
					}
					e.Settle((workflow.CheckEvidence{Checks: checks}).Outcome(workflow.StatusNotApplicable, map[string]any{"candidate": pin.Candidate}))
				}
			}

			installFakePi(a, func(n int, req execution.Request) execution.Outcome {
				write := func(name, body string) {
					if err := os.WriteFile(filepath.Join(req.Workspace, name), []byte(body), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				if n == 1 {
					write(".apptown/CHANGE.md", "Nothing to change: every new check already holds.")
					if c.code {
						write("page.txt", "changed\n")
						write(".apptown/CHANGE.md", "CHANGED page.txt to make a check hold.")
					}
				} else {
					write(".apptown/review.json", `{"verdict":"pass","summary":"checked","findings":`+c.findings+`}`)
				}
				return execution.Outcome{Output: piSays("done")}
			})
			if err := a.work.ExecuteStep(a.ctx, stepID); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(c.name, "static site") {
				if err := a.work.ExecuteStep(a.ctx, pendingStepNamed(t, a, runID, "container-checks")); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.work.ExecuteStep(a.ctx, pendingStepNamed(t, a, runID, "review")); err != nil {
				t.Fatal(err)
			}
			if _, err := a.work.Advance(a.ctx, runID); err != nil {
				t.Fatal(err)
			}
			snapshot, err := a.work.Snapshot(a.ctx, runID, workflow.DetailView)
			if err != nil {
				t.Fatal(err)
			}
			var step *workflow.Step
			for _, s := range snapshot.Steps {
				if s.Name == "accept" {
					step = s
				}
			}
			if step == nil {
				t.Fatal("no accept step")
			}
			if cast := step.Status == workflow.StatusPass; cast != c.cast {
				t.Fatalf("accept is %s, want cast=%v", step.Status, c.cast)
			}
			withheld := runIDEvents(t, a, runID, "acceptance.inadvance.withheld")
			if c.withheld == "" && len(withheld) != 0 || c.withheld != "" && (len(withheld) != 1 || !strings.Contains(fmt.Sprint(withheld[0]["reason"]), c.withheld)) {
				t.Fatalf("withheld receipts %v, want one saying %q", withheld, c.withheld)
			}
			if !c.cast {
				return
			}
			evidence, err := workflow.ReadApprovalEvidence(step.Evidence)
			if err != nil || len(evidence.Decisions) != 1 {
				t.Fatalf("approval evidence %s: %v", step.Evidence, err)
			}
			vote := evidence.Decisions[0]
			if vote.Approver != taskAppTownAdministrator || vote.Principal.IsZero() || vote.Via != access.ViaSession || vote.InAdvance != sentence || vote.Candidate == "" || evidence.Means != means {
				t.Fatalf("the vote is not the person's yes in advance as given: %+v (round means %q)", vote, evidence.Means)
			}
			if applied := runIDEvents(t, a, runID, "acceptance.inadvance.applied"); len(applied) != 1 || applied[0]["means"] != sentence {
				t.Fatalf("applied receipts %v", applied)
			}
			if strings.HasPrefix(c.name, "static site") {
				if len(vote.AlsoApproves) != 1 || vote.AlsoApproves[0] != landingSentence {
					t.Fatalf("landing sentence not retained: %+v", vote)
				}
				if _, err := a.work.Advance(a.ctx, runID); err != nil {
					t.Fatal(err)
				}
				landing, err := a.work.AdvanceAcceptedLanding(a.ctx, runID, a)
				if errors.Is(err, workflow.ErrDispatchStopped) {
					// This fixture executes steps explicitly rather than running a dispatcher.
					landing, err = a.work.AdvanceAcceptedLanding(a.ctx, runID, a)
				}
				if err != nil {
					t.Fatalf("request landing: %+v %v", landing, err)
				}
				if err := a.work.ExecuteStep(a.ctx, pendingStepNamed(t, a, landing.RunID, "container-checks")); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 3; i++ {
					if _, err := a.work.Advance(a.ctx, landing.RunID); err != nil {
						t.Fatal(err)
					}
				}
				landed, err := a.work.Change(a.ctx, snapshot.Run.ChangeID)
				if err != nil || landed.State != workflow.ChangeShipped {
					snap, _ := a.work.Snapshot(a.ctx, landing.RunID, workflow.DetailView)
					t.Fatalf("static adoption did not land on the person's yes: %+v %v snapshot=%+v", landed, err, snap)
				}
				result, err := a.work.Snapshot(a.ctx, landing.RunID, workflow.DetailView)
				if err != nil {
					t.Fatal(err)
				}
				for _, step := range result.Steps {
					if step.Name == "approve-landing" {
						ev, err := workflow.ReadApprovalEvidence(step.Evidence)
						if err != nil || len(ev.Decisions) != 1 || ev.Decisions[0].Approver != taskAppTownAdministrator || ev.Decisions[0].InAdvance != landingSentence {
							t.Fatalf("landing lost the person's yes: %s %v", step.Evidence, err)
						}
					}
				}
			}
		})
	}
}
