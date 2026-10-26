package workflow

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// standingInspector observes exact source and independently validates supported
// dependency inputs. It cannot accept, publish, land or deploy the result.
type standingInspector interface {
	InspectRoutine(context.Context, Source, StandingCategories) (StandingInspection, error)
}

// standingPresence adds observation only; the existing FolderClear operation
// can stop containers and must never be called by an inspection.
type standingPresence interface {
	Presence
	FolderUnwritten(context.Context, ids.ID) (bool, error)
}
type StandingValidation struct {
	Image             string `json:"image"`
	Node              string `json:"node"`
	NPM               string `json:"npm"`
	Platform          string `json:"platform"`
	ReportDigest      string `json:"reportDigest"`
	ScratchSettlement string `json:"scratchSettlement"`
}
type StandingImageObservation struct {
	Reference         string `json:"reference"`
	Manifest          string `json:"manifest"`
	ConfigDigest      string `json:"configDigest"`
	PreparedImage     string `json:"preparedImage"`
	Node              string `json:"node"`
	Platform          string `json:"platform"`
	ReportDigest      string `json:"reportDigest"`
	ScratchSettlement string `json:"scratchSettlement"`
}
type StandingAdvisoryFix struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	Modified   string `json:"modified"`
	BodyDigest string `json:"bodyDigest"`
	Package    string `json:"package"`
	Before     string `json:"before"`
	After      string `json:"after"`
}
type StandingAdvisoryObservation struct {
	Status string                `json:"status"`
	Reason string                `json:"reason"`
	Fixes  []StandingAdvisoryFix `json:"fixes"`
}
type StandingInspection struct {
	Advisories *StandingAdvisoryObservation `json:"advisories,omitempty"`

	Images []StandingImageObservation `json:"images,omitempty"`

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
	RuleID              ids.ID             `json:"ruleId"`
	RuleRevision        int64              `json:"ruleRevision"`
	QuestionDigest      string             `json:"questionDigest"`
	ProjectID           ids.ID             `json:"projectId"`
	ChangeID            ids.ID             `json:"changeId"`
	RunID               ids.ID             `json:"runId"`
	Stage               string             `json:"stage"`
	RetryRevision       int64              `json:"retryRevision"`
	AdoptionID          ids.ID             `json:"adoptionId"`
	AdoptionRevision    int64              `json:"adoptionRevision"`
	Inspection          StandingInspection `json:"inspection"`
	ProcessingAvailable bool               `json:"processingAvailable"`
}

type standingObservation struct {
	source    Source
	run       Run
	authority scm.Authority
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
	if source.ChangeID.IsZero() || !source.RepoBacked() || source.RootRole == "landing" {
		return out, fault.New(fault.Conflict, "routine inspection needs an ordinary repository-backed change")
	}
	ch, err := readChangeInProject(ctx, s.db, source.ChangeID, pid)
	if err != nil {
		return out, err
	}
	if ch.State != ChangeInProgress || ch.Type == "uplift" {
		return out, fault.New(fault.Conflict, "this change is not an ordinary change awaiting inspection")
	}
	run, err := readRun(ctx, s.db, runID)
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
	return standingObservation{source: source, run: *run, authority: holder}, nil
}

// Assess is classification only. It neither stores reusable assent nor accepts
// caller-provided evidence. A future effect owner must obtain fresh evidence.
func (s StandingRules) Assess(ctx context.Context, ruleID, runID ids.ID) (StandingAssessment, error) {
	var out StandingAssessment
	first, err := s.assessmentSource(ctx, runID)
	if err != nil {
		return out, err
	}
	rule, err := currentStanding(ctx, s.db, ruleID)
	if err != nil {
		return out, err
	}
	if err = s.readable(ctx, s.db, rule.Question.Definition); err != nil {
		return out, err
	}
	selected := false
	for _, p := range rule.Question.Definition.Projects {
		if p.ProjectID == first.source.ProjectID && p.Workflow == first.source.WorkflowName {
			selected = true
		}
	}
	if !selected {
		return out, fault.New(fault.Conflict, "the run is outside the rule's selected projects and workflow")
	}
	status, err := s.status(ctx, rule)
	if err != nil {
		return out, err
	}
	if !status.Eligible {
		return out, fault.New(fault.Conflict, status.Reason)
	}
	if s.deps.Inspector == nil {
		return out, fault.New(fault.Unavailable, "routine inspection is unavailable")
	}
	inspection, err := s.deps.Inspector.InspectRoutine(ctx, first.source, rule.Question.Definition.Categories)
	if err != nil {
		return out, err
	}
	last, err := s.assessmentSource(ctx, runID)
	if err != nil {
		return out, err
	}
	current, err := currentStanding(ctx, s.db, ruleID)
	if err != nil {
		return out, err
	}
	if !reflect.DeepEqual(first, last) || current.Revision != rule.Revision {
		return out, fault.New(fault.Conflict, "the rule or change moved during inspection; inspect it again")
	}
	status, err = s.status(ctx, current)
	if err != nil {
		return out, err
	}
	if !status.Eligible {
		return out, fault.New(fault.Conflict, fmt.Sprintf("standing eligibility changed during inspection: %s", status.Reason))
	}
	return StandingAssessment{RuleID: ruleID, RuleRevision: rule.Revision, QuestionDigest: rule.Question.Digest, ProjectID: first.source.ProjectID, ChangeID: first.source.ChangeID, RunID: runID, Stage: first.run.CurrentStage, RetryRevision: first.run.RetryRevision, AdoptionID: first.run.AdoptionID, AdoptionRevision: first.source.AdoptionRevision, Inspection: inspection}, nil
}
