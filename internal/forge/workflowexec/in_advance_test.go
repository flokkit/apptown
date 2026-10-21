package workflowexec

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
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

// openAdoptionWithYes opens an adoption Change as the adoption door does,
// with the opener's yes in advance to the sentences shown.
func openAdoptionWithYes(shown []string, via string) func(*taskForge) (*workflow.Change, error) {
	return func(f *taskForge) (*workflow.Change, error) {
		directory := identity.New(f.db, identity.Config{Projects: project.NewCatalogue(f.db)})
		adoptions := adoption.New(f.db, adoption.Dependencies{Ways: agentCatalog{}, Authority: access.Checked{Authority: directory, Projects: project.New(f.db, directory)}})
		ctx := access.WithCaller(f.ctx, access.Caller{Principal: access.Principal{Kind: "user", Name: taskForgeAdministrator, Via: via, Purpose: access.TokenAgent, Credential: "token:helper"}, Verified: true, Receipt: taskForgeAdministrator})
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
		{"the change has code", []string{sentence}, sentence, true, `[]`, false, "has code of its own"},
		{"the review left a finding", []string{sentence}, sentence, false, `["the README is stale"]`, false, "left 1 finding"},
		{"the acceptance declares no yes in advance", []string{sentence}, "", false, `[]`, false, "says nothing of a yes given in advance"},
		{"the person was shown another sentence", []string{"something else"}, sentence, false, `[]`, false, "was not shown"},
		{"no yes was given", nil, sentence, false, `[]`, false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			a, runID, stepID := admittedAgentStepOn(t, openAdoptionWithYes(c.shown, "session"), `{"prompt":"adopt","output":{"kind":"change-report","file":".apptown/CHANGE.md"}}`,
				schema.Step{Name: "review", Uses: ReviewKind, With: map[string]any{"of": "change-report"}}, accept(c.declared))
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
			if vote.Approver != taskForgeAdministrator || vote.Principal.IsZero() || vote.Via != access.ViaSession || vote.InAdvance != sentence || vote.Candidate == "" || evidence.Means != means {
				t.Fatalf("the vote is not the person's yes in advance as given: %+v (round means %q)", vote, evidence.Means)
			}
			if applied := runIDEvents(t, a, runID, "acceptance.inadvance.applied"); len(applied) != 1 || applied[0]["means"] != sentence {
				t.Fatalf("applied receipts %v", applied)
			}
		})
	}
}
