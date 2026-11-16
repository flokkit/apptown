package workflow

import (
	"context"
	"reflect"
	"slices"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// standingInspector observes exact source and independently validates supported
// dependency inputs. It cannot accept, publish, land or deploy the result.
type standingInspector interface {
	InspectRoutine(context.Context, Source, JanitorChoices) (StandingInspection, error)
}

type standingAdoptionInspector interface {
	InspectRoutineWithAdoption(context.Context, Source, JanitorChoices) (StandingInspection, error)
}
type standingAdoptions interface {
	CheckRoutineTargetIn(context.Context, *database.Tx, ids.ID, ids.ID, ids.ID, int64, []byte) (adoption.RoutineTarget, error)
}

// standingPresence adds observation only; the existing FolderClear operation
// can stop containers and must never be called by an inspection.
type standingPresence interface {
	Presence
	FolderUnwritten(context.Context, ids.ID) (bool, error)
}
type StandingValidation struct {
	Image             string `json:"image"`
	ReportDigest      string `json:"reportDigest"`
	ScratchSettlement string `json:"scratchSettlement"`
}
type StandingInspection struct {
	Classification string              `json:"classification"`
	Reason         string              `json:"reason"`
	Classifier     string              `json:"classifier"`
	Base           string              `json:"base"`
	Candidate      string              `json:"candidate"`
	Tree           string              `json:"tree"`
	ChangedFiles   []string            `json:"changedFiles"`
	Validation     *StandingValidation `json:"validation,omitempty"`
}
type StandingAssessment struct {
	Level               string                  `json:"level"`
	Adoption            *adoption.RoutineTarget `json:"adoption,omitempty"`
	RuleID              ids.ID                  `json:"ruleId"`
	RuleRevision        int64                   `json:"ruleRevision"`
	Choices             JanitorChoices          `json:"choices"`
	ProjectID           ids.ID                  `json:"projectId"`
	ChangeID            ids.ID                  `json:"changeId"`
	RunID               ids.ID                  `json:"runId"`
	Stage               string                  `json:"stage"`
	RetryRevision       int64                   `json:"retryRevision"`
	AdoptionID          ids.ID                  `json:"adoptionId"`
	AdoptionRevision    int64                   `json:"adoptionRevision"`
	Inspection          StandingInspection      `json:"inspection"`
	ProcessingAvailable bool                    `json:"processingAvailable"`
}

type standingObservation struct {
	source    Source
	run       Run
	authority scm.Authority
	change    Change
}

// standingWitness includes only facts the consent depends on. Narrative and
// display updates can run during inspection without changing that consent.
type standingWitness struct {
	source              Source
	authority           scm.Authority
	runID               ids.ID
	status              RunState
	stage               string
	retryRevision       int64
	adoptionID          ids.ID
	workflowName        string
	workflowVersion     int
	changeID            ids.ID
	state               ChangeState
	changeType          string
	changeRunID         ids.ID
	branch              string
	baseSHA             string
	targetAdoptionID    ids.ID
	targetCandidateJSON string
	expectedRevision    int64
	adoptedRevision     int64
}

func (o standingObservation) witness() standingWitness {
	return standingWitness{
		source: o.source, authority: o.authority,
		runID: o.run.ID, status: o.run.Status, stage: o.run.CurrentStage,
		retryRevision: o.run.RetryRevision, adoptionID: o.run.AdoptionID,
		workflowName: o.run.WorkflowName, workflowVersion: o.run.WorkflowVersion,
		changeID: o.change.ID, state: o.change.State, changeType: o.change.Type,
		changeRunID: o.change.RunID, branch: o.change.Branch, baseSHA: o.change.BaseSHA,
		targetAdoptionID: o.change.TargetAdoptionID, targetCandidateJSON: o.change.TargetCandidateJSON,
		expectedRevision: o.change.ExpectedRevision, adoptedRevision: o.change.AdoptedRevision,
	}
}

func (s StandingRules) assessmentSource(ctx context.Context, runID ids.ID) (standingObservation, error) {
	var out standingObservation
	auth := commandAuthority{db: s.db, checked: s.deps.Authority, clocked: s.clocked}
	if _, err := requireActor(ctx, auth, operationPermission{Action: actionIdentify}); err != nil {
		return out, err
	}
	pid, err := runProject(ctx, s.db, runID)
	if err != nil {
		return out, err
	}
	if _, err = requireActor(ctx, auth, operationPermission{Action: actionReadRun, ProjectID: pid, RunID: runID}); err != nil {
		return out, err
	}
	source, err := ReadSourceIn(ctx, s.db, RunRef{ID: runID, ProjectID: pid})
	if err != nil {
		return out, err
	}
	resource, err := project.AuthorityResourceIn(ctx, s.db, pid)
	if err != nil {
		return out, err
	}
	resource.RunID, resource.ChangeID = runID, source.ChangeID
	if err = s.require(ctx, access.CapChangeRunChecks, resource); err != nil {
		return out, err
	}
	return s.observeAssessmentSource(ctx, runID)
}

func (s StandingRules) observeAssessmentSource(ctx context.Context, runID ids.ID) (standingObservation, error) {
	var out standingObservation
	run, err := readRun(ctx, s.db, runID)
	if err != nil {
		return out, err
	}
	pid := run.ProjectID
	source, err := ReadSourceIn(ctx, s.db, RunRef{ID: runID, ProjectID: pid})
	if err != nil {
		return out, err
	}
	if source.ChangeID.IsZero() || !source.RepoBacked() || source.RootRole == "landing" {
		return out, fault.New(fault.Conflict, "routine inspection needs an ordinary repository-backed change")
	}
	ch, err := readChangeInProject(ctx, s.db, source.ChangeID, pid)
	if err != nil {
		return out, err
	}
	if ch.State != ChangeInProgress {
		return out, fault.New(fault.Conflict, "this change is not an ordinary change awaiting inspection")
	}
	run, err = readRun(ctx, s.db, runID)
	if err != nil {
		return out, err
	}
	if run.Status != RunRunning {
		return out, fault.New(fault.Conflict, "the inspected run is no longer active")
	}
	holder, err := scm.ReadAuthorityIn(ctx, s.db, pid, source.Branch)
	if err != nil {
		return out, err
	}
	if s.deps.Presence == nil {
		return out, fault.New(fault.Unavailable, "writer observation is unavailable")
	}
	if holder.Holder != "run:"+source.RootRunID.String() {
		ended, e := HolderRecordsEndedIn(ctx, s.db, pid, holder.Holder, s.deps.Presence, s.now())
		if e != nil {
			return out, e
		}
		if !ended {
			return out, &ChangeHeld{Holder: holder.Holder}
		}
	}
	tree, err := workflowsql.New(s.db.SQL()).RunTreeActivity(ctx, workflowsql.RunTreeActivityParams{RootID: source.RootRunID, ProjectID: pid, RunRunning: RunRunning.String(), StepRunning: StatusRunning.String()})
	if err != nil {
		return out, err
	}
	if tree.Stepping {
		return out, fault.New(fault.Conflict, "wait for the change's active step before inspecting it")
	}
	unsettled, err := s.deps.Presence.UnsettledRuns(ctx, s.db)
	if err != nil {
		return out, err
	}
	for _, id := range tree.RunIds {
		if slices.Contains(unsettled, id) {
			return out, fault.New(fault.Conflict, "wait for the change's task settlement before inspecting it")
		}
	}
	clear, err := s.deps.Presence.FolderUnwritten(ctx, source.ChangeID)
	if err != nil {
		return out, err
	}
	if !clear {
		return out, fault.New(fault.Conflict, "a runtime can still write this change's source")
	}
	return standingObservation{source: source, run: *run, authority: holder, change: *ch}, nil
}

// Assess is classification only. It neither stores reusable assent nor accepts
// caller-provided evidence. A future effect owner must obtain fresh evidence.
func (s StandingRules) Assess(ctx context.Context, runID ids.ID) (StandingAssessment, error) {
	if _, err := s.assessmentSource(ctx, runID); err != nil {
		return StandingAssessment{}, err
	}
	rule, err := latestStanding(ctx, s.db)
	if err != nil {
		return StandingAssessment{}, err
	}
	if rule == nil {
		return StandingAssessment{}, fault.New(fault.Conflict, "save Janitor choices first")
	}
	return s.assess(ctx, rule.RuleID, runID)
}

func (s StandingRules) assess(ctx context.Context, ruleID, runID ids.ID) (StandingAssessment, error) {
	var out StandingAssessment
	first, err := s.observeAssessmentSource(ctx, runID)
	if err != nil {
		return out, err
	}
	rule, err := currentStanding(ctx, s.db, ruleID)
	if err != nil {
		return out, err
	}

	if _, err := s.eligibleForIn(ctx, s.db, rule, first.source.ProjectID, first.source.WorkflowName, first.run.AdoptionID); err != nil {
		return out, err
	}
	if s.deps.Inspector == nil {
		return out, fault.New(fault.Unavailable, "routine inspection is unavailable")
	}
	categories := rule.Choices
	categories.Dependencies, err = s.effectiveLevelIn(ctx, s.db, rule.Choices, first.source.ProjectID)
	if err != nil {
		return out, err
	}
	target, err := s.assessmentAdoption(ctx, first, categories)
	if err != nil {
		return out, err
	}
	if target == nil && categories.Dependencies == "off" {
		return out, fault.New(fault.Conflict, "dependency updates are turned off")
	}
	var inspection StandingInspection
	if target != nil {
		inspector, ok := s.deps.Inspector.(standingAdoptionInspector)
		if !ok {
			return out, fault.New(fault.Unavailable, "adoption source inspection is unavailable")
		}
		inspection, err = inspector.InspectRoutineWithAdoption(ctx, first.source, categories)
	} else {
		inspection, err = s.deps.Inspector.InspectRoutine(ctx, first.source, categories)
	}
	if err != nil {
		return out, err
	}
	last, err := s.observeAssessmentSource(ctx, runID)
	if err != nil {
		return out, err
	}
	current, err := currentStanding(ctx, s.db, ruleID)
	if err != nil {
		return out, err
	}
	if first.witness() != last.witness() {
		return out, fault.New(fault.Conflict, "the change's source or branch moved during inspection")
	}
	if current.Revision != rule.Revision {
		return out, fault.New(fault.Conflict, "the standing approval changed during inspection")
	}
	lastLevel, err := s.effectiveLevelIn(ctx, s.db, current.Choices, last.source.ProjectID)
	if err != nil {
		return out, err
	}
	if lastLevel != categories.Dependencies {
		return out, fault.New(fault.Conflict, "project update level changed during inspection")
	}
	lastTarget, err := s.assessmentAdoption(ctx, last, current.Choices)
	if err != nil {
		return out, err
	}
	if !reflect.DeepEqual(target, lastTarget) {
		return out, fault.New(fault.Conflict, "routine adoption consent changed during inspection")
	}
	if target != nil {
		switch inspection.Classification {
		case "routine-dependency":
			inspection.Classification += "-and-adoption"
		}
	}
	if _, err := s.eligibleForIn(ctx, s.db, current, last.source.ProjectID, last.source.WorkflowName, last.run.AdoptionID); err != nil {
		return out, err
	}
	return StandingAssessment{Level: categories.Dependencies, ProcessingAvailable: true, Adoption: target, RuleID: ruleID, RuleRevision: rule.Revision, Choices: rule.Choices, ProjectID: first.source.ProjectID, ChangeID: first.source.ChangeID, RunID: runID, Stage: first.run.CurrentStage, RetryRevision: first.run.RetryRevision, AdoptionID: first.run.AdoptionID, AdoptionRevision: first.source.AdoptionRevision, Inspection: inspection}, nil
}

func (s StandingRules) assessmentAdoption(ctx context.Context, observed standingObservation, categories JanitorChoices) (*adoption.RoutineTarget, error) {
	ch := observed.change
	if ch.Type != "uplift" {
		if !ch.TargetAdoptionID.IsZero() || ch.TargetCandidateJSON != "" && ch.TargetCandidateJSON != "{}" {
			return nil, fault.New(fault.Conflict, "unexpected adoption target on ordinary change")
		}
		return nil, nil
	}
	if !categories.Packs {
		return nil, fault.New(fault.Conflict, "ways-of-working updates are not permitted by this rule")
	}
	origin, err := workflowsql.New(s.db.SQL()).QueueAttempt(ctx, ch.ID)
	if err != nil {
		return nil, err
	}
	if len(origin) != 1 || origin[0].RuleID.IsZero() {
		return nil, fault.New(fault.Conflict, "standing pack adoption needs a Change opened from a finished what-if")
	}
	if ch.TargetAdoptionID.IsZero() || s.deps.Adoptions == nil {
		return nil, fault.New(fault.Unavailable, "the recorded adoption target cannot be checked")
	}
	target, err := database.Transact(ctx, s.db, func(tx *database.Tx) (adoption.RoutineTarget, error) {
		if _, err := adoption.CurrentForUpdateIn(ctx, tx, ch.ProjectID); err != nil {
			return adoption.RoutineTarget{}, err
		}
		return s.deps.Adoptions.CheckRoutineTargetIn(ctx, tx, ch.ProjectID, ch.ID, ch.TargetAdoptionID, ch.ExpectedRevision, []byte(ch.TargetCandidateJSON))
	})
	if err != nil {
		return nil, err
	}
	return &target, nil
}
