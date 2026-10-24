package conformance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/stamp"
)

// These scenarios record reviews through the owner's own admission,
// execution and receipt operations, then read the saved records and the
// current and historical projections over them. The test plays the
// evaluator: it supplies each assessment. What a real evaluation decides
// (an sh failure, an error exit, an excepted failure, a runtime probe with
// no app) is the fleet's guarantee and stays with the root fleet tests.

// headSource is the review's source port with one committed head per
// project; moving a head is how a later commit reaches the projections.
// Each scenario is sequential, so neither needs a lock.
type headSource struct {
	testCheckSource
	heads map[ids.ID]string
}

func (s *headSource) Head(_ context.Context, p ProjectSource) (SourceHead, error) {
	if sha := s.heads[p.ID]; sha != "" {
		return SourceHead{SHA: sha}, nil
	}
	return SourceHead{Unavailable: ErrSource.Error()}, nil
}

func (s *headSource) move(id ids.ID, sha string) { s.heads[id] = sha }

// projectionClock is AppTown's clock for these scenarios: it moves on a
// second at every reading, and jumps when a scenario lets time pass.
type projectionClock struct{ at time.Time }

func (c *projectionClock) now() time.Time       { c.at = c.at.Add(time.Second); return c.at }
func (c *projectionClock) pass(d time.Duration) { c.at = c.at.Add(d) }

type projectionFixture struct {
	db      *database.DB
	service *Service
	source  *headSource
	clock   *projectionClock
	blocks  map[string]ids.ID
}

func newProjectionFixture(t *testing.T) *projectionFixture {
	t.Helper()
	f := &projectionFixture{db: testDB(t), source: &headSource{heads: map[ids.ID]string{}},
		clock: &projectionClock{at: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)}, blocks: map[string]ids.ID{}}
	f.service = New(f.db, nil, f.source, f.clock.now)
	f.service.authority = testAuthority{}
	return f
}

const noFixme = `{"appliesTo": ["repository"], "name":"no-fixme","evaluator":"sh","sh":"! grep -r FIXME .","severity":"high","expects":"the rule holds"}`

// project creates a repo-backed project at a committed head that adopts
// one guidance block at a version, publishing that version when it is new.
func (f *projectionFixture) project(t *testing.T, slug, block string, version int, checks string) ids.ID {
	t.Helper()
	id := createTestProject(t, f.db, slug, slug, "main")
	blockID, ok := f.blocks[block]
	if !ok {
		blockID = stockCheckFixture(t, f.db, wow.StockBlock{Name: block, Body: "body", ChecksJSON: "[" + checks + "]"})
		f.blocks[block] = blockID
		for v := 1; v < version; v++ {
			if _, err := wow.NewLibrary(f.db).StockUpdate(t.Context(), wow.StockBlock{Name: block, Body: "body", ChecksJSON: "[" + checks + "]"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	pinCheckFixture(t, f.db, id, wow.ResolvedPin{BlockID: blockID, Name: block, Version: version})
	f.source.move(id, slug+"-1")
	return id
}

// judge is the evaluator's answer for one admitted check, given the
// exceptions the execution snapshotted for its project.
type judge func(p Project, c DeclaredCheck, exceptions []Disposition) Assessment

// outcomes judges each project's checks by slug. A failure covered by an
// exception in the review's snapshot is recorded excepted, naming the
// exception and keeping the failure as its detail, as the fleet does.
func outcomes(states map[string]Outcome) judge {
	return func(p Project, c DeclaredCheck, exceptions []Disposition) Assessment {
		a := Assessment{State: states[p.Slug], Detail: p.Slug + " judged"}
		if a.State == DoesNotConform {
			a.Detail = "FIXME left in the tree"
			if ref, ok := ExceptionFor(c, exceptions); ok {
				named, _ := json.Marshal(ref)
				a.State, a.ExceptedBy = Excepted, string(named)
			}
		}
		return a
	}
}

// review runs one review through admission, execution and receipt: every
// admitted check of every selected project gets the judge's assessment,
// with fix attribution when the owner says a flip needs one.
func (f *projectionFixture) review(t *testing.T, ctx context.Context, trigger string, selected []ids.ID, decide judge) (Review, []Verdict) {
	t.Helper()
	execution, err := f.service.BeginReview(ctx, ReviewRequest{Trigger: trigger, Projects: selected})
	if err != nil {
		t.Fatal(err)
	}
	projects, err := execution.Projects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var verdicts []Verdict
	for done, p := range projects {
		if err := execution.Progress(ctx, done+1, p.Slug); err != nil {
			t.Fatal(err)
		}
		exceptions, err := execution.Exceptions(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range p.Checks {
			a := decide(p, c, exceptions)
			a.ProjectID, a.Check = p.ID, CheckIdentity{Block: c.Block, Version: c.Version, Check: c.Check.Name}
			pending, err := execution.PrepareVerdict(ctx, a)
			if err != nil {
				t.Fatal(err)
			}
			var fix *FixAttribution
			if pending.NeedsAttribution() {
				fix = &FixAttribution{Commits: pending.Previous().SHA + ".." + pending.Verdict().SHA}
			}
			v, err := pending.Commit(ctx, fix)
			if err != nil {
				t.Fatal(err)
			}
			verdicts = append(verdicts, v)
		}
	}
	finished, err := execution.Finish(ctx, Completion{Projects: len(projects), ProjectsDone: len(projects)}, ReviewDone)
	if err != nil {
		t.Fatal(err)
	}
	return *finished, verdicts
}

func (f *projectionFixture) view(t *testing.T, reviewID ids.ID) *View {
	t.Helper()
	v, err := f.service.View(caller("reader"), reviewID)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (f *projectionFixture) savedVerdicts(t *testing.T, reviewID ids.ID) []Verdict {
	t.Helper()
	d, err := f.service.Review(caller("reader"), reviewID)
	if err != nil {
		t.Fatal(err)
	}
	return d.Verdicts
}

func currentObservation(t *testing.T, view *View, projectID ids.ID, check string) Observation {
	t.Helper()
	for _, project := range view.ByProject {
		for _, o := range project {
			if o.ProjectID == projectID && o.Check == check {
				return o
			}
		}
	}
	t.Fatalf("no current observation for project %d check %q", projectID, check)
	return Observation{}
}

// Saved reviews are retrievable with their full record, newest first, and
// an earlier review is not overwritten by a later one.
func TestSavedReviewsRetrievable(t *testing.T) {
	t.Parallel()
	f := newProjectionFixture(t)
	f.project(t, "alpha", "hygiene", 1, noFixme)
	clean := outcomes(map[string]Outcome{"alpha": Conforms})

	r1, _ := f.review(t, caller("alice"), "manual", nil, clean)
	r2, _ := f.review(t, caller("cron"), "scheduled", nil, clean)

	list, err := f.service.Reviews(caller("reader"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != r2.ID || list[1].ID != r1.ID {
		t.Fatalf("both reviews should list newest-first: %+v", list)
	}
	saved, err := f.service.Review(caller("reader"), r1.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := saved.Review
	if got.Trigger != "manual" || got.Actor != "alice" || got.StartedAt == "" || !got.FinishedAt.Valid {
		t.Fatalf("saved review must carry inputs + timing: %+v", got)
	}
	if got.Projects != 1 || got.Verdicts != 1 || got.Errors != 0 || got.Cost != 0 {
		t.Fatalf("saved review must carry results/errors/cost: %+v", got)
	}
	// Each review's verdicts are independently retrievable (r1 not clobbered).
	v1, v2 := f.savedVerdicts(t, r1.ID), f.savedVerdicts(t, r2.ID)
	if len(v1) != 1 || len(v2) != 1 || v1[0].ReviewID != r1.ID || v2[0].ReviewID != r2.ID {
		t.Fatalf("each review keeps its own verdicts: r1=%+v r2=%+v", v1, v2)
	}
}

// The current projection keeps error, stale, cannot-assess and
// not-applicable distinct from conforms, and the by-check worklist ranks
// the non-quiet states ahead of the quiet conforms.
func TestCurrentWorklistSurfacesStates(t *testing.T) {
	t.Parallel()
	f := newProjectionFixture(t)
	f.project(t, "conf", "hygiene", 1, noFixme)
	stale := f.project(t, "stale", "hygiene", 1, noFixme)
	f.project(t, "errp", "strict", 1, `{"appliesTo": ["repository"], "name":"boom","evaluator":"sh","sh":"exit 3","severity":"high","expects":"the rule holds"}`)
	f.project(t, "cannot", "live", 1, `{"appliesTo": ["repository"], "name":"probe","evaluator":"runtime","probe":"GET /healthz","expects":"the app answers"}`)
	f.project(t, "na", "prose", 2, `{"appliesTo": ["repository"], "name":"tone","evaluator":"llm","rubric":"a judgement","expects":"the rule holds"}`)

	f.review(t, caller("t"), "manual", nil, outcomes(map[string]Outcome{
		"conf": Conforms, "stale": Conforms, "errp": EvaluationError, "cannot": CannotAssess, "na": NotApplicable}))
	// A later commit on the stale project's branch: its verdict judged an older head.
	f.source.move(stale, "stale-2")

	current := f.view(t, ids.Nil)
	stateOf := func(slug string) ObservationState {
		if vs := current.ByProject[slug]; len(vs) > 0 {
			return vs[0].State
		}
		return nil
	}
	for slug, want := range map[string]ObservationState{"conf": Conforms, "stale": Stale, "errp": EvaluationError, "cannot": CannotAssess, "na": NotApplicable} {
		if got := stateOf(slug); got != want {
			t.Errorf("%s: %q want %q", slug, got, want)
		}
	}
	hy := current.ByCheck["hygiene@1·no-fixme"]
	if len(hy) != 2 || hy[0].State == Conforms {
		t.Fatalf("stale must rank ahead of conforms in the check worklist: %+v", hy)
	}
}

// The worklist projections of one saved review group its verdicts by
// project and by check, and surface non-conformance first.
func TestFleetWorklists(t *testing.T) {
	t.Parallel()
	f := newProjectionFixture(t)
	f.project(t, "good", "hygiene", 1, noFixme)
	f.project(t, "bad", "hygiene", 1, noFixme)
	rev, _ := f.review(t, caller("t"), "manual", nil, outcomes(map[string]Outcome{"good": Conforms, "bad": DoesNotConform}))

	saved := f.savedVerdicts(t, rev.ID)
	byProj := GroupByProject(RecordedObservations(saved))
	if len(byProj["good"]) != 1 || len(byProj["bad"]) != 1 {
		t.Fatalf("by-project should list each project's verdicts: %+v", byProj)
	}
	row := GroupByCheck(RecordedObservations(saved))["hygiene@1·no-fixme"]
	if len(row) != 2 {
		t.Fatalf("by-check should gather the same check across projects: %+v", row)
	}
	if row[0].State != DoesNotConform {
		t.Errorf("worklist should rank does-not-conform first, got %q", row[0].State)
	}
}

// The review lens returns exactly that review's verdicts and the exceptions
// in force at its walk, unaffected by later reviews or later revocation, and
// an excepted verdict keeps the failure underneath.
func TestReviewLensIsAnImmutableRecord(t *testing.T) {
	t.Parallel()
	f := newProjectionFixture(t)
	beta := f.project(t, "beta", "hygiene", 1, noFixme)
	failing := outcomes(map[string]Outcome{"beta": DoesNotConform})

	// An exception covers the check's own fingerprint, so review 1 is excepted.
	disposition, err := f.service.CreateDisposition(caller("t"), ExceptionRequest{
		ProjectID: beta, Fingerprint: "guidance|hygiene@1|no-fixme|", Kind: AcceptedRisk,
		Justification: "legacy note, tracked elsewhere",
	})
	if err != nil {
		t.Fatal(err)
	}
	rev1, vs1 := f.review(t, caller("t"), "manual", nil, failing)
	if len(vs1) != 1 || vs1[0].State != Excepted {
		t.Fatalf("review 1 should record excepted, got %+v", vs1)
	}
	if !strings.Contains(vs1[0].Detail, "FIXME") || !strings.Contains(vs1[0].ExceptedBy, "accepted") {
		t.Fatalf("excepted must keep the failure underneath and name the exception: %+v", vs1[0])
	}

	// The exception is later revoked and review 2 runs.
	if _, err := f.service.RevokeDisposition(caller("t"), disposition.ID); err != nil {
		t.Fatal(err)
	}
	rev2, vs2 := f.review(t, caller("t"), "manual", nil, failing)
	if len(vs2) != 1 || vs2[0].State != DoesNotConform {
		t.Fatalf("review 2 should record does-not-conform, got %+v", vs2)
	}

	// Lens on review 1: still excepted, and its snapshot still lists the exception.
	v1 := f.view(t, rev1.ID)
	if v1.Lens.Kind != "review" || v1.Lens.Review.ID != rev1.ID {
		t.Fatalf("lens should be review %d: %+v", rev1.ID, v1.Lens)
	}
	if got := v1.ByProject["beta"]; len(got) != 1 || got[0].State != Excepted {
		t.Fatalf("review-1 lens must still read excepted, got %+v", got)
	}
	if ex := v1.Exceptions["beta"]; len(ex) != 1 || ex[0].ID != disposition.ID || ex[0].Status != AcceptedRisk {
		t.Fatalf("review-1 lens must show the snapshotted exception, got %+v", v1.Exceptions)
	}
	if s := confirmationStanding(t, v1, "beta"); s.Standing != StandingExcepted {
		t.Fatalf("review-1 standing should be conforms-with-exceptions, got %+v", s)
	}
	// Lens on review 2: does not conform, no exception.
	v2 := f.view(t, rev2.ID)
	if s := confirmationStanding(t, v2, "beta"); s.Standing != StandingDoesNot || len(v2.Exceptions["beta"]) != 0 {
		t.Fatalf("review-2 lens should be does-not-conform with no exceptions: %+v %+v", s, v2.Exceptions)
	}
	// Delta of review 2 vs 1: the excepted item became an open finding, so it is new.
	if d := v2.Lens.Delta; d == nil || d.Previous != rev1.ID || d.New != 1 || d.ComparedProjects != 1 {
		t.Fatalf("delta should be 1 new over 1 compared project vs review %d: %+v", rev1.ID, d)
	}
}

// Under the current lens an excepted verdict whose exception has since
// expired by AppTown's clock reveals the failure underneath without
// re-evaluation, while the saved review keeps its original decision.
func TestExpiredExceptionRevealsTheFinding(t *testing.T) {
	t.Parallel()
	f := newProjectionFixture(t)
	beta := f.project(t, "beta", "hygiene", 1, noFixme)
	if _, err := f.service.CreateDisposition(caller("t"), ExceptionRequest{
		ProjectID: beta, Fingerprint: "guidance|hygiene@1|no-fixme|", Kind: AcceptedRisk,
		Justification: "for an hour", ExpiresAt: stamp.Of(f.clock.now().Add(time.Hour)),
	}); err != nil {
		t.Fatal(err)
	}
	f.review(t, caller("t"), "manual", nil, outcomes(map[string]Outcome{"beta": DoesNotConform}))
	if cur := currentObservation(t, f.view(t, ids.Nil), beta, "no-fixme"); cur.State != Excepted {
		t.Fatalf("with the exception in force, current should be excepted: %+v", cur)
	}
	// The exception's end passes; nothing is reviewed again.
	f.clock.pass(2 * time.Hour)
	cur := currentObservation(t, f.view(t, ids.Nil), beta, "no-fixme")
	if cur.State != DoesNotConform || !strings.Contains(cur.Detail(), "FIXME") {
		t.Fatalf("after expiry the underlying finding must show, got %+v", cur)
	}
	// The saved review still carries its original exception decision.
	found := false
	for _, verdict := range f.savedVerdicts(t, cur.Record.ReviewID) {
		if verdict.ID == cur.Record.ID {
			found = true
			if verdict.State != Excepted {
				t.Fatalf("saved review lost its original exception: %+v", verdict)
			}
		}
	}
	if !found {
		t.Fatal("saved review lost the excepted verdict")
	}
}

// The delta compares only comparable reviews: a single-project review is
// compared over that one project, never the fleet; summaries and a
// project's history follow the same records.
func TestReviewDeltaOverComparableProjects(t *testing.T) {
	t.Parallel()
	f := newProjectionFixture(t)
	f.project(t, "alpha", "hygiene", 1, noFixme)
	beta := f.project(t, "beta", "hygiene", 1, noFixme)
	f.project(t, "gamma", "hygiene", 1, noFixme)
	states := map[string]Outcome{"alpha": Conforms, "beta": DoesNotConform, "gamma": DoesNotConform}

	f.review(t, caller("t"), "manual", nil, outcomes(states)) // review 1: beta + gamma open
	// beta is fixed by a later commit; a beta-only review runs.
	f.source.move(beta, "beta-2")
	states["beta"] = Conforms
	rev2, _ := f.review(t, caller("t"), "manual", []ids.ID{beta}, outcomes(states))
	d := f.view(t, rev2.ID).Lens.Delta
	if d == nil || d.ComparedProjects != 1 || d.Fixed != 1 || d.New != 0 || d.StillOpen != 0 {
		t.Fatalf("beta-only review should compare 1 project with 1 fixed, got %+v", d)
	}
	// A fleet review then compares with review 2, which covered only beta.
	rev3, _ := f.review(t, caller("t"), "manual", nil, outcomes(states))
	d = f.view(t, rev3.ID).Lens.Delta
	if d == nil || d.Previous != rev2.ID || d.ComparedProjects != 1 {
		t.Fatalf("fleet review vs a beta-only review compares beta alone, got %+v", d)
	}
	sums, err := f.service.Reviews(caller("reader"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 3 || sums[0].ID != rev3.ID || sums[0].Findings != 1 || sums[0].ProjectsWithFindings != 1 {
		t.Fatalf("review summaries: %+v", sums)
	}
	hist, err := f.service.ProjectHistory(caller("reader"), beta, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 3 || hist[0].Findings != 1 || hist[1].Findings != 0 || hist[2].Standing != StandingConforms {
		t.Fatalf("beta history should read open, fixed, conforms: %+v", hist)
	}
}
