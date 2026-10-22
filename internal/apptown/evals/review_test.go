package evals

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A queued, working or early-failed arm has no score yet: its checks read
// as an empty list, never null, so a reader shows "none yet".
func TestAnUnscoredArmHasNoChecksRatherThanNull(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 1)
	e := w.start(t, "alice")
	got, err := w.service.Get(as("alice"), e.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got.Arms[0].Score)
	if !strings.Contains(string(raw), `"checks":[]`) {
		t.Fatalf("an unscored arm's score: %s", raw)
	}
}

// Spend is read where each arm is admitted, not once per pass: an arm whose
// lane spends the budget, or spends an unknown amount, as it starts stops
// or holds the next arm in the same pass.
func TestEachArmIsAdmittedAgainstTheSpendAtThatMoment(t *testing.T) {
	t.Parallel()
	for name, spent := range map[string]usage.Totals{"budget spent": {CostMicros: 5_000_000}, "unknown spend": {CostMicros: 10, Incomplete: 1}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := newEvalWorld(t, 2)
			e := w.start(t, "alice")
			w.work.onStart = func() { w.spend.set(spent) }
			if err := w.service.Pass(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := w.eval(t, e.ID); len(w.work.started) != 1 || countArms(got, ArmWorking) != 1 {
				t.Fatalf("started %d lanes: %+v", len(w.work.started), got.Arms)
			}
		})
	}
}

// The step boundary asks the same rule: a copy whose eval has spent its
// budget, or is deleted, is refused its next step; a copy whose eval has
// budget left, and any project that is no copy, proceed.
func TestAStepOfACopyIsAdmittedOnlyWhileItsEvalMayProceed(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 1)
	e := w.start(t, "alice")
	if err := w.service.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	copy := w.eval(t, e.ID).Arms[0].ProjectID
	gate := w.service.Gate()
	ctx := context.Background()
	if err := gate.Admit(ctx, copy); err != nil {
		t.Fatalf("budget left: %v", err)
	}
	shop, _ := w.projects.BySlug(ctx, "shop")
	w.spend.set(usage.Totals{CostMicros: 5_000_000})
	if err := gate.Admit(ctx, copy); !errors.Is(err, ErrRefused) {
		t.Fatalf("budget spent: %v", err)
	}
	if err := gate.Admit(ctx, shop.ID); err != nil {
		t.Fatalf("a real project: %v", err)
	}
}

// A budget stop that Work refuses (a landing already merging) is not a
// stop: the arm stays live with why, the eval stays driven until its work
// ends, and nothing is reported as not run.
func TestARefusedStopIsNotAStop(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 1)
	e := w.start(t, "alice")
	if err := w.service.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.work.abandonErr = errors.New("change #3 is already on main and cannot be abandoned")
	w.spend.set(usage.Totals{CostMicros: 5_000_000})
	if err := w.service.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := w.eval(t, e.ID)
	if got.State == StateFinished || countArms(got, ArmNotRun) != 0 || countArms(got, ArmWorking) != 2 {
		t.Fatalf("a refused stop: %+v", got)
	}
	for _, a := range got.Arms {
		if !strings.Contains(a.Reason, "already on main") {
			t.Fatalf("the arm does not say why it could not stop: %+v", a)
		}
	}
}

// A deletion that comes while a copy is about to be made fences it: the
// copy is not made and no lane starts for the deleted eval.
func TestADeletionFencesTheCopyAboutToBeMade(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 1)
	e := w.start(t, "alice")
	deleted := false
	w.beforeCopy = func() {
		if !deleted {
			deleted = true
			if err := w.service.Delete(as("alice"), e.ID); err != nil {
				t.Error(err)
			}
		}
	}
	if err := w.service.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	copies, err := queries(w.db).CopiesOfEval(context.Background(), e.ID)
	if err != nil || len(copies) != 0 || len(w.work.started) != 0 {
		t.Fatalf("after deletion: copies %v, lanes %v, %v", copies, w.work.started, err)
	}
}

// A landing's deploy step that passed because there was nothing to deploy
// (merge is the release) is not scored as a deployment.
func TestAMergeOnlyReleaseIsNotScoredAsDeployed(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 1)
	ch := &workflow.Change{State: workflow.ChangeShipped}
	landing := &workflow.Run{ID: idstest.N(77)}
	w.work.snapshots[idstest.N(77)] = &workflow.RunSnapshot{Steps: []*workflow.Step{{Name: "deploy", Type: "deploy", Status: workflow.StatusPass,
		Evidence: json.RawMessage(`{"skipped":"no .apptown/app.yaml — nothing to deploy; merge is the release"}`)}}}
	if got := w.service.score(context.Background(), Arm{}, ch, nil, landing); got.Deploy == "deployed" {
		t.Fatalf("a merge-only release scored as %q", got.Deploy)
	}
	// Evidence that cannot be read claims neither deployed nor merge-only.
	w.work.snapshots[idstest.N(77)].Steps[0].Evidence = json.RawMessage(`{"skipped":7}`)
	if got := w.service.score(context.Background(), Arm{}, ch, nil, landing); !strings.Contains(got.Deploy, "not known") || strings.HasPrefix(got.Deploy, "deployed") {
		t.Fatalf("unreadable deploy evidence scored as %q", got.Deploy)
	}
}

// A copy left by an interruption between its creation and its lane start
// is the arm's from the moment it is made: the scorecard names it and
// counts its spend and unknown spend, and the next pass says the arm was
// interrupted rather than making another copy.
func TestAnInterruptedCopyStaysVisibleAndCounted(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 1)
	e := w.start(t, "alice")
	// The interruption: the copy is made, then the process stops before the lane starts.
	w.work.onStart = func() { panic("interrupted") }
	func() {
		defer func() { _ = recover() }()
		_ = w.service.Pass(context.Background())
	}()
	w.work.onStart = nil
	w.spend.set(usage.Totals{CostMicros: 123_000, Incomplete: 1})
	sc, err := w.service.Scorecard(as("alice"), e.ID)
	if err != nil {
		t.Fatal(err)
	}
	a := sc.Arms[0]
	if a.ProjectID.IsZero() || sc.CostMicros != 123_000 || sc.Incomplete != 1 || sc.ArmCosts[a.ID].Incomplete != 1 {
		t.Fatalf("the interrupted copy is hidden: arm %+v, cost %d/%d", a, sc.CostMicros, sc.Incomplete)
	}
	if err := w.service.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := w.eval(t, e.ID).Arms[0]
	copies, _ := queries(w.db).CopiesOfEval(context.Background(), e.ID)
	// The unknown spend holds the other arm; no second copy is made for this one.
	if got.State != ArmFailed || got.ProjectID != a.ProjectID || !strings.Contains(got.Reason, "interrupted") || len(copies) != 1 || w.eval(t, e.ID).State != StateHeld {
		t.Fatalf("after the restart: %+v, copies %v", got, copies)
	}
}

// Unknown spend that arrives while an arm's copy is being made holds that
// arm's lane: the copy stays, linked and waiting, the eval is held, and the
// lane starts only after the starter continues.
func TestUnknownSpendDuringCopyingHoldsTheLane(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 1)
	e := w.start(t, "alice")
	copies := 0
	w.beforeCopy = func() {
		copies++
		if copies == 2 {
			w.spend.set(usage.Totals{CostMicros: 123_000, Incomplete: 1})
		}
	}
	if err := w.service.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := w.eval(t, e.ID)
	if len(w.work.started) != 1 || got.State != StateHeld || countArms(got, ArmCopied) != 1 {
		t.Fatalf("started %d lanes; %+v", len(w.work.started), got)
	}
	if _, err := w.service.Continue(as("alice"), e.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.service.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := w.eval(t, e.ID); len(w.work.started) != 2 || countArms(got, ArmWorking) != 2 {
		t.Fatalf("after continuing: %d lanes; %+v", len(w.work.started), got)
	}
}

// The model choice is the spend gate too: every model call made for a copy
// (an agent's, a check's, a narrative's, whatever started it) is refused,
// unsent, once its eval is stopping, deleted or ended, and allowed while it
// runs or is held.
func TestAModelCallForACopyIsRefusedOnceItsEvalMayNotProceed(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 1)
	e := w.start(t, "alice")
	if err := w.service.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := w.eval(t, e.ID).Arms[0]
	ctx := context.Background()
	gate := w.service.Gate()
	if err := gate.AdmitModel(ctx, a.ProjectID, a.Models["fast"]); err != nil {
		t.Fatalf("running: %v", err)
	}
	w.spend.set(usage.Totals{CostMicros: 10, Incomplete: 1})
	if err := gate.AdmitModel(ctx, a.ProjectID, a.Models["fast"]); err != nil {
		t.Fatalf("unknown spend holds new arms, not this one's calls: %v", err)
	}
	w.spend.set(usage.Totals{CostMicros: 5_000_000})
	if err := gate.AdmitModel(ctx, a.ProjectID, a.Models["fast"]); !errors.Is(err, ErrRefused) {
		t.Fatalf("budget spent: %v", err)
	}
	w.spend.set(usage.Totals{})
	if err := w.service.Delete(as("alice"), e.ID); err != nil {
		t.Fatal(err)
	}
	if err := gate.AdmitModel(ctx, a.ProjectID, a.Models["fast"]); !errors.Is(err, ErrRefused) {
		t.Fatalf("deleted: %v", err)
	}
}

// The eval's rule reads entirely through the caller's transaction: with a
// pool of one connection held by that transaction (a merge's admission), it
// still answers, where a second connection would wait for ever.
func TestTheEvalRuleAnswersInsideTheCallersTransaction(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 1)
	e := w.start(t, "alice")
	if err := w.service.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	copy := w.eval(t, e.ID).Arms[0].ProjectID
	gate := NewGate(w.db, usage.New(w.db))
	w.db.Pool().SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := w.db.InTx(ctx, func(tx *database.Tx) error {
		return gate.AdmitIn(ctx, tx, copy)
	})
	if err != nil {
		t.Fatalf("asked inside a transaction holding the only connection: %v", err)
	}
}

// A pass job stopped by shutdown or its budget says so: it is not recorded
// as a finished pass. An arm it cut off after its lane began starting fails
// as interrupted at the next pass, as after a restart.
// Kills: a stopped pass job reported as done (Pass swallows the per-eval
// errors a cancelled context causes).
func TestAStoppedPassJobIsNotDone(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 1)
	e := w.start(t, "alice")
	ctx, cancel := context.WithCancel(context.Background())
	w.work.onStart = cancel // stopped as the first arm's run starts
	if out, err := w.service.RunPassJob(ctx, "wake", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("a stopped pass job: %v %v", out, err)
	}
	w.work.onStart = nil
	if len(w.work.started) != 1 {
		t.Fatalf("a stopped pass started %d lanes", len(w.work.started))
	}
	if _, err := w.service.RunPassJob(context.Background(), "scheduled", nil); err != nil {
		t.Fatal(err)
	}
	if got := w.eval(t, e.ID); countArms(got, ArmFailed) != 1 || !strings.Contains(failedReason(got), "interrupted while its lane was starting") {
		t.Fatalf("after the next pass: %+v", got.Arms)
	}
}

func failedReason(e *Eval) string {
	for _, a := range e.Arms {
		if a.State == ArmFailed {
			return a.Reason
		}
	}
	return ""
}
