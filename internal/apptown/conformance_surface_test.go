package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
)

// Conformance (design/subsystems/conformance-surface.md):
// tree rubrics are judged in a review and at the gate on the same packed
// evidence. The lens, delta and expired-exception projections are the
// conformance owner's (conformance/review_projections_test.go).

func stubLLM(t *testing.T, a *App, answer string, seen *[]string) {
	t.Helper()
	installCheckEffects(t, a, checkOverrides{complete: func(pattern, system, user string) (string, *modelcall.Usage, error) {
		if seen != nil {
			*seen = append(*seen, user)
		}
		return answer, &modelcall.Usage{Prompt: 10, Completion: 5, Cost: 0.001, Model: "vendor/test-model-2026-01-01"}, nil
	}})
}

func bindFastSlot(t *testing.T, a *App) {
	t.Helper()
	if err := a.Models.Set(context.Background(), "fast", "standard", "openrouter/test-model", "fixture"); err != nil {
		t.Fatal(err)
	}
}

func treeRubric(name, rubric string, files ...string) string {
	fs := make([]string, len(files))
	for i, f := range files {
		fs[i] = `"` + f + `"`
	}
	return `{"appliesTo": ["repository"], "name":"` + name + `","evaluator":"llm","scope":"tree","files":[` + strings.Join(fs, ",") + `],"rubric":"` + rubric + `","expects":"the rule holds","severity":"medium"}`
}

func standingOf(t *testing.T, view *conformance.View, slug string) conformance.ProjectStanding {
	t.Helper()
	for _, p := range view.Projects {
		if p.Slug == slug {
			return p
		}
	}
	t.Fatalf("no standing for %s in view", slug)
	return conformance.ProjectStanding{}
}

// A tree rubric is judged in a review and at the gate from the SAME packed
// evidence (same evidence hash, same evaluator identity); a diff rubric is
// not-applicable in a review; the verdict names the served model.
func TestTreeRubricJudgedInReviewAndAtGate(t *testing.T) {
	t.Parallel()
	a := conformanceTestApp(t)
	tgLocalSh(t, a)
	bindFastSlot(t, a)
	pid, _, _ := fleetRepo(t, a, "proj", map[string]string{
		"README.md":                "# proj\nA marketing-free readme.\n",
		"src/urls.py":              "urlpatterns = [path('admin/', admin.site.urls)]\n",
		"node_modules/x/README.md": "must never be evidence",
	})
	adopt(t, a, pid, "prose", 3, checksJSON(
		treeRubric("readme-plain", "README is plain", "README.md"),
		`{"appliesTo": ["repository"], "name":"tone-of-change","evaluator":"llm","rubric":"the diff reads well","expects":"the rule holds"}`,
	))
	var prompts []string
	stubLLM(t, a, `[{"item":1,"verdict":"fail","evidence":"README.md:2","note":"reads like marketing"}]`, &prompts)

	rev, vs, err := fleetReviewsFor(a).RunFleetReviewWith(testConformanceContext("t"), "manual", conformance.ReviewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]conformance.Verdict{}
	for _, v := range vs {
		byName[v.Check] = v
	}
	rp, ok := byName["readme-plain"]
	if !ok || rp.State != conformance.DoesNotConform {
		t.Fatalf("tree rubric should be judged (fail → does-not-conform), got %+v", rp)
	}
	if !strings.HasPrefix(rp.EvaluatorVer, "llm/"+check.PackingVersion+"/") || rp.EvidenceHash == "" {
		t.Fatalf("AI verdict must carry evaluator identity + evidence hash: %+v", rp)
	}
	if !strings.Contains(rp.Detail, "judged by vendor/test-model-2026-01-01") || !strings.Contains(rp.Detail, "README.md") {
		t.Fatalf("detail must name the served model and the files judged: %q", rp.Detail)
	}
	if tc := byName["tone-of-change"]; tc.State != conformance.NotApplicable {
		t.Fatalf("diff rubric in a review must be not-applicable, got %+v", tc)
	}
	if len(prompts) != 1 || !strings.Contains(prompts[0], "marketing-free readme") || strings.Contains(prompts[0], "must never be evidence") {
		t.Fatalf("the prompt must carry the packed file and exclude node_modules: %d prompts", len(prompts))
	}
	if rev.Cost <= 0 {
		t.Fatalf("review should record model spend, got %v", rev.Cost)
	}

	// The admitted gate records the same rubric identity as the fleet review.
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "check", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "check", Steps: []schema.Step{{Name: "guidance", Uses: "guidance/conformance"}}}}}})
	recordFixtureAdoptions(t, a)
	ctx := a.asEngine(context.Background())
	change, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: pid, Title: "check rubric identity"})
	if err != nil {
		t.Fatal(err)
	}
	a.Work.Stop()
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "check"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 1 {
		t.Fatalf("gate admission: %+v %v", snapshot, err)
	}
	if err := mirrorBranchForTest(a.projectRepoPath("proj"), (workflow.Layout{DataDir: a.cfg.dataDir}).ChangeWorkspace(change.ID), change.Branch); err != nil {
		t.Fatal(err)
	}
	if err := a.Work.ExecuteStep(ctx, snapshot.Steps[0].ID); err != nil {
		t.Fatal(err)
	}
	saved, err := workflow.ReadStepIn(ctx, a.DB, snapshot.Steps[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		Checks verification.Results
		LLM    map[string]any
	}
	if err := json.Unmarshal(saved.Evidence, &evidence); err != nil {
		t.Fatal(err)
	}
	var result workflow.CheckReceipt
	for _, r := range evidence.Checks {
		if r.Check == "readme-plain" {
			result = r
		}
	}
	if result.Verdict != workflow.VerdictFail {
		t.Fatalf("gate should judge the tree rubric: %s", saved.Evidence)
	}
	if result.EvidenceHash != rp.EvidenceHash || result.EvaluatorVer != rp.EvaluatorVer {
		t.Fatalf("gate and review must ask the same question: gate %v/%v vs review %s/%s", result.EvidenceHash, result.EvaluatorVer, rp.EvidenceHash, rp.EvaluatorVer)
	}
	if evidence.LLM["packing"] != check.PackingVersion {
		t.Fatalf("gate meta should record packing: %+v", evidence.LLM)
	}

	// A view under the current lens: the AI fail is FLAGGED, not a finding.
	view, _ := a.conformance.
		View(testConformanceContext("t"), ids.Nil)
	ps := standingOf(t, view, "proj")
	if ps.Lanes.Flagged != 1 || ps.Lanes.Findings != 0 || ps.Standing != conformance.StandingNeedsLook || ps.Lanes.NA != 1 || ps.Total != 1 {
		t.Fatalf("unconfirmed AI fail → flagged / needs-a-look, n/a outside M: %+v", ps)
	}
	_ = rev
}

func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeRepoFile(t *testing.T, a *App, slug, rel, body string) {
	t.Helper()
	writeFile(t, a.projectRepoPath(slug), rel, body)
}

// A judge that answers in prose once is asked again for the JSON array;
// only a second failure records cannot-assess.

// A model rubric the change fails is judged on main's tree too, the change's
// base: failing there as well, the finding is preexisting (main's, not the
// change's); passing there, the change introduced it. One more checklist
// call, for the failing rubric only.
//
// Kills: a model finding labelled introduced when main already failed it
// (the seed's problem blamed on the change); a base call that judges the
// change's tree again instead of main's.
func TestAModelRubricIsComparedWithMain(t *testing.T) {
	t.Parallel()
	a := conformanceTestApp(t)
	tgLocalSh(t, a)
	bindFastSlot(t, a)
	var prompts []string
	installCheckEffects(t, a, checkOverrides{complete: func(pattern, system, user string) (string, *modelcall.Usage, error) {
		prompts = append(prompts, user)
		verdict := `[{"item":1,"verdict":"pass","evidence":"README.md:1","note":"plain"}]`
		if strings.Contains(user, "BUY NOW") {
			verdict = `[{"item":1,"verdict":"fail","evidence":"README.md:1","note":"reads like marketing"}]`
		}
		return verdict, &modelcall.Usage{Prompt: 10, Completion: 5, Model: "vendor/test-model"}, nil
	}})
	seeded, _, _ := fleetRepo(t, a, "seeded", map[string]string{"README.md": "# seeded\nBUY NOW\n"})
	plain, _, _ := fleetRepo(t, a, "plain", map[string]string{"README.md": "# plain\n"})
	for _, pid := range []ids.ID{seeded, plain} {
		adopt(t, a, pid, "prose", 1, checksJSON(treeRubric("readme-plain", "README is plain", "README.md")))
	}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "check", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "check", Steps: []schema.Step{{Name: "guidance", Uses: "guidance/conformance"}}}}}})
	recordFixtureAdoptions(t, a)
	ctx := a.asEngine(context.Background())
	a.Work.Stop()
	gate := func(pid ids.ID, slug, readme string) (workflow.CheckReceipt, map[string]json.RawMessage) {
		t.Helper()
		change, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: pid, Title: "tidy"})
		if err != nil {
			t.Fatal(err)
		}
		ws := (workflow.Layout{DataDir: a.cfg.dataDir}).ChangeWorkspace(change.ID)
		if readme != "" {
			writeFile(t, ws, "README.md", readme)
			for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "readme"}} {
				if out, err := gittest.Command(append([]string{"-C", ws}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v %s", args, err, out)
				}
			}
		}
		run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "check"})
		if !errors.Is(err, workflow.ErrDispatchStopped) {
			t.Fatal(err)
		}
		snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
		if err != nil || len(snapshot.Steps) != 1 {
			t.Fatalf("gate admission: %+v %v", snapshot, err)
		}
		if err := mirrorBranchForTest(a.projectRepoPath(slug), ws, change.Branch); err != nil {
			t.Fatal(err)
		}
		if err := a.Work.ExecuteStep(ctx, snapshot.Steps[0].ID); err != nil {
			t.Fatal(err)
		}
		saved, err := workflow.ReadStepIn(ctx, a.DB, snapshot.Steps[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		var evidence map[string]json.RawMessage
		var parsed struct{ Checks verification.Results }
		if json.Unmarshal(saved.Evidence, &evidence) != nil || json.Unmarshal(saved.Evidence, &parsed) != nil {
			t.Fatalf("evidence: %s", saved.Evidence)
		}
		for _, r := range parsed.Checks {
			if r.Check == "readme-plain" {
				return r, evidence
			}
		}
		t.Fatalf("no readme-plain result: %s", saved.Evidence)
		return workflow.CheckReceipt{}, nil
	}

	// The seed already reads like marketing; the change does not touch it.
	prompts = nil
	r, evidence := gate(seeded, "seeded", "")
	if r.Verdict != workflow.VerdictFail || !r.Preexisting || r.Undecided || evidence["preexisting"] == nil {
		t.Fatalf("failing on main too is preexisting: %+v %s", r, evidence["preexisting"])
	}
	if len(prompts) != 2 || !strings.Contains(prompts[1], "BUY NOW") {
		t.Fatalf("one more call, on main's tree: %d prompts", len(prompts))
	}

	// Main is plain; the change adds the marketing.
	prompts = nil
	r, evidence = gate(plain, "plain", "# plain\nBUY NOW\n")
	if r.Verdict != workflow.VerdictFail || r.Preexisting || r.Undecided || evidence["preexisting"] != nil {
		t.Fatalf("failing only on the change is introduced: %+v", r)
	}
	if len(prompts) != 2 || strings.Contains(prompts[1], "BUY NOW") {
		t.Fatalf("main's tree is what the second call judged: %d prompts", len(prompts))
	}
}
