package verification

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/preview"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
)

func TestConformanceFingerprintStable(t *testing.T) {
	t.Parallel()
	sc := conformance.DeclaredCheck{Block: "data/sqlite-everywhere", Version: 4,
		Check: conformance.CheckDeclaration{Name: "pure-go-driver"}}
	got := conformance.Fingerprint(sc, "")
	want := "guidance|data/sqlite-everywhere@4|pure-go-driver|"
	if got != want {
		t.Errorf("fingerprint %q, want %q", got, want)
	}
}
func TestFindingEvaluatorRecovery(t *testing.T) {
	t.Parallel()
	results := Results{
		{Block: "data/sqlite-everywhere", Version: 4, Check: "pure-go-driver", Evaluator: "sh"},
		{Block: "lang/go/errors-are-values", Version: 2, Check: "no-panic", Evaluator: "llm"},
	}
	f := conformance.Finding{Finding: check.Finding{Fingerprint: "guidance|lang/go/errors-are-values@2|no-panic|"}}
	if ev := findingEvaluator(results, f); ev != "llm" {
		t.Errorf("evaluator = %q, want llm", ev)
	}
	f = conformance.Finding{Finding: check.Finding{Fingerprint: "guidance|data/sqlite-everywhere@4|pure-go-driver|"}}
	if ev := findingEvaluator(results, f); ev != "sh" {
		t.Errorf("evaluator = %q, want sh", ev)
	}
}

// gateKind is how the gate judges one evaluator kind, specified here and not
// read from the gate: a declaration of the kind, the word its receipt
// carries, and whether the collaborator that kind runs on was asked.
type gateKind struct {
	declaration check.Declaration
	verdict     string
	asked       func() bool
}

// recordingPreviews answers that no preview runs, and keeps every label it
// was asked to open and every change it was asked to boot; a boot fails with
// bootErr.
type recordingPreviews struct {
	mu      sync.Mutex
	labels  []string
	boots   []preview.Change
	bootErr error
	openErr error
}

func (p *recordingPreviews) RehearseChange(_ context.Context, ch preview.Change, _ bool) (preview.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.boots = append(p.boots, ch)
	return preview.Result{}, p.bootErr
}

func (p *recordingPreviews) OpenPreview(_ context.Context, _ ids.ID, label string) (*preview.Served, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.labels = append(p.labels, label)
	return nil, false, p.openErr
}

func (p *recordingPreviews) asked(label string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Contains(p.labels, label)
}

// projectScope binds the run to the fixture's project, as the workflow owner
// binds a real run.
type projectScope ids.ID

func (s projectScope) ProjectID(context.Context) (ids.ID, error) { return ids.ID(s), nil }

// The gate judges every evaluator kind a declaration may name (the
// declaration owner's vocabulary, check.Evaluators), each through the
// collaborator that kind runs on: a declared check of every kind comes back
// with a receipt in its kind's words, and a check of a kind no runner judges
// is refused, never dropped as if nothing were wrong.
//
// Kills: a runner's invocation removed from the gate (its check has no
// receipt, so the gate refuses it); a kind added to the vocabulary with no
// runner (this table has no row for it, and the gate refuses it).
func TestEveryEvaluatorKindIsJudgedAtTheGate(t *testing.T) {
	t.Parallel()
	f := newScannerOwnerFixture(t)
	f.semgrep(t)
	f.Effects.report = scannerSARIF(0)
	f.Effects.answer = `[{"item":1,"verdict":"conforms","detail":"the readme says what it is"}]`
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("# shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err := database.Transact(t.Context(), f.DB, func(tx *database.Tx) (workflow.CreatedChange, error) {
		return workflow.CreateChangeIn(t.Context(), tx, workflow.ChangeDraft{ProjectID: f.ProjectID, Title: "readiness"}, "bls", time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	previews := &recordingPreviews{}
	a := NewGuidance(GuidanceDependencies{DB: f.DB, Scanners: f.Scanners, Evaluator: NewEvaluator(f.Effects, nil), Previews: previews,
		Selection: selectionWithDefault(t, f.DB, "openrouter/test-model")})
	kinds := map[string]gateKind{
		"sh": {check.Declaration{Name: "script", Expects: "a readme", Sh: "test -f README.md", Severity: "high"}, "pass",
			func() bool { return f.Effects.ran(check.ShImage) }},
		"tool": {check.Declaration{Name: "sast", Expects: "no sast findings", Tool: "semgrep-django", Severity: "high"}, "pass",
			func() bool { return f.Effects.ran("semgrep/semgrep:test") }},
		"llm": {check.Declaration{Name: "readme", Expects: "says what it is", Rubric: "the readme names the app", Scope: check.ScopeTree, Files: []string{"README.md"}}, "unclear", // "conforms" is not a checklist word
			func() bool { return f.Effects.asked("openrouter/test-model") }},
		"runtime": {check.Declaration{Name: "ready", Expects: "answers readiness", Probe: "readiness", Severity: "medium"}, "cannot-assess",
			func() bool { return previews.asked(fmt.Sprintf("change-%d", created.Change.ID)) }},
	}
	var declarations []check.Declaration
	for _, kind := range check.Evaluators() {
		row, ok := kinds[kind]
		if !ok {
			t.Errorf("evaluator kind %q may be declared, but this test does not say how the gate judges it", kind)
			row.declaration = check.Declaration{Name: "undescribed-" + kind}
		}
		row.declaration.Evaluator = kind
		declarations = append(declarations, row.declaration)
	}
	if len(kinds) != len(check.Evaluators()) {
		t.Errorf("this test describes %d kinds; declarations may name %v", len(kinds), check.Evaluators())
	}
	checks := make([]conformance.DeclaredCheck, 0, len(declarations))
	for _, d := range declarations {
		checks = append(checks, conformance.DeclaredCheck{Block: "kinds", Version: 1, Check: d})
	}
	run := &workflow.Run{ID: idstest.N(1), ProjectID: f.ProjectID, ChangeID: created.Change.ID}
	source := workflow.Source{RunID: run.ID, ProjectID: f.ProjectID}
	at := checked{Src: src, Commit: "0123456789abcdef0123456789abcdef01234567"}
	got, err := a.judgeDeclared(t.Context(), projectScope(f.ProjectID), run, source, checks, at, "high", "test")
	if err != nil {
		t.Fatalf("the gate refused a kind declarations may name: %v", err)
	}
	for _, c := range checks {
		kind := c.Check.Evaluator
		i := slices.IndexFunc(got.results, func(r workflow.CheckReceipt) bool { return r.Block == c.Block && r.Check == c.Check.Name })
		if i < 0 {
			t.Errorf("%s check %q has no receipt: %+v", kind, c.Check.Name, got.results)
			continue
		}
		if r := got.results[i]; r.Evaluator != kind || r.Verdict.String() != kinds[kind].verdict {
			t.Errorf("%s check's receipt: %+v, want verdict %q", kind, r, kinds[kind].verdict)
		}
		if row, ok := kinds[kind]; ok && !row.asked() {
			t.Errorf("the %s check was never put to the collaborator it runs on", kind)
		}
	}
	if len(got.results) != len(checks) {
		t.Errorf("%d receipts for %d checks: %+v", len(got.results), len(checks), got.results)
	}

	unsupported := []conformance.DeclaredCheck{{Block: "kinds", Version: 1, Check: check.Declaration{Name: "other", Evaluator: "nonesuch"}}}
	if _, err := a.judgeDeclared(t.Context(), projectScope(f.ProjectID), run, source, unsupported, at, "high", "test"); err == nil || !strings.Contains(err.Error(), `"nonesuch"`) {
		t.Fatalf("a check of a kind no runner judges was not refused: %v", err)
	}
}
func TestGateRuntimeCheckWithoutAPreviewIsCannotAssess(t *testing.T) {
	t.Parallel()
	a := &Guidance{}
	checks := []conformance.DeclaredCheck{{Block: "deploy/health-declared", Version: 1, Check: conformance.CheckDeclaration{Name: "readiness-redirect-safe", Evaluator: "runtime", Probe: "readiness", Severity: "medium"}}}
	// Required or not, a run with no preview booted (the landing boots none)
	// records the check unjudged and does not hold the step.
	res, findings, unjudged := a.runRuntimeChecksForRun(t.Context(), &workflow.Run{ID: idstest.N(1), ProjectID: idstest.N(1), ChangeID: ids.Nil}, checks, t.TempDir(), "", "medium", "")
	if len(res) != 1 || res[0].Verdict != workflow.VerdictCannotAssess || len(findings) != 0 || len(unjudged) != 0 {
		t.Fatalf("no change → cannot-assess, no finding, no hold: %v %v %v", res, findings, unjudged)
	}
	if unmet := unmetAcceptance(res, workflow.AcceptanceScopeOf(&workflow.Change{AcceptanceJSON: `{"checks":[{"block":"deploy/health-declared","check":"readiness-redirect-safe"}]}`})); len(unmet) != 1 {
		t.Fatalf("a remediation target that could not be judged is unmet, not silently met: %v", unmet)
	}
}

// A landing has no boot step, so its gate boots the change's preview from the
// pinned candidate itself, once however many runtime checks need it, and
// judges only what it then finds proven to run that candidate. A boot that
// fails, a preview not proven to run the candidate, or previews that could
// not be asked about, leave the checks
// cannot-assess, AppTown's fault, saying why, and those at or above failOn
// hold the step (unjudged), with or without an acceptance scope naming them.
// A candidate with no runtime contract has no app: nothing is booted or held.
// A run with no candidate boots nothing (its own boot step provides the
// preview).
//
// Kills: the boot removed (no boot asked); the boot pinned to anything but
// the candidate; the boot error dropped from the reason; the landing's hold
// removed (nothing unjudged); the no-contract exemption removed.
func TestLandingBootsItsCandidatesPreview(t *testing.T) {
	t.Parallel()
	f := newScannerOwnerFixture(t)
	created, err := database.Transact(t.Context(), f.DB, func(tx *database.Tx) (workflow.CreatedChange, error) {
		return workflow.CreateChangeIn(t.Context(), tx, workflow.ChangeDraft{ProjectID: f.ProjectID, Title: "adopt"}, "bls", time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeCheck := func(name string) conformance.DeclaredCheck {
		return conformance.DeclaredCheck{Block: "deploy/health-declared", Version: 1, Check: conformance.CheckDeclaration{Name: name, Evaluator: "runtime", Probe: "readiness", Severity: "medium"}}
	}
	checks := []conformance.DeclaredCheck{runtimeCheck("front-page-answers"), runtimeCheck("readiness-redirect-safe")}
	run := &workflow.Run{ID: idstest.N(7), ProjectID: f.ProjectID, ChangeID: created.Change.ID}
	const candidate = "9b8540411489aaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	changeLabel := fmt.Sprintf("change-%d", created.Change.ID)

	tree := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tree, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, ".apptown", "app.yaml"), []byte("apiVersion: apptown/v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	previews := &recordingPreviews{bootErr: fmt.Errorf("the app did not come up")}
	a := NewGuidance(GuidanceDependencies{DB: f.DB, Previews: previews})
	res, _, unjudged := a.runRuntimeChecksForRun(t.Context(), run, checks, tree, "", "medium", candidate)
	if len(unjudged) != len(checks) {
		t.Fatalf("required checks a failed boot left unjudged hold the landing: unjudged %v", unjudged)
	}
	want := preview.Change{ID: created.Change.ID, ProjectID: f.ProjectID, Pinned: candidate}
	if len(previews.boots) != 1 || previews.boots[0] != want {
		t.Fatalf("the landing should boot the change pinned to its candidate once, asked %+v", previews.boots)
	}
	for _, r := range res {
		if r.Verdict != workflow.VerdictCannotAssess || !r.AppTownFault || !strings.Contains(r.Detail, "the app did not come up") {
			t.Errorf("a failed boot is cannot-assess, AppTown's fault, with its error: %+v", r)
		}
	}

	previews = &recordingPreviews{}
	a = NewGuidance(GuidanceDependencies{DB: f.DB, Previews: previews})
	res, _, unjudged = a.runRuntimeChecksForRun(t.Context(), run, checks, tree, "", "medium", candidate)
	if len(unjudged) != len(checks) {
		t.Fatalf("required checks with no proven candidate preview hold the landing: unjudged %v", unjudged)
	}
	opened := 0
	for _, l := range previews.labels {
		if l == changeLabel {
			opened++
		}
	}
	if len(previews.boots) != 1 || opened != 2 {
		t.Fatalf("after booting, the landing opens the change's preview again to prove what runs: boots %+v, opened %v", previews.boots, previews.labels)
	}
	for _, r := range res {
		if r.Verdict != workflow.VerdictCannotAssess || !r.AppTownFault {
			t.Errorf("a preview not proven to run the candidate is not judged: %+v", r)
		}
	}

	previews = &recordingPreviews{openErr: fmt.Errorf("the worker did not answer")}
	a = NewGuidance(GuidanceDependencies{DB: f.DB, Previews: previews})
	if _, _, unjudged = a.runRuntimeChecksForRun(t.Context(), run, checks, tree, "", "medium", candidate); len(unjudged) != len(checks) {
		t.Fatalf("required checks whose preview could not be asked about hold the landing: unjudged %v", unjudged)
	}

	previews = &recordingPreviews{}
	a = NewGuidance(GuidanceDependencies{DB: f.DB, Previews: previews})
	if _, _, unjudged = a.runRuntimeChecksForRun(t.Context(), run, checks, tree, "", "high", candidate); len(unjudged) != 0 {
		t.Fatalf("checks below failOn do not hold the landing: %v", unjudged)
	}

	previews = &recordingPreviews{}
	a = NewGuidance(GuidanceDependencies{DB: f.DB, Previews: previews})
	if _, _, unjudged = a.runRuntimeChecksForRun(t.Context(), run, checks, t.TempDir(), "", "medium", candidate); len(unjudged) != 0 || len(previews.boots) != 0 {
		t.Fatalf("a candidate with no runtime contract has no app to boot or hold: boots %+v, unjudged %v", previews.boots, unjudged)
	}

	previews = &recordingPreviews{}
	a = NewGuidance(GuidanceDependencies{DB: f.DB, Previews: previews})
	a.runRuntimeChecksForRun(t.Context(), run, checks, tree, "", "medium", "")
	if len(previews.boots) != 0 {
		t.Fatalf("a run with no landing candidate boots nothing from its gate: %+v", previews.boots)
	}
}

func TestGateAppTownFaultIsNotRework(t *testing.T) {
	t.Parallel()
	a := &Guidance{}
	checks := []conformance.DeclaredCheck{{Block: "deploy/health-declared", Version: 1, Check: conformance.CheckDeclaration{Name: "readiness-redirect-safe", Evaluator: "runtime", Probe: "readiness", Severity: "medium"}}}
	res, _, _ := a.runRuntimeChecksForRun(t.Context(), &workflow.Run{ID: idstest.N(1), ProjectID: idstest.N(1), ChangeID: ids.Nil}, checks, t.TempDir(), "", "high", "")
	target := workflow.AcceptanceScopeOf(&workflow.Change{AcceptanceJSON: `{"checks":[{"block":"deploy/health-declared","check":"readiness-redirect-safe"}]}`})
	unmet := unmetAcceptance(res, target)
	if !onlyAppTownFaults(res, unmet) {
		t.Fatalf("no preview is AppTown's fault, got unmet=%v res=%v", unmet, res)
	}
	res[0].Verdict, res[0].AppTownFault = workflow.VerdictFail, false
	if onlyAppTownFaults(res, unmetAcceptance(res, target)) {
		t.Fatal("a real fail is the change's to fix")
	}
}

// A checklist's receipt carries the model's pass or fail; any other answer,
// a missing word included, is the model not saying: unclear, which never
// reads as a pass and is always a word the receipt's readers know.
func TestAChecklistAnswerOutsideItsWordsIsUnclear(t *testing.T) {
	t.Parallel()
	f := newScannerOwnerFixture(t)
	f.Effects.answer = `[{"item":1,"verdict":"pass"},{"item":2,"verdict":"fail"},{"item":3,"evidence":"README.md:1"},{"item":4,"verdict":"conforms"},{"item":5,"verdict":"unclear"}]`
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("# shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := NewGuidance(GuidanceDependencies{DB: f.DB, Scanners: f.Scanners, Evaluator: NewEvaluator(f.Effects, nil),
		Selection: selectionWithDefault(t, f.DB, "openrouter/test-model")})
	var checks []conformance.DeclaredCheck
	for _, name := range []string{"one", "two", "three", "four", "five"} {
		checks = append(checks, conformance.DeclaredCheck{Block: "style", Version: 1, Check: check.Declaration{Name: name, Evaluator: "llm", Rubric: name, Scope: check.ScopeTree, Files: []string{"README.md"}}})
	}
	results, _, _ := a.runLLMChecklist(t.Context(), workflow.Source{RunID: idstest.N(1), ProjectID: f.ProjectID}, checks, checked{Src: src})
	var got []string
	for _, r := range results {
		got = append(got, r.Check+"="+r.Verdict.String())
	}
	if want := []string{"one=pass", "two=fail", "three=unclear", "four=unclear", "five=unclear"}; !slices.Equal(got, want) {
		t.Fatalf("receipts %v, want %v", got, want)
	}
}

func TestLLMChecklistSkipsWithoutModel(t *testing.T) {
	t.Parallel()
	a := &Guidance{selection: model.NewSelection(model.New(testDB(t))), evaluator: NewEvaluator(nil, nil)}
	source := workflow.Source{RunID: idstest.N(1), ProjectID: ids.Nil}
	checks := []conformance.DeclaredCheck{
		{Block: "style", Version: 1, Check: conformance.CheckDeclaration{Name: "tone", Evaluator: "llm", Rubric: "kind wording"}},
	}
	results, findings, meta := a.runLLMChecklist(context.Background(), source, checks, checked{Src: t.TempDir()})
	if len(results) != 1 || results[0].Verdict != workflow.VerdictSkipped {
		t.Fatalf("llm check should be skipped without a model: %+v", results)
	}
	if len(findings) != 0 || meta != nil {
		t.Fatalf("skip path must not gate: findings=%+v meta=%+v", findings, meta)
	}
}

// At the gate the contract check is judged from the checked tree with no
// preview, and fails as a finding of its own severity; the standing-only
// check is skipped with its reason, never probed against a preview and
// never a reason to hold the landing, whatever its severity.
func TestGateJudgesTheMemoryContractAndSkipsTheStandingCheck(t *testing.T) {
	t.Parallel()
	a := &Guidance{runtime: &Runtime{}}
	tree := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tree, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, ".apptown", "app.yaml"), []byte("app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	checks := []conformance.DeclaredCheck{
		{Block: "deploy/memory-declared", Version: 1, Check: conformance.CheckDeclaration{Name: "memory-declared", Evaluator: "runtime", Probe: "memory-declared", Severity: "medium"}},
		{Block: "deploy/memory-declared", Version: 1, Check: conformance.CheckDeclaration{Name: "memory-within", Evaluator: "runtime", Probe: "memory-within", Severity: "high"}},
	}
	res, findings, unjudged := a.runRuntimeChecksForRun(t.Context(), &workflow.Run{ID: idstest.N(1), ProjectID: idstest.N(1), ChangeID: ids.Nil}, checks, tree, "", "medium", "")
	if len(res) != 2 || len(unjudged) != 0 {
		t.Fatalf("two receipts and nothing held: %v %v", res, unjudged)
	}
	if res[0].Verdict != workflow.VerdictFail || len(findings) != 1 || findings[0].Severity != "medium" || !strings.Contains(findings[0].Detail, "part web declares no memory") {
		t.Errorf("the contract check fails on the tree with no preview: %+v %+v", res[0], findings)
	}
	if res[1].Verdict != workflow.VerdictSkipped || !strings.Contains(res[1].Detail, "standing deployment") {
		t.Errorf("the standing check is skipped at the gate: %+v", res[1])
	}
	// No tree is unknown, never judged on another commit.
	res, _, _ = a.runRuntimeChecksForRun(t.Context(), &workflow.Run{ID: idstest.N(1), ProjectID: idstest.N(1), ChangeID: ids.Nil}, checks[:1], "", "", "medium", "")
	if len(res) != 1 || res[0].Verdict != workflow.VerdictCannotAssess || !strings.Contains(res[0].Detail, "checked tree is not available") {
		t.Errorf("no tree to judge: %+v", res)
	}
}
