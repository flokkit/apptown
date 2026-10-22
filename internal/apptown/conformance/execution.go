package conformance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/conformance/internal/conformancesql"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/stamp"
)

// ReviewRequest is what a review was asked to do, kept on its record so the
// job that runs a queued review reads it there by ID. Projects nil is the
// whole estate; an explicit empty selection is no projects. Build lets a
// cache miss on an exec-image tool build the app image, within BuildBudget
// builds (0 is the default budget).
type ReviewRequest struct {
	Trigger     string
	Projects    []ids.ID
	Build       bool
	BuildBudget int
}

// ErrNotQueued is a claim of a review or what-if that another delivery
// already claimed, or that has ended: there is nothing for this one to run.
var ErrNotQueued = errors.New("not queued: already claimed or ended")

// Execution is an admitted review capability. It cannot be constructed for an
// existing review or rebound to another project/check/source by its caller.
// Admission survives a long evaluation; terminal state is still checked in SQL.
type Execution struct {
	service    *Service
	mu         sync.Mutex
	review     Review
	request    ReviewRequest
	planned    bool
	projects   []Project
	sources    map[ids.ID]SourceHead
	exceptions map[ids.ID][]Disposition
}

// BeginReview admits a review that its caller runs at once (remediation's
// own check of one item). A review someone asks for is queued instead
// (AdmitReviewIn).
func (s *Service) BeginReview(ctx context.Context, in ReviewRequest) (*Execution, error) {
	in.Projects = slices.Clone(in.Projects)
	who, err := s.require(ctx, Permission{Action: RunReview, Projects: slices.Clone(in.Projects)})
	if err != nil {
		return nil, err
	}
	review, err := s.records.create(ctx, s.db, in, who, ReviewRunning)
	if err != nil {
		return nil, err
	}
	return s.execution(*review, in), nil
}

// AdmitReviewIn admits a review, with the caller's authority, as queued in
// the caller's transaction, which also queues the job that runs it: neither
// commits alone. The record keeps the request; the job names only its ID.
func (s *Service) AdmitReviewIn(ctx context.Context, tx *database.Tx, in ReviewRequest) (*Review, error) {
	in.Projects = slices.Clone(in.Projects)
	who, err := s.require(ctx, Permission{Action: RunReview, Projects: slices.Clone(in.Projects)})
	if err != nil {
		return nil, err
	}
	return s.records.create(ctx, tx, in, who, ReviewQueued)
}

// ClaimReview starts a queued review for the one delivery that claims it,
// from its saved request. It needs no caller: its authority is the
// admission, and it reaches only what that record asked. A review already
// claimed or ended is ErrNotQueued.
func (s *Service) ClaimReview(ctx context.Context, id ids.ID) (*Execution, error) {
	row, err := queries(s.db).ClaimQueuedReview(ctx, conformancesql.ClaimQueuedReviewParams{ID: id, Queued: ReviewQueued.String(), Running: ReviewRunning.String()})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotQueued
	}
	if err != nil {
		return nil, err
	}
	review, err := review(row)
	if err != nil {
		return nil, err
	}
	in, err := reviewRequest(row)
	if err != nil {
		return nil, err
	}
	return s.execution(review, in), nil
}

func (s *Service) execution(review Review, in ReviewRequest) *Execution {
	return &Execution{service: s, review: review, request: in, sources: map[ids.ID]SourceHead{}, exceptions: map[ids.ID][]Disposition{}}
}

// Options is how the review was asked to run.
func (e *Execution) Options() ReviewOptions {
	return ReviewOptions{Projects: slices.Clone(e.request.Projects), Build: e.request.Build, BuildBudget: e.request.BuildBudget}
}

func (e *Execution) Review() Review { e.mu.Lock(); defer e.mu.Unlock(); return e.review }

func cloneProjects(projects []Project) []Project {
	out := slices.Clone(projects)
	for i := range out {
		out[i].Checks = slices.Clone(out[i].Checks)
		for j := range out[i].Checks {
			out[i].Checks[j].Check.Files = slices.Clone(out[i].Checks[j].Check.Files)
		}
	}
	return out
}
func (e *Execution) plan(ctx context.Context) error {
	if e.planned {
		return nil
	}
	projects, err := e.service.records.prepareProjects(ctx, e.request.Projects)
	if err != nil {
		return err
	}
	e.projects = projects
	e.planned = true
	return nil
}
func (e *Execution) Projects(ctx context.Context) ([]Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.plan(ctx); err != nil {
		return nil, err
	}
	return cloneProjects(e.projects), nil
}
func (e *Execution) project(ctx context.Context, id ids.ID) (Project, error) {
	if err := e.plan(ctx); err != nil {
		return Project{}, err
	}
	for _, p := range e.projects {
		if p.ID == id {
			return p, nil
		}
	}
	return Project{}, ErrScope
}

// Source pins one committed head for each project. Missing source fails the
// review explicitly; callers cannot supply a guessed SHA to assessment writes.
func (e *Execution) Source(ctx context.Context, id ids.ID) (SourceHead, error) {
	if err := ctx.Err(); err != nil {
		return SourceHead{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	p, err := e.project(ctx, id)
	if err != nil {
		return SourceHead{}, err
	}
	if head, ok := e.sources[id]; ok {
		return head, nil
	}
	head, err := e.service.sourceHead(ctx, p)
	if err != nil {
		return SourceHead{}, err
	}
	if head.SHA == "" {
		return SourceHead{}, fmt.Errorf("%w: %s: %s", ErrSource, p.Slug, head.Unavailable)
	}
	e.sources[id] = head
	return head, nil
}

// A project gets exactly one exception snapshot for this review, even if more
// than one evaluator asks for it. Saved facts do not change during evaluation.
func (e *Execution) Exceptions(ctx context.Context, id ids.ID) ([]Disposition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.project(ctx, id); err != nil {
		return nil, err
	}
	if saved, ok := e.exceptions[id]; ok {
		return slices.Clone(saved), nil
	}
	saved, err := e.service.records.snapshotExceptions(ctx, e.review.ID, id)
	if err != nil {
		return nil, err
	}
	e.exceptions[id] = saved
	return slices.Clone(saved), nil
}

func (e *Execution) Progress(ctx context.Context, done int, current string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.plan(ctx); err != nil {
		return err
	}
	if done < 0 || done > len(e.projects) {
		return fmt.Errorf("invalid review progress")
	}
	found := current == ""
	for _, p := range e.projects {
		found = found || p.Slug == current
	}
	if !found {
		return ErrScope
	}
	next := e.review
	next.ProjectsTotal = len(e.projects)
	next.ProjectsDone = done
	next.CurrentProject = current
	if err := e.service.records.progress(ctx, next); err != nil {
		return err
	}
	e.review = next
	return nil
}

type Completion struct {
	Projects, ProjectsDone, Errors int
	Cost                           float64
}

func (e *Execution) Finish(ctx context.Context, result Completion, state ReviewState) (*Review, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	// A preparation error may finish the admitted review before a plan exists.
	if result.Projects < 0 || result.ProjectsDone < 0 || result.Errors < 0 || result.Cost < 0 {
		return nil, fmt.Errorf("invalid review completion")
	}
	if e.planned && (result.Projects > len(e.projects) || result.ProjectsDone > len(e.projects)) {
		return nil, ErrScope
	}
	next := e.review
	next.Projects = result.Projects
	next.ProjectsDone = result.ProjectsDone
	next.Errors = result.Errors
	next.Cost = result.Cost
	saved, err := e.service.records.finish(ctx, next, state)
	if err != nil {
		return nil, err
	}
	e.review = *saved
	return saved, nil
}

// Assessment supplies evaluation results, not record identity or authority.
// The execution fills identity, source, declaration and recording time itself.
type Assessment struct {
	ProjectID                                  ids.ID
	Check                                      CheckIdentity
	State                                      Outcome
	Detail                                     string
	EvaluatorVersion, ExceptedBy, EvidenceHash string
}

type PendingVerdict struct {
	service   *Service
	admission verdictAdmission
	previous  Verdict
	needsFix  bool
}

func (p *PendingVerdict) Previous() Verdict      { return p.previous }
func (p *PendingVerdict) Verdict() Verdict       { return p.admission.Verdict }
func (p *PendingVerdict) NeedsAttribution() bool { return p.needsFix }

func (e *Execution) PrepareVerdict(ctx context.Context, in Assessment) (*PendingVerdict, error) {
	if _, err := in.State.Value(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	p, err := e.project(ctx, in.ProjectID)
	id := e.review.ID
	var declared *DeclaredCheck
	if err == nil {
		for i := range p.Checks {
			c := &p.Checks[i]
			if c.Block == in.Check.Block && c.Version == in.Check.Version && c.Check.Name == in.Check.Check {
				copy := *c
				declared = &copy
				break
			}
		}
	}
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if declared == nil || (!in.Check.ProjectID.IsZero() && in.Check.ProjectID != in.ProjectID) {
		return nil, ErrScope
	}
	source, err := e.Source(ctx, in.ProjectID)
	if err != nil {
		return nil, err
	}
	v := Verdict{ReviewID: id, ProjectID: p.ID, ProjectSlug: p.Slug, SHA: source.SHA, Block: declared.Block, Version: declared.Version, Check: declared.Check.Name,
		Evaluator: check.Evaluator(declared.Check), EvaluatorVer: in.EvaluatorVersion, Mode: check.ModeFor(declared.Check), State: in.State, Severity: declared.Check.Severity, Detail: in.Detail, At: stamp.Of(e.service.records.now()).String(), ExceptedBy: in.ExceptedBy, EvidenceHash: in.EvidenceHash}
	if v.Severity == "" {
		v.Severity = "note"
	}
	previous, err := e.service.records.latestVerdict(ctx, p.ID, v.Block, v.Version, v.Check)
	if _, err := database.Lookup(err); err != nil {
		return nil, err
	}
	needed, err := fixRequired(ctx, e.service.records.db, previous, v)
	if err != nil {
		return nil, err
	}
	return &PendingVerdict{service: e.service, admission: verdictAdmission{Verdict: v, PreviousID: previous.ID}, previous: previous, needsFix: needed}, nil
}

// Commit rechecks the previous verdict, required confirmation and running
// review in the same transaction as the verdict, fix and required audit.
func (p *PendingVerdict) Commit(ctx context.Context, fix *FixAttribution) (Verdict, error) {
	request := p.admission
	if fix != nil {
		copy := *fix
		copy.Changes = slices.Clone(fix.Changes)
		request.Fix = &copy
	}
	return p.service.records.recordVerdict(ctx, request)
}
