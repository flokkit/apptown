package workflow

import (
	"context"
	"maps"
	"slices"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/scm"
)

// Service is the supported caller-facing Work API. Its stores and query plans
// are private; actor strings in commands never establish permission.
type Service struct {
	stepCtx func(context.Context, ids.ID) context.Context
	// attemptWatch is how often a running step's run is looked at.
	attemptWatch time.Duration
	// beforeFence is a test seam: it runs after a fenced transaction's
	// context is chosen and before the transaction (Execution.fenced).
	beforeFence func(*Execution)
	masker      func(context.Context) func(string) string
	life        context.Context // Work's lifetime (New)
	evalGate    EvalGate
	db          *database.DB
	authority   operationAuthority
	wait        SlotWait
	means       LandingMeans
	inAdvance   InAdvanceQuestions
	runners     map[string]Runner
	// recoverers continue a running step's recorded work after a restart,
	// in the step's place in its stage (ResumePending).
	recoverers map[string]Runner

	// The parts New builds once, reached from outside only through the
	// Service's operations.
	stages          Stages
	assertions      *Assertions
	abandonments    Abandonments
	stepAttempts    stepAttempts
	stepRetries     stepRetries
	sendbacks       sendBackStore
	landingRechecks landingRechecks
	landingWork     LandingWork
	landings        landings
	narratives      narratives
	dispatchPlans   dispatchPlans
	runAdmissions   runAdmissions
	workspaces      workspaces
	creator         Creator
	importer        changeImporter
	starter         *starter
	scmService      *scm.Service
	pins            landingPins
	adds            approvalAdds
	dispatcher      *dispatcher
	merges          *Merges
	advancer        *advancer
	// placement is where checks run (placement.go): recorded on each check
	// result, and with a check worker, the only place gates accept. Scripts
	// run where tasks do, recorded on each script result.
	placement, scripts Placement
	// dataDir holds the project repositories: closing a change with nothing
	// to change reads it (nothing_to_change.go).
	dataDir string
	// clocked is AppTown's clock, which writes the times workflow's rules
	// compare (Dependencies.Now; the zero value is the real clock).
	clocked
}

// Dependencies is what Work needs and does not own (architecture.md §1,
// §5.7): the authority that answers for the principal in an operation's
// context, the sources it reads over time, and the recorders other
// subsystems supply for the effects Work's transactions carry.
type Dependencies struct {
	Authority    access.ReadAuthorizer
	Scanners     ScannerSource                                                         // Conformance's attachment snapshot, pinned at stage entry
	Laws         LawRules                                                              // policy violations for a run
	Abandonment  AbandonRecorder                                                       // effects when work stops
	Completion   CompletionRecorder                                                    // Shipping's participant at completion
	Merge        MergeEffects                                                          // the SCM driver and what happens after a merge
	Landings     LandingAdoptions                                                      // Adoption's revision pair for a landing
	LandingMeans LandingMeans                                                          // what the project's landing approvals would say
	InAdvance    InAdvanceQuestions                                                    // what a yes given in advance on an adoption would mean
	ChangeAdds   AddsReader                                                            // what an approval's commit brings into the app
	Budget       LandingRetryBudget                                                    // charge for a landing retry
	Narratives   NarrativeReleaseNotes                                                 // release notes filled by a model
	Resolve      func(context.Context, StartRequest, StartSource) (PreparedRun, error) // Adoption's port: the pinned procedure
	LandingWork  LandingWork                                                           // publishes a change's committed work for a landing
	Runners      map[string]Runner                                                     // the step kinds the daemon runs
	Recoverers   map[string]Runner                                                     // kinds whose recorded work a restarted daemon adopts
	Chats        Presence                                                              // whether a chat holding a change has ended
	Report       func(error)                                                           // dispatch failures
	Slots        SlotWait
	Git          Git
	Repositories *scm.RepositoryLocks
	Automatic    []string // step types that run without a person
	DataDir      string

	// EvalGate is an eval's "may this proceed?" for its copies, asked as
	// each step is claimed (eval_authority.go); nil admits every step.
	EvalGate EvalGate

	// CheckPlacement is where the check executor runs, TaskPlacement where
	// scripts run: the worker or this machine (placement.go).
	CheckPlacement, TaskPlacement Placement

	// Masker gives a function replacing known secret values in text Work
	// records (a step's evidence), from one read of the stores for the whole
	// operation; nil records text as given. A convenience against an
	// accidental echo, not a boundary.
	Masker func(context.Context) func(string) string

	// StepContext adds to a step's context what its project's work runs
	// under besides Work's own (the project's cache volume); nil adds nothing.
	StepContext func(ctx context.Context, projectID ids.ID) context.Context

	// AttemptWatch is how often Work looks at a running step's run while its
	// runner runs (lost_attempt.go); zero is defaultAttemptWatch.
	AttemptWatch time.Duration

	// Now is AppTown's clock; nil is the real one.
	Now func() time.Time
}

// stepContext is ctx with what deps.StepContext adds for the project.
func (s *Service) stepContext(ctx context.Context, projectID ids.ID) context.Context {
	if s.stepCtx == nil {
		return ctx
	}
	return s.stepCtx(ctx, projectID)
}

// Now is AppTown's clock as Work holds it, for a participant that writes
// Work's records outside Work (AddArtifactIn), so their times compare with
// Work's own.
func (s *Service) Now() time.Time { return s.now() }

// New builds Work once. Its private parts communicate directly; AppTown supplies
// authority and external effects rather than relaying calls between these parts.
// life is Work's lifetime: dispatched steps, their recovery and the
// engine's own advancement run under it, and stop with it.
func New(life context.Context, db *database.DB, deps Dependencies) *Service {
	automatic := slices.Clone(deps.Automatic)
	isAutomatic := func(kind string) bool { return slices.Contains(automatic, kind) }
	authority := commandAuthority{db: db, checked: deps.Authority, visibility: deps.Authority, clocked: clocked{deps.Now}}
	s := &Service{life: life, db: db, authority: authority, wait: deps.Slots, means: deps.LandingMeans, inAdvance: deps.InAdvance, runners: maps.Clone(deps.Runners), recoverers: maps.Clone(deps.Recoverers), placement: deps.CheckPlacement, scripts: deps.TaskPlacement, evalGate: deps.EvalGate, stepCtx: deps.StepContext, masker: deps.Masker, clocked: clocked{deps.Now}, attemptWatch: deps.AttemptWatch}
	if s.attemptWatch <= 0 {
		s.attemptWatch = defaultAttemptWatch
	}
	s.stages = NewStages(deps.Scanners, isAutomatic)
	s.abandonments = NewAbandonments(db, deps.Abandonment, deps.Repositories)
	s.abandonments.clocked = s.clocked
	s.stepAttempts = newStepAttempts(db, isAutomatic)
	s.stepAttempts.clocked = s.clocked
	s.stepRetries = newStepRetries(db, automatic)
	s.stepRetries.clocked = s.clocked
	s.sendbacks = sendBackStore{db, automatic, s.abandonments, deps.Chats, s.clocked}
	s.landingRechecks = newLandingRechecks(db, automatic, deps.Budget)
	s.landingRechecks.clocked = s.clocked
	s.landingWork = deps.LandingWork
	s.landings = newLandings(db, deps.Landings)
	s.landings.clocked = s.clocked
	s.landings.eligible = authority.mayApprove
	s.narratives = newNarratives(db, deps.Narratives)
	s.narratives.clocked = s.clocked
	s.dispatchPlans = newDispatchPlans(db, automatic)
	s.dispatchPlans.clocked = s.clocked
	s.runAdmissions = newRunAdmissions(db, s.stages, deps.Chats)
	s.runAdmissions.records.clocked = s.clocked
	s.workspaces = newWorkspaces(db, deps.Git, deps.Repositories)
	s.workspaces.clocked = s.clocked
	s.dataDir = deps.DataDir
	if deps.Repositories != nil {
		s.pins = landingPins{db: db, dataDir: deps.DataDir, clocked: s.clocked}
	}
	s.adds = approvalAdds{reader: deps.ChangeAdds, pins: s.pins}
	s.landings.adds = s.adds
	s.creator = NewCreator(db, s.prepareCreated)
	s.creator.records.clocked = s.clocked
	s.importer = newImporter(db, deps.Git, deps.Repositories, s.prepareCreated)
	s.importer.creator.records.clocked = s.clocked
	if deps.Merge != nil {
		work := NewSCMWork(db, deps.DataDir, deps.Laws)
		work.placement, work.evals, work.clocked = deps.CheckPlacement, deps.EvalGate, s.clocked
		s.scmService = scm.New(db, work, deps.Repositories, s.clocked.clock)
		s.merges = NewMerges(db, deps.DataDir, deps.Laws, s.scmService, deps.Merge)
		s.merges.placement, s.merges.evals, s.merges.clocked = deps.CheckPlacement, deps.EvalGate, s.clocked
	}
	var assertions MergeObserver
	var checks advanceChecks
	if s.merges != nil {
		checks = s.merges
		assertions = s.merges
	}
	s.assertions = NewAssertions(db, assertions)
	s.assertions.clocked = s.clocked
	s.advancer = newAdvancer(db, advanceRuntime{Stages: s.stages, Assertions: s.assertions, Checks: checks, Completion: deps.Completion, Carry: s.answerFromAcceptance, EndNothing: s.endNothingToChange, Placement: deps.CheckPlacement, clocked: s.clocked})
	s.starter = newStarter(db, s.stages, deps.Chats, startRuntime{
		Source: s.runAdmissions.Source,
		Supersede: func(ctx context.Context, failed RunRef, next string) error {
			_, err := s.abandonments.SupersedeFailedLanding(ctx, failed, next)
			return err
		},
		Missing:   s.runAdmissions.MissingArtifact,
		Resolve:   deps.Resolve,
		Assent:    s.requireLanding,
		LandingBy: s.landingBy,
		AcceptedEvalAuthority: func(ctx context.Context, req StartRequest) (string, error) {
			return acceptedEvalAuthorityIn(ctx, db, req)
		},
		Prepare: func(ctx context.Context, run *Run) error {
			if err := s.workspaces.PrepareRun(ctx, RunRef{ID: run.ID, ProjectID: run.ProjectID}); err != nil {
				return err
			}
			return s.pins.pinPrepared(ctx, run)
		},
		Dispatch: func(ctx context.Context, run *Run) error {
			return s.DispatchStarted(run.ID)
		},
	}, s.clocked)
	runners := make(map[string]func(ids.ID), len(s.runners))
	for kind := range s.runners {
		runners[kind] = func(id ids.ID) {
			if err := s.executeRunner(s.life, id, kind); err != nil {
				s.dispatcher.report(err)
			}
		}
	}
	recoverers := make(map[string]func(ids.ID), len(s.recoverers))
	for kind := range s.recoverers {
		recoverers[kind] = func(id ids.ID) {
			if err := s.recoverRunner(s.life, id, kind); err != nil {
				s.dispatcher.report(err)
			}
		}
	}
	s.dispatcher = newDispatcher(db, dispatcherConfig{Life: life, Runners: runners, Recoverers: recoverers, Stages: dispatchStages{s}, Report: deps.Report, Now: s.clocked.clock})
	return s
}
