package evals

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/evals/internal/evalssql"
	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

// MaxRunning is how many arms of one eval run at once.
const MaxRunning = 2

// The driver moves every running eval along, one pass at a time, from what
// is recorded: a pass after a restart picks up where the last left off.
// Each pass, for each eval:
//
//  1. observes its running arms (their copy's Change and runs) and scores
//     an arm whose Change landed, then takes its deployment down;
//  2. reads the eval's whole spend: a spent budget abandons the running
//     arms and leaves the rest not run; an incomplete observation the
//     starter has not continued past holds it;
//  3. starts queued arms, at most MaxRunning running at once: makes the
//     copy, gives it its models, opens its Change and starts its lane
//     with the eval's authority;
//  4. finishes the eval when every arm has ended.
//
// The accepted overspend is one agent step per running arm: spend is
// observed after the fact.

// PassJob is the job queue's kind for one pass. Passes run on their own
// queue with one worker, every minute and when an eval is started or
// continued. They never overlap, which the rule for an arm found copying or
// starting relies on (advance).
const PassJob = "eval-pass"

// RunPassJob is the job queue's handler for PassJob: one pass. A pass
// stopped by shutdown or its budget returns why, not success; the next
// pass carries on from the tables.
func (s *Service) RunPassJob(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
	err := s.Pass(ctx)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"pass": "done"}, nil
}

// requestPass asks the queue for a pass now, so a starter is not held to
// the schedule. A failed request is logged; the scheduled pass does the work.
func (s *Service) requestPass(ctx context.Context) {
	if s.deps.RequestPass == nil {
		return
	}
	if err := s.deps.RequestPass(ctx); err != nil {
		log.Printf("evals: asking for a pass: %v", err)
	}
}

// Pass moves every running or held eval along once.
func (s *Service) Pass(ctx context.Context) error {
	running, err := queries(s.db).RunningEvals(ctx)
	if err != nil {
		return err
	}
	for _, e := range running {
		if err := s.advance(ctx, e); err != nil && ctx.Err() == nil {
			log.Printf("eval %d: %v", e.ID, err)
		}
	}
	return nil
}

func (s *Service) advance(ctx context.Context, e evalssql.Eval) error {
	rows, err := queries(s.db).ArmsOfEval(ctx, e.ID)
	if err != nil {
		return err
	}
	// Every arm is read before any is observed: an arm compared now finds
	// its sibling's saved reading of the base, whichever ran first (E2B-03).
	arms := make([]Arm, len(rows))
	for i, r := range rows {
		arms[i] = armOf(r.EvalArm, r.SourceChangeNumber, r.ChangeNumber)
	}
	for i := range arms {
		switch {
		case arms[i].running():
			arms[i] = s.observe(ctx, e, arms[i], arms)
		case arms[i].State == ArmCopying || arms[i].State == ArmStarting:
			// Arms start one at a time within a pass, so one found copying
			// or starting at the start of a pass was interrupted: its copy
			// stays linked, counted and visible, left for the eval's deletion.
			what := map[bool]string{true: "its copy was being made", false: "its lane was starting"}[arms[i].State == ArmCopying]
			arms[i].State, arms[i].Reason = ArmFailed, "interrupted while "+what+"; the copy is left for the eval's deletion"
			s.saveArm(ctx, arms[i])
		}
	}
	// A spent budget stops the eval: running arms are abandoned and the
	// rest not run. Work that Work will not stop (a landing already
	// merging) stays live, says why, and is observed until it ends.
	if why, err := s.budgetSpent(ctx, e); err != nil {
		return err
	} else if why != "" {
		for i, a := range arms {
			switch {
			case a.running():
				if err := s.abandon(ctx, e, a, why); err != nil {
					arms[i].Reason = "could not be stopped: " + err.Error()
				} else {
					arms[i].State, arms[i].Reason = ArmNotRun, "stopped: "+why
				}
				s.saveArm(ctx, arms[i])
			case a.State == ArmQueued || a.State == ArmCopied:
				arms[i].State, arms[i].Reason = ArmNotRun, "not run: "+why
				s.saveArm(ctx, arms[i])
			}
		}
	} else {
		running := 0
		for _, a := range arms {
			if a.busy() {
				running++
			}
		}
		// Copied arms first: their copies are made, their lanes wait.
		for i, a := range arms {
			if a.State == ArmCopied {
				arms[i] = s.startLane(ctx, e, a)
			}
		}
		for i, a := range arms {
			if a.State != ArmQueued {
				continue
			}
			// Asked for each arm, at that moment, under the eval's lock;
			// asked too while every slot is busy, so spend nobody could
			// count holds the eval as soon as it is seen.
			if err := s.gate.admitArm(ctx, e.ID); err != nil {
				if !errors.Is(err, ErrRefused) {
					return err
				}
				break
			}
			if running >= MaxRunning {
				break
			}
			arms[i] = s.startArm(ctx, e, a)
			if arms[i].busy() {
				running++
			}
		}
	}
	for _, a := range arms {
		if !a.terminal() {
			return nil
		}
	}
	return s.finish(ctx, e, StateFinished, "")
}

// budgetSpent is why the eval's budget stops it, "" while some is left; a
// spent budget is recorded on the eval (stopping) once.
func (s *Service) budgetSpent(ctx context.Context, e evalssql.Eval) (string, error) {
	if e.State == StateStopping {
		return e.Reason, nil
	}
	spent, err := s.gate.spent(ctx, s.db, e.ID)
	if err != nil {
		return "", err
	}
	why, stop, _ := verdict(e, spent, false)
	if !stop {
		return "", nil
	}
	if _, err := queries(s.db).StopEval(ctx, evalssql.StopEvalParams{ID: e.ID, Reason: why}); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	return why, nil
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func (s *Service) finish(ctx context.Context, e evalssql.Eval, state, reason string) error {
	now := s.now()
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		if _, err := queries(tx).SetEvalState(ctx, evalssql.SetEvalStateParams{ID: e.ID, State: state, Reason: reason, FinishedAt: sql.NullTime{Time: now, Valid: true}}); err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, evalActor(e.ID), ids.Nil, "eval.finished", e.ID.String(), map[string]any{"reason": reason})
	})
}

func evalActor(evalID ids.ID) string { return fmt.Sprintf("eval/%d", evalID) }

func (s *Service) saveArm(ctx context.Context, a Arm) {
	score, _ := json.Marshal(a.Score)
	when := func(t *time.Time) sql.NullTime {
		if t == nil {
			return sql.NullTime{}
		}
		return sql.NullTime{Time: *t, Valid: true}
	}
	if a.terminal() && a.FinishedAt == nil {
		now := s.now()
		a.FinishedAt = &now
	}
	if _, err := queries(s.db).SaveArm(ctx, evalssql.SaveArmParams{ID: a.ID, State: a.State, Reason: a.Reason, ProjectID: a.ProjectID, ChangeID: a.ChangeID,
		RunID: a.RunID, LandingRunID: a.LandingRunID, StartedAt: when(a.StartedAt), ReadyAt: when(a.ReadyAt), FinishedAt: when(a.FinishedAt), ScoreJson: score}); err != nil {
		log.Printf("eval arm %d: saving: %v", a.ID, err)
	}
}

// startArm makes the arm's copy and starts its lane. The copy record and
// the arm's link to its copy are written with the copy, under the eval's
// lock (recordArmCopyIn); the lane starts only while the eval may still
// proceed. Anything that fails fails the arm with why, and the copy, if
// made, stays linked for the eval's deletion.
func (s *Service) startArm(ctx context.Context, e evalssql.Eval, a Arm) Arm {
	fail := func(why string, err error) Arm {
		a.State, a.Reason = ArmFailed, fmt.Sprintf("%s: %v", why, err)
		a.Score.NotComparable = strings.HasPrefix(why, "not comparable")
		s.saveArm(ctx, a)
		return a
	}
	source, err := workflow.ReadChangeIn(ctx, s.db, a.SourceChangeID)
	if err != nil {
		return fail("reading the original Change", err)
	}
	plan, err := s.planOf(ctx, e.ID, a.ID)
	if err != nil {
		return fail("reading the arm", err)
	}
	// The copy takes exactly what the arm was started with: a pack or a
	// source project that changed since is not the setup the eval names.
	var picks []adoption.PackPick
	if plan.Adoption != nil {
		picks = plan.Adoption.Picks
		target, _, err := s.deps.Adoption.CopyTarget(ctx, a.SourceProjectID, picks)
		if err != nil {
			return fail("not comparable: the setup can no longer be adopted", err)
		}
		if adoption.Digest(target) != plan.Adoption.Digest {
			return fail("not comparable", errors.New("the setup changed since the eval started"))
		}
	}
	who := evalActor(e.ID)
	draft := projectlifecycle.CopyDraft{
		Slug: fmt.Sprintf("eval-%d-%d", e.ID, a.ID), Name: fmt.Sprintf("Eval %d %s: change %d", e.Number, a.Arm, a.SourceChangeNumber),
		SourceID: a.SourceProjectID, Commit: source.BaseSHA, Who: who, Packs: picks,
		Record: func(tx *database.Tx, projectID ids.ID) error {
			return recordArmCopyIn(ctx, tx, projectID, Copy{EvalID: e.ID, ArmID: a.ID, Arm: a.Arm})
		},
	}
	// An adoption Change's copy starts where the Change started; the arm's
	// own Change moves it to the arm's adoption (startLane).
	want := ""
	if plan.Adoption != nil {
		want = plan.Adoption.Digest
	}
	if plan.Before != nil {
		draft.Packs, draft.Revision, want = nil, plan.Before.Revision, plan.Before.Digest
	}
	p, err := s.deps.Copier.CreateCopy(ctx, draft)
	if errors.Is(err, ErrRefused) {
		// The eval was deleted, held or stopped as its copy was made: no
		// copy exists, and the arm waits or is settled with the eval.
		return a
	}
	if p != nil {
		a.ProjectID, a.State = p.ID, ArmCopying
	}
	if err != nil {
		return fail("not comparable: the copy could not be made", err)
	}
	if want != "" {
		rev, err := s.deps.Adoption.CurrentReceipt(ctx, p.ID)
		if err != nil {
			return fail("reading the copy's adoption", err)
		}
		if adoption.Digest(rev.Receipt) != want {
			return fail("not comparable", errors.New("the copy's adoption is not the one the eval started with"))
		}
	}
	// A pack-version eval holds the model: both arms run the baseline's,
	// overrides included.
	if err := s.deps.Models.CopyModels(ctx, a.SourceProjectID, p.ID, a.Profile, a.Arm == ArmBaseline || e.SubjectKind == SubjectPack, who); err != nil {
		return fail("the copy could not be given its models", err)
	}
	a.State = ArmCopied
	s.saveArm(ctx, a)
	return s.startLane(ctx, e, a)
}

// startLane starts a copied arm's lane when the eval admits a new arm now:
// the new-arm form of the rule, read again after the copy was made, so
// spend nobody could count that arrived meanwhile holds the lane. Held,
// the arm stays copied, its copy kept, until the starter continues.
func (s *Service) startLane(ctx context.Context, e evalssql.Eval, a Arm) Arm {
	fail := func(why string, err error) Arm {
		a.State, a.Reason = ArmFailed, fmt.Sprintf("%s: %v", why, err)
		s.saveArm(ctx, a)
		return a
	}
	if err := s.gate.admitArm(ctx, e.ID); err != nil {
		if errors.Is(err, ErrRefused) {
			return a
		}
		return fail("its lane did not start", err)
	}
	plan, err := s.planOf(ctx, e.ID, a.ID)
	if err != nil {
		return fail("reading the arm", err)
	}
	source, err := workflow.ReadChangeIn(ctx, s.db, a.SourceChangeID)
	if err != nil {
		return fail("reading the original Change", err)
	}
	p := struct{ ID ids.ID }{a.ProjectID}
	a.State = ArmStarting
	s.saveArm(ctx, a)
	in := s.deps.InCopy(ctx, e.ID, p.ID)
	var ch *workflow.Change
	if plan.Before != nil {
		ch, err = s.openAdoption(in, a, plan)
	} else {
		ch, err = s.deps.Work.Create(in, workflow.ChangeDraft{ProjectID: p.ID, Type: source.Type, Title: source.Title, Body: source.Body})
	}
	if err != nil {
		a.Score.NotComparable = errors.Is(err, errNotComparable)
		return fail("the Change could not be opened", err)
	}
	a.ChangeID = ch.ID
	run, err := s.deps.Work.StartEvalRun(in, workflow.EvalRun{ChangeID: ch.ID, Workflow: plan.Lane, EvalID: e.ID, StartedBy: e.StartedBy})
	if err != nil {
		return fail("its lane could not start", err)
	}
	now := s.now()
	a.RunID, a.StartedAt, a.State = run.ID, &now, ArmWorking
	s.saveArm(ctx, a)
	return a
}

// errNotComparable: the arm's setup is not the one the eval started with.
var errNotComparable = errors.New("not comparable")

// openAdoption opens the arm's adoption Change in its copy: from the
// adoption the copy started on to the arm's own, with the checks that move
// brings as its acceptance, through Work's admission of adoption Changes.
func (s *Service) openAdoption(ctx context.Context, a Arm, plan armPlan) (*workflow.Change, error) {
	c, err := s.deps.Adoption.PrepareCopyUplift(ctx, a.ProjectID, a.SourceProjectID, plan.Adoption.Picks)
	if err != nil {
		return nil, err
	}
	if c.Digest != plan.Adoption.Digest {
		return nil, fmt.Errorf("%w: the way of working the change moves to is not the one the eval started with", errNotComparable)
	}
	checks := make([]workflow.AcceptanceCheck, 0, len(c.NewChecks))
	for _, ch := range c.NewChecks {
		checks = append(checks, workflow.AcceptanceCheck{Block: ch.Block, Check: ch.Check, Version: ch.Version, Title: ch.Title, Expects: ch.Expects})
	}
	return s.deps.Work.Uplift(ctx, workflow.Uplift{ProjectID: a.ProjectID, ExpectedRevision: c.ExpectedRevision, Acceptance: workflow.Acceptance{TargetDigest: c.Digest, Checks: checks},
		Words: c.Words(), Target: copyTarget{adoption: s.deps.Adoption, candidate: *c, copy: a.ProjectID}})
}

// copyTarget records an adoption Change's target in a copy inside Work's
// admission, with the authority the copy was given its adoption under:
// the copy's own, which a variant that drops a gating block still needs.
type copyTarget struct {
	adoption  Adoption
	candidate adoption.Candidate
	copy      ids.ID
}

func (t copyTarget) RecordTargetIn(ctx context.Context, tx *database.Tx, in workflow.TargetChange) (workflow.RecordedTarget, error) {
	r, err := t.adoption.RecordTargetIn(adoption.WithSystemAuthority(ctx, t.copy), tx, adoption.Target{ProjectID: in.ProjectID, ChangeID: in.ChangeID, Candidate: t.candidate, Actor: in.Actor})
	return workflow.RecordedTarget{ID: r.ID, Candidate: r.Candidate}, err
}

// waitingForAdoption is an adoption arm's reason between its landing and
// AppTown moving its copy to the arm's way of working.
const waitingForAdoption = "landed; waiting for AppTown to move the copy to its new way of working"

// observe reads a running arm's copy: its Change and its runs. An arm
// whose working run finished is ready to land, and lands by its own
// accepted landing; one whose Change landed is scored and its deployment
// taken down; one that ended any other way failed.
func (s *Service) observe(ctx context.Context, e evalssql.Eval, a Arm, arms []Arm) Arm {
	in := s.deps.InCopy(ctx, e.ID, a.ProjectID)
	ch, err := workflow.ReadChangeIn(ctx, s.db, a.ChangeID)
	if err != nil {
		log.Printf("eval arm %d: reading its Change: %v", a.ID, err)
		return a
	}
	runs, err := s.deps.Work.ChangeRuns(in, a.ChangeID, workflow.ChangeRuns{RootsOnly: true})
	if err != nil {
		log.Printf("eval arm %d: reading its runs: %v", a.ID, err)
		return a
	}
	var work, landing *workflow.Run
	for _, r := range runs { // newest first
		if r.ID == a.RunID {
			work = r
		} else if landing == nil && r.ID.Compare(a.RunID) > 0 { // started after the work run
			if spec, err := r.Definition(); err == nil && spec != nil && spec.Role == "landing" {
				landing = r
			}
		}
	}
	changed := false
	if work != nil && work.Status == workflow.RunDone && a.ReadyAt == nil {
		t, err := time.Parse(time.RFC3339Nano, work.UpdatedAt)
		if err != nil {
			t = s.now()
		}
		a.ReadyAt, a.State, changed = &t, ArmLanding, true
	}
	if landing != nil && a.LandingRunID != landing.ID {
		a.LandingRunID, changed = landing.ID, true
	}
	switch {
	case ch.State == workflow.ChangeShipped && ch.LandsWithoutCode() && ch.AdoptedRevision == 0:
		// An adoption Change ships first; AppTown then moves the copy to
		// its target and stamps the Change. Scored only once stamped.
		why, ended := s.adoptionEnded(ctx, a, landing)
		if !ended {
			if a.Reason != waitingForAdoption {
				a.Reason, changed = waitingForAdoption, true
			}
			break
		}
		a.Score = s.score(in, a, ch, work, landing)
		s.takeDown(ctx, e, a)
		a.State, a.Reason, changed = ArmFailed, "it landed, but AppTown could not move the copy to its new way of working: "+why, true
	case ch.State == workflow.ChangeShipped:
		a.Score = s.score(in, a, ch, work, landing)
		a.Reason = ""
		if why := s.notOnItsAdoption(ctx, e, a, ch); why != "" {
			a.Score.NotComparable, a.Reason = true, why
		} else {
			// Compared while its deployment still runs: the runtime probes
			// read it. Then it is taken down.
			a.Score.Compared, a.Score.CompareError = s.compare(ctx, e, a, arms)
		}
		s.takeDown(ctx, e, a)
		a.State, changed = ArmScored, true
	case ch.State == workflow.ChangeAbandoned:
		a.Score = s.score(in, a, ch, work, landing)
		a.State, a.Reason, changed = ArmFailed, "the Change was abandoned", true
	case work != nil && work.Status == workflow.RunAbandoned:
		a.Score = s.score(in, a, ch, work, landing)
		a.State, a.Reason, changed = ArmFailed, "its run ended before the work was ready to land", true
	case landing != nil && landing.Status == workflow.RunAbandoned && ch.State != workflow.ChangeLanding:
		a.Score = s.score(in, a, ch, work, landing)
		a.State, a.Reason, changed = ArmFailed, "its landing ended without landing", true
	default:
		if why := s.stopIfBlocked(ctx, e, a, work, landing); why != "" {
			a.Score = s.score(in, a, ch, work, landing)
			a.State, a.Reason, changed = ArmFailed, why, true
		}
	}
	if changed {
		s.saveArm(ctx, a)
	}
	return a
}

// adoptionEnded is whether AppTown stopped trying to move a shipped
// adoption arm's copy to its target, and why: its landing run's adopt job
// ended without succeeding. Pending, unknown or unreadable is not ended:
// the arm waits, and the next pass asks again.
func (s *Service) adoptionEnded(ctx context.Context, a Arm, landing *workflow.Run) (string, bool) {
	if s.deps.AdoptionJobs == nil || landing == nil {
		return "", false
	}
	ended, why, err := s.deps.AdoptionJobs.AdoptionEnded(ctx, landing.ID)
	if err != nil {
		log.Printf("eval arm %d: reading its adoption job: %v", a.ID, err)
		return "", false
	}
	return why, ended
}

// notOnItsAdoption is why a landed adoption arm is not comparable: its
// copy did not end on the arm's way of working. "" for any other arm, and
// for one that did.
func (s *Service) notOnItsAdoption(ctx context.Context, e evalssql.Eval, a Arm, ch *workflow.Change) string {
	if !ch.LandsWithoutCode() {
		return ""
	}
	plan, err := s.planOf(ctx, e.ID, a.ID)
	if err != nil || plan.Adoption == nil {
		return "not comparable: the arm's way of working could not be read"
	}
	rev, err := s.deps.Adoption.CurrentReceipt(ctx, a.ProjectID)
	if err != nil {
		return "not comparable: the copy's way of working could not be read: " + err.Error()
	}
	if adoption.Digest(rev.Receipt) != plan.Adoption.Digest {
		return "not comparable: the copy did not end on the way of working the eval started with"
	}
	return ""
}

// stopIfBlocked ends an arm whose current run (its landing while one runs,
// else its working run) Work classifies as blocked: it waits for a person
// to retry, override or send it back, and nobody does that in an eval, so
// it would hold one of the eval's running places for ever. Rework is
// committed with the failure it answers, so a blocked run is not about to
// move by itself. Its Change is abandoned, as a spent budget's is. The
// reason is why the arm failed; "" when the run is not blocked or its
// activity could not be read (the next pass reads it again).
func (s *Service) stopIfBlocked(ctx context.Context, e evalssql.Eval, a Arm, work, landing *workflow.Run) string {
	current := work
	if landing != nil && landing.Status == workflow.RunRunning {
		current = landing
	}
	if current == nil || current.Status != workflow.RunRunning {
		return ""
	}
	activity, err := s.deps.Work.Activities(s.deps.InCopy(ctx, e.ID, a.ProjectID), []ids.ID{current.ID})
	if err != nil {
		log.Printf("eval arm %d: reading its run's activity: %v", a.ID, err)
		return ""
	}
	if activity[current.ID].State != workflow.ActivityBlocked {
		return ""
	}
	why := "its run was blocked: " + activity[current.ID].Detail + ". No one can unblock a run in an eval"
	if err := s.abandon(ctx, e, a, fmt.Sprintf("eval %d: its run was blocked", e.Number)); err != nil {
		return why + ", and AppTown could not abandon its change (" + err.Error() + "). Deleting the eval abandons it."
	}
	return why + ", so AppTown abandoned its change."
}

func (s *Service) abandon(ctx context.Context, e evalssql.Eval, a Arm, why string) error {
	if a.ChangeID.IsZero() {
		return nil
	}
	_, err := s.deps.Work.Abandon(s.deps.InCopy(ctx, e.ID, a.ProjectID), workflow.StopWork{ChangeID: a.ChangeID, Reason: why})
	return err
}

// takeDown takes the copy's standing deployments down once its arm is
// scored: no final backup, the data is synthetic.
func (s *Service) takeDown(ctx context.Context, e evalssql.Eval, a Arm) {
	names, err := s.deps.Deployments.StandingNames(ctx, a.ProjectID)
	if err != nil {
		log.Printf("eval arm %d: reading its deployments: %v", a.ID, err)
		return
	}
	in := s.deps.InCopy(ctx, e.ID, a.ProjectID)
	for _, name := range names {
		if err := s.deps.Deployments.TakeDownWithProject(in, a.ProjectID, name); err != nil {
			log.Printf("eval arm %d: taking %s down: %v", a.ID, name, err)
		}
	}
}

// planOf reads what an arm was started with. A plan that cannot be read is
// an error, never an empty plan.
func (s *Service) planOf(ctx context.Context, evalID, armID ids.ID) (armPlan, error) {
	rows, err := queries(s.db).ArmsOfEval(ctx, evalID)
	if err != nil {
		return armPlan{}, err
	}
	for _, r := range rows {
		if r.EvalArm.ID == armID {
			var plan armPlan
			if err := json.Unmarshal(r.EvalArm.ModelsJson, &plan); err != nil {
				return armPlan{}, fmt.Errorf("arm %d's plan: %w", armID, err)
			}
			return plan, nil
		}
	}
	return armPlan{}, fmt.Errorf("eval %d has no arm %d", evalID, armID)
}

// compare judges the arm's landed main against its Change's base over the
// checks both arms declare, reading the base once for the Change: an arm
// whose sibling already read it reuses that reading. Its failure is kept
// as why, never as a comparison with nothing in it.
func (s *Service) compare(ctx context.Context, e evalssql.Eval, a Arm, arms []Arm) (*verification.ArmCompared, string) {
	if s.deps.Comparer == nil {
		return nil, "this AppTown does not compare arms"
	}
	plan, err := s.planOf(ctx, e.ID, a.ID)
	if err != nil {
		return nil, err.Error()
	}
	if plan.Union == nil {
		return nil, "this eval was started before arms were compared"
	}
	source, err := workflow.ReadChangeIn(ctx, s.db, a.SourceChangeID)
	if err != nil {
		return nil, err.Error()
	}
	p, err := s.deps.Projects.ByID(ctx, a.ProjectID)
	if err != nil {
		return nil, err.Error()
	}
	var base []verification.CheckAnswer
	for _, other := range arms {
		if other.ID != a.ID && other.SourceChangeID == a.SourceChangeID && other.Score.Compared != nil {
			base = other.Score.Compared.Base
		}
	}
	out, err := s.deps.Comparer.CompareArm(ctx, verification.ArmComparison{EvalID: e.ID, ProjectID: p.ID, Slug: p.Slug, Branch: p.DefaultBranch,
		BaseSHA: source.BaseSHA, Blocks: plan.Union, Judge: plan.Judge, Base: base})
	if err != nil {
		return nil, err.Error()
	}
	return out, ""
}
