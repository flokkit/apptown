package evals

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/complaint"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// fakeWork is Work as an eval's driver sees it: Changes it opens and runs
// it starts, runs it reports, abandonments it records.
type fakeWork struct {
	mu        sync.Mutex
	db        *database.DB
	started   []workflow.EvalRun
	abandoned []ids.ID
	runs      map[ids.ID][]*workflow.Run // by Change
	nextRun   uint64
	// onStart runs as a lane starts: what the arm spends meanwhile.
	onStart func()
	// abandonErr is Work's answer to an abandonment.
	abandonErr error
	// snapshots are the runs' snapshots by run id.
	snapshots map[ids.ID]*workflow.RunSnapshot
	// activities are Work's readings of runs by run id; a run not named is
	// working.
	activities map[ids.ID]workflow.Activity
	// created counts ordinary Changes opened; uplifts are the adoption
	// Changes opened, as asked.
	created int
	uplifts []workflow.Uplift
}

func (w *fakeWork) Activities(_ context.Context, runs []ids.ID) (map[ids.ID]workflow.Activity, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[ids.ID]workflow.Activity{}
	for _, id := range runs {
		out[id] = workflow.Activity{State: workflow.ActivityWorking}
		if a, ok := w.activities[id]; ok {
			out[id] = a
		}
	}
	return out, nil
}

func (w *fakeWork) Create(ctx context.Context, in workflow.ChangeDraft) (*workflow.Change, error) {
	w.mu.Lock()
	w.created++
	w.mu.Unlock()
	return workflow.NewCreator(w.db, noPreparation).Create(ctx, in, "eval")
}

// Uplift opens an adoption Change as Work's admission would, without its
// target record: the Change and the request it was opened with.
func (w *fakeWork) Uplift(ctx context.Context, in workflow.Uplift) (*workflow.Change, error) {
	ch, err := workflow.NewCreator(w.db, noPreparation).Create(ctx, workflow.ChangeDraft{ProjectID: in.ProjectID, Type: "uplift", Title: "Adopt the way of working: " + strings.Join(in.Words, ", "), Body: "bring it onto the target"}, "eval")
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.uplifts = append(w.uplifts, in)
	return ch, nil
}
func (w *fakeWork) StartEvalRun(_ context.Context, in workflow.EvalRun) (*workflow.Run, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.started = append(w.started, in)
	w.nextRun++
	if w.onStart != nil {
		w.onStart()
	}
	return &workflow.Run{ID: idstest.N(1000 + w.nextRun), Status: workflow.RunRunning}, nil
}
func (w *fakeWork) ChangeRuns(_ context.Context, id ids.ID, _ workflow.ChangeRuns) ([]*workflow.Run, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if runs, ok := w.runs[id]; ok {
		return runs, nil
	}
	def, _ := json.Marshal(schema.Spec{Kind: "pipeline", Role: "working"})
	return []*workflow.Run{{ID: idstest.N(1), WorkflowName: "quick-change", DefinitionJSON: string(def), Status: workflow.RunDone}}, nil
}
func (w *fakeWork) Changes(ctx context.Context, project ids.ID, _ workflow.ChangeList) ([]workflow.ChangeSummary, error) {
	return nil, nil
}
func (w *fakeWork) Snapshot(_ context.Context, id ids.ID, _ workflow.ReadView) (*workflow.RunSnapshot, error) {
	if snap, ok := w.snapshots[id]; ok {
		return snap, nil
	}
	return &workflow.RunSnapshot{}, nil
}
func (w *fakeWork) Abandon(_ context.Context, in workflow.StopWork) (*workflow.Abandoned, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.abandonErr != nil {
		return nil, w.abandonErr
	}
	w.abandoned = append(w.abandoned, in.ChangeID)
	return &workflow.Abandoned{}, nil
}

// fakeCopier makes a copy project and its record, as the lifecycle does,
// without a repository, and gives it the adoption asked for.
type fakeCopier struct {
	db *database.DB
	// before runs before the copy's creation transaction.
	before   *func()
	adoption *fakeAdoption
	made     *int
	// revisions is the source revision each copy was asked to start from
	// (0: as the source is now).
	revisions *[]int64
}

func (c fakeCopier) CreateCopy(ctx context.Context, in projectlifecycle.CopyDraft) (*project.Project, error) {
	if c.before != nil && *c.before != nil {
		(*c.before)()
	}
	var p project.Project
	err := c.db.InTx(ctx, func(tx *database.Tx) error {
		id, err := project.CreateAt(ctx, tx, time.Now(), project.Spec{Slug: in.Slug, Name: in.Name, Purpose: project.EvalCopy})
		if err != nil {
			return err
		}
		if err := in.Record(tx, id); err != nil {
			return err
		}
		p, err = project.ByIDIn(ctx, tx, id)
		return err
	})
	if err == nil && c.adoption != nil {
		*c.made++
		if c.revisions != nil {
			*c.revisions = append(*c.revisions, in.Revision)
		}
		if in.Revision != 0 {
			c.adoption.mu.Lock()
			if c.adoption.copies == nil {
				c.adoption.copies = map[ids.ID]adoption.Receipt{}
			}
			c.adoption.copies[p.ID] = beforeReceipt(in.Revision)
			c.adoption.mu.Unlock()
		} else {
			err = c.adoption.give(p.ID, in.SourceID, in.Packs)
		}
	}
	return &p, err
}

// beforeReceipt is what the source had adopted at revision: the house
// pack's block one version back.
func beforeReceipt(revision int64) adoption.Receipt {
	r := adoption.EmptyReceipt()
	r.Packs = []string{"house"}
	r.Guidance = []adoption.ReceiptBlock{{Name: "house-rules", Version: int(-revision)}}
	r.Workflows = []adoption.ReceiptWorkflow{{Name: "quick-change", Version: 1}}
	return r
}

// fakeAdoption is the adoption owner as an eval sees it: the source is on
// pack "house" v1 with its block; a pick of "candidate" at a version adds
// that version's block. Version 9 cannot be adopted; version 8 has no
// quick-change lane. moved changes what a pick of "candidate" gives, as a
// re-published pack would.
type fakeAdoption struct {
	mu     sync.Mutex
	copies map[ids.ID]adoption.Receipt
	moved  bool
}

func (a *fakeAdoption) CopyTarget(ctx context.Context, src ids.ID, picks []adoption.PackPick) (adoption.Receipt, []adoption.PackPick, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r := adoption.EmptyReceipt()
	r.Packs = []string{"house"}
	r.Guidance = []adoption.ReceiptBlock{{Name: "house-rules", Version: 1}}
	r.Workflows = []adoption.ReceiptWorkflow{{Name: "quick-change", Version: 1}}
	if picks == nil {
		return r, []adoption.PackPick{{Pack: "house", Version: 1}}, nil
	}
	for _, p := range picks {
		switch {
		case p.Pack != "candidate":
		case p.Version == 9:
			return adoption.Receipt{}, nil, errors.New("candidate v9 wants a block the house pack pins differently")
		case p.Version == 8:
			r.Workflows = []adoption.ReceiptWorkflow{{Name: "big-change", Version: 1}}
		default:
			v := int(p.Version)
			if a.moved {
				v += 100
			}
			r.Guidance = append(r.Guidance, adoption.ReceiptBlock{Name: "candidate-rules", Version: v})
		}
	}
	return r, picks, nil
}
func (a *fakeAdoption) PicksWith(ctx context.Context, projectID ids.ID, pack string, version int64, _ ...adoption.PackPick) ([]adoption.PackPick, error) {
	return []adoption.PackPick{{Pack: "house", Version: 1}, {Pack: pack, Version: version}}, nil
}
func (a *fakeAdoption) CurrentReceipt(ctx context.Context, projectID ids.ID) (*adoption.RecordedRevision, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.copies[projectID]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return &adoption.RecordedRevision{Receipt: r}, nil
}
func (a *fakeAdoption) ReceiptAt(_ context.Context, _ ids.ID, revision int64) (*adoption.RecordedRevision, error) {
	if revision <= 0 {
		return nil, adoption.ErrRevisionUnavailable
	}
	return &adoption.RecordedRevision{Revision: revision, Receipt: beforeReceipt(revision)}, nil
}

// PrepareCopyUplift is the target a copy of src would get with picks,
// prepared for dst, with the one check the move brings.
func (a *fakeAdoption) PrepareCopyUplift(ctx context.Context, dst, src ids.ID, picks []adoption.PackPick) (*adoption.Candidate, error) {
	r, picks, err := a.CopyTarget(ctx, src, picks)
	if err != nil {
		return nil, err
	}
	return &adoption.Candidate{ExpectedRevision: 1, Packs: picks, Receipt: r, Digest: adoption.Digest(r), NewChecks: []adoption.CheckSummary{{Block: "house-rules", Check: "rules-hold", Version: 1}}}, nil
}
func (a *fakeAdoption) RecordTargetIn(context.Context, *database.Tx, adoption.Target) (adoption.ScopedTarget, error) {
	return adoption.ScopedTarget{}, errors.New("the fake Work records no targets")
}

func (a *fakeAdoption) give(copyID, src ids.ID, picks []adoption.PackPick) error {
	r, _, err := a.CopyTarget(context.Background(), src, picks)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.copies == nil {
		a.copies = map[ids.ID]adoption.Receipt{}
	}
	a.copies[copyID] = r
	return nil
}

type fakeModels struct{}

func (fakeModels) ArmSlots(ctx context.Context, projectID ids.ID, profile string) (map[string]string, error) {
	if !projectID.IsZero() {
		return map[string]string{"default": "openrouter/standard/model", "fast": "openrouter/standard/fast"}, nil
	}
	return map[string]string{"default": "openrouter/" + profile + "/model", "fast": "openrouter/" + profile + "/model"}, nil
}
func (fakeModels) OfferedProfile(ctx context.Context, profile string) (bool, error) {
	return profile == "frontier", nil
}
func (fakeModels) Profiles(ctx context.Context) ([]string, error) {
	return []string{"standard", "frontier"}, nil
}
func (fakeModels) CopyModels(context.Context, ids.ID, string) error {
	return nil
}

// fakeSpend is what the eval's copies spent, as a test sets it.
type fakeSpend struct {
	mu     sync.Mutex
	totals usage.Totals
}

func (s *fakeSpend) ProjectsTotal(_ context.Context, _ database.Queryer, projects ...ids.ID) (usage.Totals, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(projects) == 0 {
		return usage.Totals{}, nil
	}
	return s.totals, nil
}

// LockInUnknownIn counts every unknown observation the test set as one
// that can never change.
func (s *fakeSpend) LockInUnknownIn(_ context.Context, _ database.Queryer, projects ...ids.ID) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(projects) == 0 {
		return 0, nil
	}
	return s.totals.Incomplete, nil
}
func (s *fakeSpend) set(t usage.Totals) { s.mu.Lock(); s.totals = t; s.mu.Unlock() }

type noComplaints struct{}

func (noComplaints) OfEval(context.Context, ids.ID) ([]complaint.Complaint, error) { return nil, nil }

type noDeployments struct{}

func (noDeployments) StandingNames(context.Context, ids.ID) ([]string, error) { return nil, nil }
func (noDeployments) TakeDownWithProject(context.Context, ids.ID, string) error {
	return nil
}

type recordingDeleter struct {
	mu      sync.Mutex
	deleted []ids.ID
}

func (d *recordingDeleter) RequestCopy(_ context.Context, projectID ids.ID, _ string) (projectlifecycle.Status, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deleted = append(d.deleted, projectID)
	return projectlifecycle.Status{}, nil
}

type evalWorld struct {
	db       *database.DB
	ids      *identity.Service
	service  *Service
	work     *fakeWork
	spend    *fakeSpend
	deleter  *recordingDeleter
	sources  []ids.ID // landed source Changes
	projects *project.Projects
	// beforeCopy runs as each copy is about to be created.
	beforeCopy func()
	adoption   *fakeAdoption
	copiesMade int
	// revisions is the source revision each copy started from.
	revisions []int64
	jobs      *fakeAdoptionJobs
	// at is AppTown's clock, in Unix seconds; zero reads the real clock.
	at atomic.Int64
	// passes counts the passes asked of the job queue.
	passes atomic.Int64
}

// evalWorld is the evals owner over a real database and real identity,
// with Work, copies, models and spend as fakes. Alice and Carol manage
// packs; root administers; source Changes landed in project "shop".
func newEvalWorld(t *testing.T, changes int) *evalWorld {
	t.Helper()
	return buildEvalWorld(t, changes, nil)
}

// buildEvalWorld is newEvalWorld, with spend read from ledger when it is
// not nil instead of the fake.
func buildEvalWorld(t *testing.T, changes int, ledger func(*database.DB) spend) *evalWorld {
	t.Helper()
	db := testDB(t)
	accounts := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := accounts.SeedAdministrator(context.Background(), "root"); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"alice", "carol"} {
		if _, err := accounts.EnsureUser(context.Background(), who); err != nil {
			t.Fatal(err)
		}
		if _, err := accounts.Grant(context.Background(), identity.Spec{SubjectType: "user", Subject: who, Role: access.RoleStandardsAdmin, Scope: access.ScopeInstance, By: "root"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	projects := project.New(db, accounts)
	w := &evalWorld{db: db, ids: accounts, work: &fakeWork{db: db, runs: map[ids.ID][]*workflow.Run{}, snapshots: map[ids.ID]*workflow.RunSnapshot{}}, spend: &fakeSpend{}, deleter: &recordingDeleter{}, projects: projects, adoption: &fakeAdoption{},
		jobs: &fakeAdoptionJobs{ended: map[ids.ID]string{}}}
	shop, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "shop", Name: "Shop"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := projects.SetDefaultBranch(context.Background(), shop, "main"); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"alice", "carol"} {
		if _, err := accounts.Grant(context.Background(), identity.Spec{SubjectType: "user", Subject: who, Role: access.RoleProjectViewer, Scope: access.ScopeProject, ScopeID: shop.String(), By: "root"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	for i := range changes {
		ch, err := workflow.NewCreator(db, noPreparation).Create(context.Background(), workflow.ChangeDraft{ProjectID: shop, Type: "feature", Title: fmt.Sprintf("change %d", i), Body: "make it so"}, "bls")
		if err != nil {
			t.Fatal(err)
		}
		// sql-guarantee: a landed Change needs a whole landing; the eval reads only its state and base commit.
		if _, err := db.SQL().ExecContext(context.Background(), `UPDATE changes SET state='shipped', base_sha='abc123' WHERE id=$1`, ch.ID); err != nil {
			t.Fatal(err)
		}
		w.sources = append(w.sources, ch.ID)
	}
	w.service = New(db, Dependencies{
		Authority: access.Checked{Authority: identity.Scoped(accounts, "evals"), Projects: projects},
		Projects:  projects, Work: w.work, Copier: fakeCopier{db: db, before: &w.beforeCopy, adoption: w.adoption, made: &w.copiesMade, revisions: &w.revisions}, Models: fakeModels{}, Spend: spendOf(w, ledger), Adoption: w.adoption, AdoptionJobs: w.jobs,
		Complaints: noComplaints{}, Deployments: noDeployments{}, Deleter: w.deleter,
		InCopy:      func(ctx context.Context, evalID, projectID ids.ID) context.Context { return ctx },
		RequestPass: func(context.Context) error { w.passes.Add(1); return nil },
		Now: func() time.Time {
			if at := w.at.Load(); at != 0 {
				return time.Unix(at, 0)
			}
			return time.Now()
		},
	})
	return w
}

// fakeAdoptionJobs is each landing run's adopt job that ended without
// succeeding, with its error; any other is pending.
type fakeAdoptionJobs struct {
	mu    sync.Mutex
	ended map[ids.ID]string
}

func (j *fakeAdoptionJobs) AdoptionEnded(_ context.Context, run ids.ID) (bool, string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	why, ok := j.ended[run]
	return ok, why, nil
}

func spendOf(w *evalWorld, ledger func(*database.DB) spend) spend {
	if ledger != nil {
		return ledger(w.db)
	}
	return w.spend
}

func as(name string) context.Context {
	return access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: name, Via: "session"}, Verified: true, Receipt: name})
}

func (w *evalWorld) start(t *testing.T, who string) *Eval {
	t.Helper()
	e, err := w.service.Start(as(who), Start{Changes: w.sources, Profile: "frontier", BudgetMicros: 5_000_000, Shown: meaning})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (w *evalWorld) eval(t *testing.T, id ids.ID) *Eval {
	t.Helper()
	e, err := evalIn(context.Background(), w.db, id)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func countArms(e *Eval, state string) int {
	n := 0
	for _, a := range e.Arms {
		if a.State == state {
			n++
		}
	}
	return n
}

// Starting is a standards manager's act, after being shown what it means:
// anyone else, and anyone not shown the meaning, is refused. A start
// records two arms per Change, each with the models it runs and the lane
// that re-runs it, and a receipt carrying the meaning.
func TestStartingAnEvalIsAStandardsManagersShownAct(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 2)
	if _, err := w.ids.EnsureUser(context.Background(), "bob"); err != nil {
		t.Fatal(err)
	}
	var denied *access.Denied
	if _, err := w.service.Start(as("bob"), Start{Changes: w.sources, Profile: "frontier", BudgetMicros: 1, Shown: meaning}); !errors.As(err, &denied) {
		t.Fatalf("someone who does not manage packs: %v", err)
	}
	if _, err := w.service.Start(as("alice"), Start{Changes: w.sources, Profile: "frontier", BudgetMicros: 1, Shown: "ok"}); !errors.Is(err, errMeaning) {
		t.Fatalf("not shown the meaning: %v", err)
	}
	if _, err := w.service.Start(as("alice"), Start{Changes: w.sources, Profile: "unknown", BudgetMicros: 1, Shown: meaning}); !errors.Is(err, errSubject) {
		t.Fatalf("a profile AppTown does not offer: %v", err)
	}
	if n := w.passes.Load(); n != 0 {
		t.Fatalf("refused starts asked for %d passes", n)
	}
	e := w.start(t, "alice")
	if len(e.Arms) != 4 || countArms(e, armQueued) != 4 || e.StartedBy != "alice" || e.Subject != "frontier" {
		t.Fatalf("started: %+v", e)
	}
	// Its arms start at the pass it asks for, not at the next scheduled one.
	if n := w.passes.Load(); n != 1 {
		t.Fatalf("a start asked for %d passes", n)
	}
	for _, a := range e.Arms {
		want := "openrouter/frontier/model"
		if a.Arm == armBaseline {
			want = "openrouter/standard/model"
		}
		if a.Models["default"] != want {
			t.Fatalf("arm %s runs %v", a.Arm, a.Models)
		}
		// An arm names its source change by the number people read; it has
		// no change of its own until its copy is worked.
		source, err := workflow.ReadChangeIn(context.Background(), w.db, a.SourceChangeID)
		if err != nil || source.Number <= 0 || a.SourceChangeNumber != source.Number || a.ChangeNumber != 0 {
			t.Fatalf("arm %s names source change %d and change %d, want %+v: %v", a.Arm, a.SourceChangeNumber, a.ChangeNumber, source, err)
		}
	}
	receipts, err := audit.Recent(context.Background(), w.db, audit.Filter{KindPrefix: "eval.started", Limit: 10})
	if err != nil || len(receipts) != 1 || receipts[0].Actor != "alice" || !jsonHas(receipts[0].Payload, meaning) {
		t.Fatalf("start receipt: %+v %v", receipts, err)
	}
}

func jsonHas(payload any, s string) bool {
	b, _ := json.Marshal(payload)
	return json.Valid(b) && contains(string(b), s)
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// At most two arms of an eval run at once; each starts in its own copy,
// recorded as a copy of that eval and arm, with the eval's authority.
func TestAtMostTwoArmsRunAtOnce(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 2)
	e := w.start(t, "alice")
	for range 2 {
		if err := w.service.pass(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	got := w.eval(t, e.ID)
	if countArms(got, armWorking) != maxRunning || countArms(got, armQueued) != 2 || len(w.work.started) != maxRunning {
		t.Fatalf("arms: %+v, started %v", got.Arms, w.work.started)
	}
	for _, a := range got.Arms {
		if a.State != armWorking {
			continue
		}
		copy, ok, err := copyOfIn(context.Background(), w.service.db, a.ProjectID)
		if err != nil || !ok || copy.EvalID != e.ID || copy.ArmID != a.ID || copy.Arm != a.Arm {
			t.Fatalf("copy record of arm %d: %+v %v %v", a.ID, copy, ok, err)
		}
	}
	for _, r := range w.work.started {
		if r.EvalID != e.ID || r.StartedBy != "alice" || r.Workflow != "quick-change" {
			t.Fatalf("started %+v", r)
		}
	}
}

// An arm whose run is blocked waits for a person, and nobody unblocks a run
// in an eval: the arm fails saying why, its Change is abandoned, and its
// running place goes to the next queued arm on the same pass. An arm whose
// run is working is left alone.
func TestABlockedArmFailsAndFreesItsPlace(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 2)
	e := w.start(t, "alice")
	if err := w.service.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	var blocked, working Arm
	for _, a := range w.eval(t, e.ID).Arms {
		if a.State != armWorking {
			continue
		}
		w.work.runs[a.ChangeID] = []*workflow.Run{{ID: a.RunID, Status: workflow.RunRunning}}
		if blocked.ID.IsZero() {
			blocked = a
		} else {
			working = a
		}
	}
	w.work.activities = map[ids.ID]workflow.Activity{blocked.RunID: {State: workflow.ActivityBlocked, Detail: "review (fail)"}}
	if err := w.service.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := w.eval(t, e.ID)
	for _, a := range got.Arms {
		switch a.ID {
		case blocked.ID:
			want := "its run was blocked: review (fail). No one can unblock a run in an eval, so AppTown abandoned its change."
			if a.State != armFailed || a.Reason != want || a.FinishedAt == nil {
				t.Fatalf("the blocked arm: %s %q, want failed %q", a.State, a.Reason, want)
			}
		case working.ID:
			if a.State != armWorking {
				t.Fatalf("the working arm became %s: %s", a.State, a.Reason)
			}
		}
	}
	if len(w.work.abandoned) != 1 || w.work.abandoned[0] != blocked.ChangeID {
		t.Fatalf("abandoned %v, want only the blocked arm's change %v", w.work.abandoned, blocked.ChangeID)
	}
	if countArms(got, armWorking) != maxRunning || len(w.work.started) != maxRunning+1 {
		t.Fatalf("the freed place was not taken: %+v, started %d", got.Arms, len(w.work.started))
	}
}

// Spend that is not wholly known holds the eval: no next arm starts until
// the person who started it continues, and only they may. A later unknown
// observation holds it again.
func TestUnknownSpendHoldsTheNextArmUntilTheStarterContinues(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 3)
	e := w.start(t, "alice")
	if err := w.service.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The first two arms end, leaving room for the next two.
	for _, a := range w.eval(t, e.ID).Arms {
		if a.State == armWorking {
			w.work.runs[a.ChangeID] = []*workflow.Run{{ID: a.RunID, Status: workflow.RunAbandoned}}
		}
	}
	w.spend.set(usage.Totals{CostMicros: 100, Incomplete: 1})
	if err := w.service.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := w.eval(t, e.ID)
	if got.State != stateHeld || countArms(got, armFailed) != 2 || countArms(got, armQueued) != 4 {
		t.Fatalf("held, the next arms not started: %+v", got)
	}
	asked := w.passes.Load()
	var denied *access.Denied
	if _, err := w.service.Continue(as("carol"), e.ID); !errors.As(err, &denied) || w.passes.Load() != asked {
		t.Fatalf("another standards manager continued: %v (passes asked %d)", err, w.passes.Load()-asked)
	}
	if _, err := w.service.Continue(as("alice"), e.ID); err != nil {
		t.Fatal(err)
	}
	// The continued eval's next arm starts at the pass it asks for.
	if n := w.passes.Load() - asked; n != 1 {
		t.Fatalf("a continue asked for %d passes", n)
	}
	if err := w.service.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := w.eval(t, e.ID); got.State != stateRunning || countArms(got, armWorking) != maxRunning {
		t.Fatalf("continued: %+v", got)
	}
	w.spend.set(usage.Totals{CostMicros: 200, Incomplete: 2})
	if err := w.service.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := w.eval(t, e.ID); got.State != stateHeld {
		t.Fatalf("a new unknown observation holds it again: %+v", got)
	}
}

// A spent budget abandons the running arms, leaves the rest not run, and
// finishes the eval with why.
func TestASpentBudgetStopsTheEval(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 2)
	e := w.start(t, "alice")
	if err := w.service.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.spend.set(usage.Totals{CostMicros: 5_000_000})
	if err := w.service.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := w.eval(t, e.ID)
	if got.State != stateFinished || countArms(got, armNotRun) != 4 || len(w.work.abandoned) != maxRunning {
		t.Fatalf("budget spent: %+v, abandoned %v", got, w.work.abandoned)
	}
}

// The model choice for a copy admits only the models its arm runs; any
// project that is not a copy is admitted as before.
func TestACopysModelChoiceIsItsArmsModels(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 1)
	e := w.start(t, "alice")
	if err := w.service.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, a := range w.eval(t, e.ID).Arms {
		other := "openrouter/standard/model"
		if a.Arm == armBaseline {
			other = "openrouter/frontier/model"
		}
		if err := w.service.gate.AdmitModel(ctx, a.ProjectID, a.Models["fast"]); err != nil {
			t.Fatalf("arm %s, its own model: %v", a.Arm, err)
		}
		if err := w.service.gate.AdmitModel(ctx, a.ProjectID, other); !errors.Is(err, errModelOutsideArm) {
			t.Fatalf("arm %s, the other arm's model: %v", a.Arm, err)
		}
	}
	shop, _ := w.projects.BySlug(ctx, "shop")
	if err := w.service.gate.AdmitModel(ctx, shop.ID, "anything/at-all"); err != nil {
		t.Fatalf("a real project: %v", err)
	}
}

// An eval is deleted by an administrator, or by the person who started it
// while they still manage packs, as their grants say now: not by another
// standards manager, not by the starter once their grant is gone, not by
// an agent's token. The receipt names who deleted it, and its copies go.
func TestWhoMayDeleteAnEval(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		who     access.Principal
		revoke  bool
		allowed bool
	}{
		{"the starter", access.Principal{Kind: "user", Name: "alice", Via: "session"}, false, true},
		{"the starter whose grant was removed", access.Principal{Kind: "user", Name: "alice", Via: "session"}, true, false},
		{"another standards manager", access.Principal{Kind: "user", Name: "carol", Via: "session"}, false, false},
		{"an administrator", access.Principal{Kind: "user", Name: "root", Via: "session"}, false, true},
		{"the starter's agent token", access.Principal{Kind: "user", Name: "alice", Via: "token", Purpose: access.TokenAgent}, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := newEvalWorld(t, 1)
			e := w.start(t, "alice")
			if err := w.service.pass(context.Background()); err != nil {
				t.Fatal(err)
			}
			if c.revoke {
				grants, err := w.ids.GrantsAt(context.Background(), access.ScopeInstance, "")
				if err != nil {
					t.Fatal(err)
				}
				for _, g := range grants {
					if g.Subject == "alice" {
						if _, err := w.ids.Revoke(context.Background(), g.ID, "root"); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			ctx := access.WithCaller(context.Background(), access.Caller{Principal: c.who, Verified: true, Receipt: c.who.Name})
			err := w.service.Delete(ctx, e.ID)
			if c.allowed != (err == nil) {
				t.Fatalf("delete: %v", err)
			}
			receipts, rerr := audit.Recent(context.Background(), w.db, audit.Filter{KindPrefix: "eval.deleted", Limit: 10})
			if rerr != nil {
				t.Fatal(rerr)
			}
			if !c.allowed {
				if len(receipts) != 0 || len(w.deleter.deleted) != 0 {
					t.Fatalf("a refused deletion deleted: %+v %v", receipts, w.deleter.deleted)
				}
				return
			}
			if len(receipts) != 1 || receipts[0].Actor != c.who.Name || len(w.deleter.deleted) != maxRunning {
				t.Fatalf("deleted: %+v, copies %v", receipts, w.deleter.deleted)
			}
			if _, err := w.service.get(as("alice"), e.ID); !errors.Is(err, errMissing) {
				t.Fatalf("a deleted eval is still read: %v", err)
			}
		})
	}
}

// noPreparation: these Changes have no workspace to prepare.
func noPreparation(context.Context, workflow.CreatedChange) error { return nil }

// A pack-version eval varies exactly one thing: both arms run the
// baseline's models, the baseline adopts what the source runs now and the
// variant the candidate the version would be for it, each fixed at start
// by digest, with the union of both arms' blocks and the baseline's fast
// model as the judge. The variant's copy adopts that candidate exactly. A
// version the project could not adopt, or one without the lane the Change
// was worked in, refuses that Change at start.
//
// Kills: a variant copy adopting the source's current pack; arms running
// different models in a pack eval; a check only the version declares left
// out of the union; a broken version left to fail as an arm.
func TestAPackVersionEvalsVariantAdoptsTheCandidateExactly(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 1)
	publishCandidate(t, w, 9)
	start := func(version int64) (*Eval, error) {
		return w.service.Start(as("alice"), Start{Changes: w.sources, Pack: "candidate", PackVersion: version, BudgetMicros: 5_000_000, Shown: meaning})
	}
	if _, err := start(9); err == nil || !contains(err.Error(), "cannot adopt") {
		t.Fatalf("a version the project cannot adopt: %v", err)
	}
	if _, err := start(8); err == nil || !contains(err.Error(), "quick-change lane") {
		t.Fatalf("a version without the Change's lane: %v", err)
	}
	if _, err := w.service.Start(as("alice"), Start{Changes: w.sources, Pack: "candidate", PackVersion: 2, Profile: "frontier", BudgetMicros: 1, Shown: meaning}); !errors.Is(err, errSubject) {
		t.Fatalf("two things varying at once: %v", err)
	}
	if _, err := start(10); !errors.Is(err, errSubject) {
		t.Fatalf("a version never published: %v", err)
	}
	e, err := start(2)
	if err != nil {
		t.Fatal(err)
	}
	if e.SubjectKind != SubjectPack || e.Subject != "candidate@2" || e.SubjectDigest == "" {
		t.Fatalf("the subject names the version tested: %+v", e)
	}
	plans := map[string]armPlan{}
	for _, a := range e.Arms {
		plan, err := w.service.planOf(context.Background(), e.ID, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		plans[a.Arm] = plan
		if a.Models["default"] != "openrouter/standard/model" || plan.Judge != "openrouter/standard/fast" {
			t.Fatalf("arm %s runs %v judged by %q: a pack eval holds the model", a.Arm, a.Models, plan.Judge)
		}
		if len(plan.Union) != 2 {
			t.Fatalf("arm %s: the union of both arms' blocks: %+v", a.Arm, plan.Union)
		}
	}
	if plans[armBaseline].Adoption.Picks != nil || len(plans[armVariant].Adoption.Picks) != 2 || plans[armBaseline].Adoption.Digest == plans[armVariant].Adoption.Digest {
		t.Fatalf("the arms' adoptions: %+v %+v", plans[armBaseline].Adoption, plans[armVariant].Adoption)
	}
	if err := w.service.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, a := range w.eval(t, e.ID).Arms {
		rev, err := w.adoption.CurrentReceipt(t.Context(), a.ProjectID)
		if err != nil {
			t.Fatalf("arm %s has no copy: %+v %v", a.Arm, a, err)
		}
		if adoption.Digest(rev.Receipt) != plans[a.Arm].Adoption.Digest {
			t.Fatalf("arm %s's copy adopted %+v, not the setup it started with", a.Arm, rev.Receipt)
		}
	}
}

// publishCandidate publishes pack "candidate" up to version n, so the
// version a pack eval names exists.
func publishCandidate(t *testing.T, w *evalWorld, n int) {
	t.Helper()
	ways := wow.New(w.db, wow.Dependencies{})
	for i := range n {
		if v, err := ways.PublishPack(t.Context(), "candidate", "", fmt.Sprintf(`{"extensions":["e%d"]}`, i+1), ""); err != nil || v != i+1 {
			t.Fatalf("publishing candidate v%d: %d %v", i+1, v, err)
		}
	}
}

// An arm's copy takes exactly the setup its plan names: when the version
// gives something else by the time the arm starts (re-published data, a
// changed source), the arm is not comparable, says so, makes no copy and
// is left out of better and worse.
//
// Kills: a copy adopting whatever the pack gives now; a drifted arm
// counted as a result.
func TestAnArmWhoseSetupChangedSinceStartIsNotComparable(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 1)
	publishCandidate(t, w, 2)
	e, err := w.service.Start(as("alice"), Start{Changes: w.sources, Pack: "candidate", PackVersion: 2, BudgetMicros: 5_000_000, Shown: meaning})
	if err != nil {
		t.Fatal(err)
	}
	w.adoption.mu.Lock()
	w.adoption.moved = true
	w.adoption.mu.Unlock()
	if err := w.service.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, a := range w.eval(t, e.ID).Arms {
		switch a.Arm {
		case armVariant:
			if a.State != armFailed || !a.Score.NotComparable || a.Reason != "not comparable: the setup changed since the eval started" || !a.ProjectID.IsZero() {
				t.Fatalf("the drifted variant: %+v", a)
			}
		case armBaseline:
			if a.ProjectID.IsZero() || a.Score.NotComparable {
				t.Fatalf("the baseline did not drift: %+v", a)
			}
		}
	}
	if w.copiesMade != 1 {
		t.Fatalf("copies made: %d, want the baseline's alone", w.copiesMade)
	}
}

// The judge's door admits only the judge recorded for a copy's Change, for
// that copy, while its eval may proceed: with arms on disjoint models the
// scorer may judge the variant with the baseline's model, while the
// variant's own door (its agents, its checks) still refuses that model.
// A project that is not a copy, another model, and a stopped eval are
// refused.
//
// Kills: the judge refused for the variant, defeating a common
// judge; the judge added to the variant's own models; the judge's door
// open to real projects or after the eval stops.
func TestTheJudgesDoorAdmitsOnlyTheRecordedJudge(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 1)
	e := w.start(t, "alice")
	if err := w.service.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const judge = "openrouter/standard/fast"
	for _, a := range w.eval(t, e.ID).Arms {
		if err := w.service.gate.AdmitJudge(ctx, a.ProjectID, judge); err != nil {
			t.Fatalf("arm %s, the recorded judge: %v", a.Arm, err)
		}
		if err := w.service.gate.AdmitJudge(ctx, a.ProjectID, "openrouter/frontier/model"); !errors.Is(err, errNotTheJudge) {
			t.Fatalf("arm %s, another model through the judge's door: %v", a.Arm, err)
		}
		if a.Arm == armVariant {
			if err := w.service.gate.AdmitModel(ctx, a.ProjectID, judge); !errors.Is(err, errModelOutsideArm) {
				t.Fatalf("the variant's own door must still refuse the baseline-only judge: %v", err)
			}
		}
	}
	shop, _ := w.projects.BySlug(ctx, "shop")
	if err := w.service.gate.AdmitJudge(ctx, shop.ID, judge); !errors.Is(err, errNotTheJudge) {
		t.Fatalf("a real project through the judge's door: %v", err)
	}
	w.spend.set(usage.Totals{CostMicros: 5_000_000})
	if err := w.service.pass(ctx); err != nil {
		t.Fatal(err)
	}
	for _, a := range w.eval(t, e.ID).Arms {
		if !a.ProjectID.IsZero() {
			if err := w.service.gate.AdmitJudge(ctx, a.ProjectID, judge); !errors.Is(err, ErrRefused) {
				t.Fatalf("judging after the eval stopped: %v", err)
			}
		}
	}
}

// The scorecard's tallies compare the variant with the baseline only on
// Changes where both arms ended and both are comparable; the others are
// counted apart. A measure either arm has no reading of (no comparison, a
// check whose findings could not be compared, spend partly unknown) is
// unknown, never "same" or "better".
//
// Kills: a not-comparable arm counted as worse or same; a missing reading
// counted as same; fewer new findings read as worse.
func TestTheTallyLeavesOutWhatIsNotComparable(t *testing.T) {
	t.Parallel()
	compared := func(newN, fixedN int) *verification.ArmCompared {
		row := verification.CheckRow{Block: "b", Check: "c"}
		for i := range newN {
			row.New = append(row.New, fmt.Sprint("new", i))
		}
		for i := range fixedN {
			row.Fixed = append(row.Fixed, fmt.Sprint("fixed", i))
		}
		return &verification.ArmCompared{Rows: []verification.CheckRow{row}}
	}
	arms := []Arm{
		{ID: idstest.N(1), SourceChangeID: idstest.N(10), Arm: armBaseline, State: armScored, Score: Score{Landed: true, Compared: compared(2, 0), RequestMet: "partly"}},
		{ID: idstest.N(2), SourceChangeID: idstest.N(10), Arm: armVariant, State: armScored, Score: Score{Landed: true, Compared: compared(0, 1), RequestMet: "yes"}},
		{ID: idstest.N(3), SourceChangeID: idstest.N(11), Arm: armBaseline, State: armScored, Score: Score{Landed: true}},
		{ID: idstest.N(4), SourceChangeID: idstest.N(11), Arm: armVariant, State: armFailed, Score: Score{NotComparable: true}},
		{ID: idstest.N(5), SourceChangeID: idstest.N(12), Arm: armBaseline, State: armWorking},
		{ID: idstest.N(6), SourceChangeID: idstest.N(12), Arm: armVariant, State: armScored},
	}
	tallies, comparable, notComparable := tally(arms, map[ids.ID]ArmCost{idstest.N(1): {CostMicros: 5}, idstest.N(2): {CostMicros: 3, Incomplete: 1}})
	if comparable != 1 || notComparable != 1 {
		t.Fatalf("comparable %d, not comparable %d", comparable, notComparable)
	}
	want := map[string]Tally{
		"landed":         {Same: 1},
		"new findings":   {Better: 1},
		"findings fixed": {Better: 1},
		"request met":    {Better: 1},
		"cost":           {Unknown: 1},
	}
	for _, got := range tallies {
		w, ok := want[got.Measure]
		if !ok {
			continue
		}
		w.Measure = got.Measure
		if got != w {
			t.Errorf("%s: %+v, want %+v", got.Measure, got, w)
		}
	}

	// A check that timed out on the variant, and a boolean check failing
	// on both trees, leave that Change's finding measures unknown: never
	// better because the variant's count came out empty, never same.
	unknownRow := func(why string) *verification.ArmCompared {
		return &verification.ArmCompared{Rows: []verification.CheckRow{{Block: "b", Check: "c", NotComparable: why}}}
	}
	for name, pair := range map[string][2]*verification.ArmCompared{
		"an errored variant":          {compared(1, 0), unknownRow("on this arm: the check could not answer: timed out")},
		"a boolean failing on both":   {unknownRow("failing on both"), unknownRow("failing on both")},
		"an unassessed baseline only": {unknownRow("on the base: no image"), compared(0, 0)},
	} {
		arms := []Arm{
			{ID: idstest.N(1), SourceChangeID: idstest.N(10), Arm: armBaseline, State: armScored, Score: Score{Landed: true, Compared: pair[0]}},
			{ID: idstest.N(2), SourceChangeID: idstest.N(10), Arm: armVariant, State: armScored, Score: Score{Landed: true, Compared: pair[1]}},
		}
		tallies, _, _ := tally(arms, map[ids.ID]ArmCost{})
		for _, got := range tallies {
			if (got.Measure == "new findings" || got.Measure == "findings fixed") && (got.Unknown != 1 || got.Better+got.Same+got.Worse != 0) {
				t.Errorf("%s, %s: %+v, want unknown", name, got.Measure, got)
			}
		}
	}
}

// An arm's diff is read under the scorecard's authority and the source
// project's read (the diff is its code), and the arm is found within the
// eval named: an arm of another eval is not there, and someone who may
// not read evals learns nothing.
//
// Kills: a diff served by arm id alone, across evals; a diff served to a
// caller the scorecard refuses.
func TestAnArmsDiffIsFoundWithinItsEval(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 1)
	first, second := w.start(t, "alice"), w.start(t, "alice")
	if _, err := w.service.ArmDiff(as("alice"), first.ID, second.Arms[0].ID); fault.KindOf(err) != fault.NotFound {
		t.Fatalf("an arm of another eval: %v", err)
	}
	if _, err := w.ids.EnsureUser(context.Background(), "bob"); err != nil {
		t.Fatal(err)
	}
	var denied *access.Denied
	if _, err := w.service.ArmDiff(as("bob"), first.ID, first.Arms[0].ID); !errors.As(err, &denied) {
		t.Fatalf("someone who does not manage packs: %v", err)
	}
	// Dave reads evals but not the source project, whose code a diff is.
	if _, err := w.ids.EnsureUser(context.Background(), "dave"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ids.Grant(context.Background(), identity.Spec{SubjectType: "user", Subject: "dave", Role: access.RoleStandardsAdmin, Scope: access.ScopeInstance, By: "root"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.service.ArmDiff(as("dave"), first.ID, first.Arms[0].ID); !errors.As(err, &denied) {
		t.Fatalf("someone who may not read the source project: %v", err)
	}
	if _, err := w.service.Scorecard(as("dave"), first.ID); err != nil {
		t.Fatalf("the scorecard itself, for someone who reads evals: %v", err)
	}
	if _, err := w.service.ArmDiff(as("alice"), first.ID, first.Arms[0].ID); err == nil || !contains(err.Error(), "never started") {
		t.Fatalf("an arm with no copy yet: %v", err)
	}
	if got, err := w.service.LatestOf(as("bob"), subjectModel, "frontier"); !errors.As(err, &denied) || got != nil {
		t.Fatalf("the newest eval, for someone who may not read evals: %+v %v", got, err)
	}
	if got, err := w.service.LatestOf(as("alice"), subjectModel, "frontier"); err != nil || got == nil || got.ID != second.ID {
		t.Fatalf("the newest eval of a subject: %+v %v", got, err)
	}
}

// Evals are read by people and their own command lines and agent tokens,
// never by a project-bound instrument: a step's or a chat's credential in
// one project reads no eval, though standards:read-evals is a read every
// instrument would otherwise be allowed (E2B-01).
//
// Kills: an instance-wide eval read admitted to a step token of another
// project.
func TestAProjectsStepsAndChatsReadNoEval(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 1)
	e := w.start(t, "alice")
	shop, _ := w.projects.BySlug(context.Background(), "shop")
	for _, instrument := range []string{"run", "console"} {
		ctx := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "instrument", Name: instrument + "/1", Person: "alice", Instrument: instrument, Via: instrument, ProjectID: shop.ID}, Verified: true, Receipt: instrument})
		var denied *access.Denied
		if _, err := w.service.List(ctx); !errors.As(err, &denied) {
			t.Fatalf("a %s credential listed evals: %v", instrument, err)
		}
		if _, err := w.service.Scorecard(ctx, e.ID); !errors.As(err, &denied) {
			t.Fatalf("a %s credential read a scorecard: %v", instrument, err)
		}
		if _, err := w.service.LatestOf(ctx, subjectModel, "frontier"); !errors.As(err, &denied) {
			t.Fatalf("a %s credential read the newest eval: %v", instrument, err)
		}
	}
	agent := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "alice", Via: "token", Purpose: access.TokenAgent}, Verified: true, Receipt: "alice"})
	if _, err := w.service.Scorecard(agent, e.ID); err != nil {
		t.Fatalf("alice's own agent token reads her eval: %v", err)
	}
}

// fakeComparer is Verification's comparison as the driver sees it: it
// counts how often the base had to be read and hands back what it read.
type fakeComparer struct {
	mu        sync.Mutex
	baseReads int
	given     map[ids.ID]bool // copy project → was given the base
}

func (c *fakeComparer) CompareArm(_ context.Context, in verification.ArmComparison) (*verification.ArmCompared, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.given[in.ProjectID] = in.Base != nil
	base := in.Base
	if base == nil {
		c.baseReads++
		base = []verification.CheckAnswer{{Block: "b", Version: 1, Check: "c", State: "conforms"}}
	}
	return &verification.ArmCompared{Base: base, Rows: []verification.CheckRow{}}, nil
}

// A Change's base is read once, by whichever arm is compared first, and
// the other arm reuses that reading, in either order and in one pass or
// two: the arms of a Change are never judged against two readings of one
// base (E2B-03).
//
// Kills: an arm compared before its sibling is loaded reading the base
// again (and paying for it again).
func TestAChangesBaseIsReadOnceWhicheverArmLandsFirst(t *testing.T) {
	t.Parallel()
	for _, order := range [][]string{{armVariant, armBaseline}, {armBaseline, armVariant}, {"both"}} {
		w := newEvalWorld(t, 1)
		cmp := &fakeComparer{given: map[ids.ID]bool{}}
		w.service.deps.Comparer = cmp
		e := w.start(t, "alice")
		if err := w.service.pass(context.Background()); err != nil {
			t.Fatal(err)
		}
		land := func(arm string) {
			for _, a := range w.eval(t, e.ID).Arms {
				if arm == "both" || a.Arm == arm {
					// sql-guarantee: a copy's landing is Work's whole flow; the driver reads only that its Change landed.
					if _, err := w.db.SQL().ExecContext(context.Background(), `UPDATE changes SET state='shipped' WHERE id=$1`, a.ChangeID); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
		for _, arm := range order {
			land(arm)
			if err := w.service.pass(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		got := w.eval(t, e.ID)
		if countArms(got, armScored) != 2 || cmp.baseReads != 1 || len(cmp.given) != 2 {
			t.Fatalf("%v: %d scored, base read %d times, compared %v", order, countArms(got, armScored), cmp.baseReads, cmp.given)
		}
		second := 0
		for _, given := range cmp.given {
			if given {
				second++
			}
		}
		if second != 1 {
			t.Fatalf("%v: the second arm was not given the first's base: %v", order, cmp.given)
		}
	}
}

// An eval's arms working from a project's code are live work in it: the
// project is named as their source until the eval ends, and a source that
// is being deleted (or is deleted) starts no eval.
//
// Kills: a deletion not told of an eval copying from the project; an eval
// started from a project being deleted.
func TestAnEvalsArmsAreLiveWorkInTheirSourceProject(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 1)
	ctx := context.Background()
	source, err := w.projects.BySlug(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	live := func() []ids.ID {
		t.Helper()
		live, err := database.Transact(ctx, w.db, func(tx *database.Tx) ([]ids.ID, error) { return w.service.LiveEvalsFromIn(ctx, tx, source.ID) })
		if err != nil {
			t.Fatal(err)
		}
		return live
	}
	if got := live(); len(got) != 0 {
		t.Fatalf("live evals before any started: %v", got)
	}
	e := w.start(t, "alice")
	if got := live(); len(got) != 1 || got[0] != e.ID {
		t.Fatalf("live evals from the source: %v, want [%d]", got, e.ID)
	}
	if err := w.service.Delete(as("alice"), e.ID); err != nil {
		t.Fatal(err)
	}
	if got := live(); len(got) != 0 {
		t.Fatalf("a deleted eval is still live work: %v", got)
	}
	if err := w.db.InTx(ctx, func(tx *database.Tx) error {
		return project.MarkDeletingIn(ctx, tx, source.ID, access.Actor{Name: "root"}, ids.Nil, false, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.service.Start(as("alice"), Start{Changes: w.sources, Profile: "frontier", BudgetMicros: 5_000_000, Shown: meaning}); !errors.Is(err, project.ErrNotActive) {
		t.Fatalf("an eval from a project being deleted: %v", err)
	}
}

// An eval and its receipts carry AppTown's clock, however far it is from
// the machine's.
func TestAnEvalIsStampedByAppTownsClock(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 1)
	started := time.Now().Add(-72 * time.Hour).Truncate(time.Second).UTC()
	deleted := started.Add(time.Hour)
	w.at.Store(started.Unix())
	e := w.start(t, "alice")
	if !e.StartedAt.Equal(started) {
		t.Fatalf("started at %v, want AppTown's %v", e.StartedAt, started)
	}
	w.at.Store(deleted.Unix())
	if err := w.service.Delete(as("alice"), e.ID); err != nil {
		t.Fatal(err)
	}
	for kind, want := range map[string]time.Time{"eval.started": started, "eval.deleted": deleted} {
		lines, err := audit.Recent(context.Background(), w.db, audit.Filter{KindPrefix: kind, Limit: 1})
		if err != nil || len(lines) != 1 || lines[0].At != want.Format(time.RFC3339) {
			t.Fatalf("%s receipt %+v %v, want it stamped %v", kind, lines, err, want)
		}
	}
}

// makeAdoptionChange turns a landed source Change into an adoption Change
// opened at revision 3 of its project's way of working.
func (w *evalWorld) makeAdoptionChange(t *testing.T, id ids.ID) {
	t.Helper()
	// sql-guarantee: an adoption Change needs a whole uplift admission and landing; the eval reads only its type, target and the revision it started from.
	if _, err := w.db.SQL().ExecContext(context.Background(), `UPDATE changes SET type='uplift', target_adoption_id=$2, expected_revision=3 WHERE id=$1`, id, idstest.N(77)); err != nil {
		t.Fatal(err)
	}
}

// shipAdoption marks an arm's Change shipped as an adoption Change, moved
// to its target (adopted) or not yet, with its working and landing runs.
func (w *evalWorld) shipAdoption(t *testing.T, a Arm, landing ids.ID, adopted int64) {
	t.Helper()
	// sql-guarantee: a shipped adoption Change needs a whole landing and settlement; the eval reads its state, target and stamp.
	if _, err := w.db.SQL().ExecContext(context.Background(), `UPDATE changes SET state='shipped', target_adoption_id=$2, adopted_revision=$3 WHERE id=$1`, a.ChangeID, idstest.N(78), adopted); err != nil {
		t.Fatal(err)
	}
	def, _ := json.Marshal(schema.Spec{Kind: "pipeline", Role: "landing"})
	w.work.mu.Lock()
	w.work.runs[a.ChangeID] = []*workflow.Run{{ID: landing, WorkflowName: "land", DefinitionJSON: string(def), Status: workflow.RunDone}, {ID: a.RunID, Status: workflow.RunDone}}
	w.work.mu.Unlock()
}

// An adoption Change is re-run as one: each copy starts on what its
// project had adopted when the Change was opened, and the arm opens an
// adoption Change moving it to the arm's way of working, with the checks
// that move brings, run with the eval's authority. No ordinary Change is
// opened.
func TestAnAdoptionChangeIsReRunAsOne(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 1)
	w.makeAdoptionChange(t, w.sources[0])
	e := w.start(t, "alice")
	if err := w.service.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := w.eval(t, e.ID)
	if countArms(got, armWorking) != 2 {
		t.Fatalf("arms: %+v", got.Arms)
	}
	if !slices.Equal(w.revisions, []int64{3, 3}) {
		t.Fatalf("copies started from revisions %v, want the change's own (3) for both", w.revisions)
	}
	if w.work.created != 0 || len(w.work.uplifts) != 2 || len(w.work.started) != 2 {
		t.Fatalf("opened %d ordinary changes and %d adoption changes, started %d lanes", w.work.created, len(w.work.uplifts), len(w.work.started))
	}
	for i, a := range got.Arms {
		plan, err := w.service.planOf(context.Background(), e.ID, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		up := w.work.uplifts[i]
		if up.ProjectID != a.ProjectID || up.Acceptance.TargetDigest != plan.Adoption.Digest || len(up.Acceptance.Checks) != 1 || up.Target == nil {
			t.Fatalf("arm %s opened %+v, want an adoption change to %s in its copy", a.Arm, up, plan.Adoption.Digest)
		}
	}
}

// An adoption arm is scored only once AppTown has moved its copy to the
// arm's way of working: shipped and not yet moved, it waits, uncompared;
// moved, it is scored; not moved and AppTown has stopped trying, it fails
// with why and frees its place.
func TestAnAdoptionArmIsScoredOnceItsCopyMoved(t *testing.T) {
	t.Parallel()
	w := newEvalWorld(t, 1)
	w.makeAdoptionChange(t, w.sources[0])
	e := w.start(t, "alice")
	if err := w.service.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	arms := w.eval(t, e.ID).Arms
	moved, refused := arms[0], arms[1]
	w.shipAdoption(t, moved, idstest.N(5001), 0)
	w.shipAdoption(t, refused, idstest.N(5002), 0)
	if err := w.service.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, a := range w.eval(t, e.ID).Arms {
		if a.State != armLanding || a.Reason != waitingForAdoption || a.Score.Landed || a.Score.Compared != nil || a.Score.CompareError != "" {
			t.Fatalf("arm %s before its copy moved: %s %q %+v", a.Arm, a.State, a.Reason, a.Score)
		}
	}
	// AppTown moves one copy and stamps its Change; it gives up on the other.
	if err := w.adoption.give(moved.ProjectID, moved.SourceProjectID, nil); err != nil {
		t.Fatal(err)
	}
	w.shipAdoption(t, moved, idstest.N(5001), 2)
	w.jobs.mu.Lock()
	w.jobs.ended[idstest.N(5002)] = "the source's way of working changed since the copy was prepared"
	w.jobs.mu.Unlock()
	if err := w.service.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := w.eval(t, e.ID)
	for _, a := range got.Arms {
		switch a.ID {
		case moved.ID:
			if a.State != armScored || a.Score.NotComparable || !a.Score.Landed || a.Reason != "" {
				t.Fatalf("the moved arm: %s %q %+v", a.State, a.Reason, a.Score)
			}
		case refused.ID:
			want := "it landed, but AppTown could not move the copy to its new way of working: the source's way of working changed since the copy was prepared"
			if a.State != armFailed || a.Reason != want {
				t.Fatalf("the arm AppTown could not move: %s %q, want failed %q", a.State, a.Reason, want)
			}
		}
	}
	if got.State != stateFinished {
		t.Fatalf("the eval is %s with every arm ended", got.State)
	}
}
