package remediation

// The facade's doors (architecture.md §6): remediation is a complete
// operation across Work and Conformance with tables and a worker of its own.
// Everything it needs and does not own arrives in Dependencies; every part is
// built once in New and reached only through the doors below.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

// Work is the transaction and execution port remediation consumes directly.
// Caller supplies the trusted engine identity; remediation owns the outer commit.
type Work interface {
	PrepareStop(ctx context.Context, projectID ids.ID) (*workflow.StopScope, error)
	AdmitRunIn(ctx context.Context, tx *database.Tx, in workflow.StartRequest, def workflow.PreparedRun) (*workflow.Run, error)
	RecheckLandingIn(ctx context.Context, tx *database.Tx, in workflow.LandingRecheck) (workflow.LandingRechecked, error)
	DispatchStarted(runID ids.ID) error
	DispatchCommittedRetry(runID ids.ID)
}

// Dependencies names what remediation needs and does not own.
type Dependencies struct {
	Work    Work                                                                    // Work service; no App or forwarding adapter
	Caller  context.Context                                                         // immutable identity for transaction participation
	Checks  *conformance.Service                                                    // the lens scope resolution reads
	Resolve func(context.Context, Item, VerificationScope) (StartDefinition, error) // lane, source, receipt, checks, brief, head
	Review  func(context.Context, Item, VerificationScope) Review                   // the post-landing review
	Busy    func(ctx context.Context, projectID ids.ID) bool                        // a person is in the workshop on this project
	// RequestPass queues a pass now (the job queue's remediation pass); nil
	// leaves every pass to the schedule.
	RequestPass func(context.Context) error
	Now         func() time.Time // AppTown's clock; nil is the real one
}

// Service is remediation, built once.
type Service struct {
	db         *database.DB
	deps       Dependencies
	pools      *pools
	attempts   Attempts
	outcomes   outcomes
	schedule   schedule
	creations  creations
	records    Reconciliation
	starter    *starter
	verifier   *verifier
	reconciler *reconciler
	scheduler  scheduler
	cool       *cooldown
	// holder names this process on the items it starts.
	holder string
}

// New builds every part in order. There is no lazy construction and no
// once-guard: a Service is complete when it returns.
func New(db *database.DB, deps Dependencies) *Service {
	s := &Service{db: db, deps: deps, pools: newPools(db, deps.Now)}
	s.attempts = NewAttempts(db, deps.Work, deps.Caller, deps.Now)
	s.outcomes = NewOutcomes(db, deps.Now)
	s.schedule = newSchedule(db, deps.Now)
	s.creations = NewCreations(db, deps.Now)
	s.records = NewReconciliation(db, deps.Work, deps.Caller, deps.Now)
	s.starter = NewStarter(s.attempts, func(ctx context.Context, item Item) (StartDefinition, error) {
		return deps.Resolve(ctx, item, s.VerificationScope(item))
	}, deps.Work.DispatchStarted)
	s.verifier = newVerifier(s.outcomes, func(ctx context.Context, item Item) Review {
		return deps.Review(ctx, item, s.VerificationScope(item))
	})
	host, _ := os.Hostname()
	s.holder = fmt.Sprintf("%s/%d", host, os.Getpid())
	s.cool = &cooldown{clock: deps.Now}
	s.reconciler = newReconciler(s.records, deps.Work.DispatchStarted, s.verifier.Verify, deps.Work.DispatchCommittedRetry, s.cool.hold)
	s.scheduler = newScheduler(s.schedule.Queue, s.cool.cooling, s.busy, s.start)
	return s
}

// busy says a project takes no remediation now: a person is in its
// workshop, or it is not active (being imported, being deleted, deleted),
// which the pass skips rather than starting work its doors would refuse.
func (s *Service) busy(ctx context.Context, projectID ids.ID) bool {
	if l, err := project.LifecycleIn(ctx, s.db, projectID); err != nil || l.State != project.Active {
		return true
	}
	if s.deps.Busy == nil {
		return false
	}
	return s.deps.Busy(ctx, projectID)
}

func (s *Service) start(ctx context.Context, item Item) (*workflow.Run, error) {
	return s.starter.Start(ctx, item, s.holder)
}

// --- the worker pool ---------------------------------------------------------------

// WorkerCount reads a pool's declared size (default when never set).
func (s *Service) WorkerCount(ctx context.Context, kind string) int {
	n, set, err := s.pools.count(ctx, kind)
	if err != nil {
		log.Printf("reading the %s worker pool: %v", kind, err)
	}
	if !set {
		if kind == WorkerKindRemediation {
			return defaultWorkers
		}
		return 0
	}
	return n
}

// SetWorkerCount declares a pool's size. Zero pauses the kind entirely.
func (s *Service) SetWorkerCount(ctx context.Context, kind string, n int, by string) error {
	return s.pools.set(ctx, kind, n, by)
}

// --- scope and start ---------------------------------------------------------------

// Scope lists the (project, check) findings a job would cover, from the
// CURRENT lens: verdicts in the findings lane (deterministic does-not-conform,
// or an AI flag a person confirmed). Unconfirmed AI flags, excepted, n/a and
// unassessed verdicts are never in scope (design §3, §12.6). The result is
// ranked worst-first within each project.
func (s *Service) Scope(ctx context.Context, scope, ref string) ([]conformance.Observation, error) {
	var view *conformance.View
	var err error
	if scope == "review" {
		rid, serr := ids.Parse(ref)
		if serr != nil || rid.IsZero() {
			return nil, fmt.Errorf("review scope needs a review id")
		}
		view, err = s.deps.Checks.View(ctx, rid)
	} else {
		view, err = s.deps.Checks.View(ctx, ids.Nil)
	}
	if err != nil {
		return nil, err
	}
	var out []conformance.Observation
	for slug, vs := range view.ByProject {
		if scope == "project" && slug != ref {
			continue
		}
		for _, v := range vs {
			if conformance.LaneOf(v, view.Adjudications) != conformance.LaneFindings {
				continue
			}
			if scope == "check" && conformance.CheckKey(v.Block, v.Version, v.Check) != ref {
				continue
			}
			out = append(out, v)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ProjectSlug != out[j].ProjectSlug {
			return out[i].ProjectSlug < out[j].ProjectSlug
		}
		if ri, rj := severityRankOf(out[i].Record.Severity), severityRankOf(out[j].Record.Severity); ri != rj {
			return ri < rj
		}
		return out[i].Check < out[j].Check
	})
	return out, nil
}

// Start creates a job over the resolved scope, with one queued item per
// (project, check), each freezing the verdict it was dispatched to fix. The
// worker picks items up on its next tick.
func (s *Service) Start(ctx context.Context, scope, ref, actor string, concurrency int) (*Remediation, []Item, error) {
	switch scope {
	case "all", "check", "project", "review":
	default:
		return nil, nil, fmt.Errorf("scope must be all, check, project or review")
	}
	findings, err := s.Scope(ctx, scope, ref)
	if err != nil {
		return nil, nil, err
	}
	return s.creations.Create(ctx, CreationRequest{
		Scope: scope, Ref: ref, Actor: actor, Concurrency: concurrency, Findings: findings,
	})
}

// --- a person's actions ------------------------------------------------------------

func (s *Service) setStatus(ctx context.Context, id ids.ID, from []string, to, by string) error {
	return s.attempts.status(ctx, id, from, to, by)
}

// Pause: no new items start; working ones finish their current run.
func (s *Service) Pause(ctx context.Context, id ids.ID, by string) error {
	return s.setStatus(ctx, id, []string{RemRunning}, RemPaused, by)
}

func (s *Service) Resume(ctx context.Context, id ids.ID, by string) error {
	return s.setStatus(ctx, id, []string{RemPaused}, RemRunning, by)
}

// Cancel: queued items are skipped; in-flight ones finish their current run
// and then stop (their Change stays open, attended).
func (s *Service) Cancel(ctx context.Context, id ids.ID, by string) error {
	return s.setStatus(ctx, id, []string{RemRunning, RemPaused}, RemCancelled, by)
}

// ErrItemScope is an item command naming an item that is not in the named
// remediation, or no item at all. ErrNoRemediation is a job id nobody
// started. Both are classified here so transports never read the store's
// sql.ErrNoRows or choose a status themselves.
var (
	errItemScope     = fault.New(fault.NotFound, "no such item in this remediation")
	errNoRemediation = fault.New(fault.NotFound, "no remediation has id")
)

// RetryItem re-queues a stuck item for a fresh attempt. The note becomes the
// first thing the next agent reads. The previous Change is superseded
// (design §12.9) when the attempt starts.
func (s *Service) RetryItem(ctx context.Context, jobID ids.ID, itemID ids.ID, by, note string) error {
	return s.attempts.Retry(ctx, jobID, itemID, by, note)
}

// SkipItem records a person giving up on an item, with the reason.
func (s *Service) SkipItem(ctx context.Context, jobID ids.ID, itemID ids.ID, by, reason string) error {
	return s.attempts.Skip(ctx, jobID, itemID, by, reason)
}

// --- what a person reads -----------------------------------------------------------

// Remediations lists the newest jobs, each with the sentence that says what
// it covers and how far it has got.
func (s *Service) Remediations(ctx context.Context, limit int) ([]Remediation, error) {
	jobs, err := readJobs(ctx, s.db, limit)
	if err != nil {
		return nil, err
	}
	for i := range jobs {
		jobs[i].Sentence = jobs[i].scopeSentence() + " · " + jobs[i].Sentence
	}
	return jobs, nil
}

// Remediation is one job and its sentence.
func (s *Service) Remediation(ctx context.Context, id ids.ID) (*Remediation, error) {
	m, err := readJob(ctx, s.db, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w %d", errNoRemediation, id)
	}
	if err != nil {
		return nil, err
	}
	m.Sentence = m.scopeSentence() + " · " + m.Sentence
	return m, nil
}

// Items are a job's items, or every worker holder when jobID is nil.
func (s *Service) Items(ctx context.Context, jobID *ids.ID) ([]Item, error) {
	return readItems(ctx, s.db, jobID)
}

// Attempts is each item's history for one job.
func (s *Service) Attempts(ctx context.Context, jobID ids.ID) (map[ids.ID][]Attempt, error) {
	return readAttempts(ctx, s.db, jobID)
}

// Occupancy is the pool, who holds it, and whether the queue is cooling down.
func (s *Service) Occupancy(ctx context.Context) (Occupancy, error) {
	o, err := readOccupancy(ctx, s.db)
	if err != nil {
		return Occupancy{}, err
	}
	if until, why := s.cool.state(); s.cool.now().Before(until) {
		o.CooldownUntil = until.UTC().Format(time.RFC3339)
		o.CooldownWhy = why
	}
	return o, nil
}

// ItemForChangeIn names the item a Change was opened for, for fix attribution.
func ItemForChangeIn(ctx context.Context, q database.Queryer, changeID ids.ID) (ids.ID, bool, error) {
	row, err := queries(q).ItemForChange(ctx, changeID)
	if errors.Is(err, sql.ErrNoRows) {
		return ids.Nil, false, nil
	}
	if err != nil {
		return ids.Nil, false, err
	}
	return row.ID, true, nil
}

// --- passes ------------------------------------------------------------------------

// Wake asks for a pass now, so a person's action is not held to the
// schedule. A failed request is logged; the next scheduled pass does the work.
func (s *Service) Wake(ctx context.Context) {
	if s.deps.RequestPass == nil {
		return
	}
	if err := s.deps.RequestPass(ctx); err != nil {
		log.Printf("remediation: asking for a pass: %v", err)
	}
}
