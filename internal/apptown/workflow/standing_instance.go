package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

func (s StandingRules) eligibleForIn(ctx context.Context, q database.Queryer, rule StandingRevision, pid ids.ID, workflow string, target ...ids.ID) (StandingProcedure, error) {
	set := []ProjectWorkflows{{ProjectID: pid, Workflows: []string{workflow}}}
	if len(target) > 0 {
		set[0].target = target[0]
	}
	facts := s.standingFacts(ctx, q, rule, set)
	return s.judgeStanding(ctx, q, rule, pid, workflow, facts[0], facts[0].issuer)
}

func (s StandingRules) judgeStanding(ctx context.Context, q database.Queryer, rule StandingRevision, pid ids.ID, workflow string, facts standingProjectFacts, issuer standingIssuerFacts) (StandingProcedure, error) {
	var out StandingProcedure
	if s.deps.Identity == nil || s.deps.Procedures == nil {
		return out, access.ErrAuthorityUnavailable
	}
	refuse := func(why string) (StandingProcedure, error) { return out, fault.New(fault.Conflict, why) }
	if !rule.Enabled {
		return refuse("standing approval is disabled")
	}
	if err := issuer.err; err != nil {
		var denied *access.Denied
		if errors.Is(err, access.ErrPrincipalRequired) || errors.As(err, &denied) && denied.Code == access.DenyAccountDisabled {
			// Eligibility's account preflight supplies only the durable principal.
			return out, &access.Denied{Code: access.DenyAccountDisabled, Reason: "'s account is disabled or does not exist"}
		}
		return out, err
	}
	if err := facts.activeErr; err != nil {
		return out, err
	}
	level, err := facts.level(rule.Choices)
	if err != nil {
		return out, err
	}
	if facts.choice == "off" {
		return refuse("turned off for this project")
	}
	if facts.independentErr != nil {
		return out, facts.independentErr
	}
	if facts.independent {
		return refuse("independent approval requires a person")
	}
	selectedProcedure := facts.procedures[workflow]
	proc, err := selectedProcedure.procedure, selectedProcedure.err
	if err != nil {
		return out, err
	}
	switch proc.Trigger {
	case "publication":
		if !rule.Choices.Packs {
			return refuse("pack updates are turned off")
		}
	case "schedule":
		if level == "off" {
			return refuse("dependency updates are turned off")
		}
	default:
		return refuse("the workflow is outside the Janitor choices")
	}
	if facts.resourceErr != nil {
		return out, facts.resourceErr
	}
	for _, err := range issuer.allowed {
		if err != nil {
			return out, err
		}
	}
	if facts.approverErr != nil {
		return out, facts.approverErr
	}
	_, root := access.RootGrant(issuer.facts.Grants)
	for _, purpose := range []StandingPurpose{proc.Acceptance, proc.Landing} {
		if err := judgeApprover(operationPermission{ProjectID: pid}, purpose.Approvers, rule.Issuer.Name, rule.Issuer.Principal, root, facts.approver, issuer.facts.Groups, issuer.facts.Grants); err != nil {
			return out, err
		}
	}
	if s.deps.Portfolio == nil {
		return refuse("project coverage and release health are unavailable")
	}
	reach, err := facts.reach, facts.reachErr
	if err != nil {
		return out, err
	}
	if !reach.Repository {
		return refuse("this project has no repository for the janitor to update")
	}
	hold, err := facts.hold, facts.holdErr
	if err != nil {
		return out, err
	}
	if hold != "" {
		return refuse("the project is held back: " + hold)
	}
	return proc, nil
}

// ProjectWorkflows names the workflows to judge independently for one project.
type ProjectWorkflows struct {
	ProjectID ids.ID
	Workflows []string
	// target is an execution's frozen adoption, or zero for current scheduling.
	target ids.ID
}

type StandingEligibilityResult struct {
	Rule           StandingRevision
	Level, Refused string
	Err            error
	Unassessed     []ids.ID
}

// StandingEligibility judges the one current record against each resolved procedure.
func (s StandingRules) StandingEligibility(ctx context.Context, set []ProjectWorkflows) (map[ids.ID]map[string]StandingEligibilityResult, error) {
	current, err := latestStanding(ctx, s.db)
	if err != nil {
		return nil, err
	}
	out := map[ids.ID]map[string]StandingEligibilityResult{}
	var facts []standingProjectFacts
	if current != nil {
		facts = s.standingFacts(ctx, s.db, *current, set)
	}
	for i, p := range set {
		results := map[string]StandingEligibilityResult{}
		out[p.ProjectID] = results
		for _, name := range p.Workflows {
			r := StandingEligibilityResult{Refused: "waits for Janitor choices"}
			if current != nil {
				_, err := s.judgeStanding(ctx, s.db, *current, p.ProjectID, name, facts[i], facts[i].issuer)
				if err == nil {
					r.Rule = *current
					r.Level, r.Err = facts[i].level(current.Choices)
					r.Refused = ""
				} else if standingRefusal(err) {
					r.Refused = err.Error()
				} else {
					r.Err = err
					r.Unassessed = []ids.ID{current.RuleID}
				}
			}
			results[name] = r
		}
	}
	return out, nil
}

// Execution and singleton scheduling use the same set judgment.
func (s StandingRules) standingForWorkflow(ctx context.Context, pid ids.ID, name string, target ids.ID) (StandingRevision, string, string, error) {
	results, err := s.StandingEligibility(ctx, []ProjectWorkflows{{ProjectID: pid, Workflows: []string{name}, target: target}})
	if err != nil {
		return StandingRevision{}, "", "", err
	}
	r := results[pid][name]
	return r.Rule, r.Level, r.Refused, r.Err
}

type standingProjectChoice struct {
	ProjectID ids.ID `json:"projectId"`
	Level     string `json:"level"`
}

func (s StandingRules) SetProjectChoice(ctx context.Context, pid ids.ID, level string) error {
	if level != "instance" && level != "off" && level != "routine" && level != "aggressive" {
		return fault.New(fault.Invalid, "select instance, off, routine or aggressive")
	}
	in := standingProjectChoice{ProjectID: pid, Level: level}
	actor, err := s.human(ctx, s.db)
	if err != nil {
		return err
	}
	resource, err := project.AuthorityResourceIn(ctx, s.db, in.ProjectID)
	if err != nil {
		return err
	}
	if err = s.require(ctx, access.CapProjectStandingApproval, resource); err != nil {
		return err
	}
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		if _, err := workflowsql.New(tx.SQL()).ProjectForUpdate(ctx, in.ProjectID); err != nil {
			return err
		}
		raw, err := json.Marshal(standingProjectAct{standingProjectChoice: in, Person: actor, At: s.now().Format("2006-01-02T15:04:05Z07:00")})
		if err != nil {
			return err
		}
		return workflowsql.New(tx.SQL()).RecordStandingProjectChoice(ctx, workflowsql.RecordStandingProjectChoiceParams{ID: ids.New(s.now()), ProjectID: in.ProjectID, Level: in.Level, RecordJson: raw, At: s.now()})
	})
}

func standingAttribution(v *standingVote) string {
	return fmt.Sprintf("the janitor, under the janitor choices (rev %d), set by %s", v.Revision, v.Issuer.Name)
}

type standingProjectAct struct {
	standingProjectChoice
	Person access.Actor `json:"person"`
	At     string       `json:"at"`
}

type standingProjectState struct {
	InstanceLevel string              `json:"instanceLevel"`
	Enabled       bool                `json:"enabled"`
	Level         string              `json:"level"`
	Choice        *standingProjectAct `json:"choice,omitempty"`
}

func (s StandingRules) ProjectState(ctx context.Context, pid ids.ID) (standingProjectState, error) {
	out := standingProjectState{}
	resource, err := project.AuthorityResourceIn(ctx, s.db, pid)
	if err != nil {
		return out, err
	}
	if err = s.require(ctx, access.CapProjectRead, resource); err != nil {
		return out, err
	}
	choices, err := workflowsql.New(s.db.SQL()).LatestStandingProjectChoice(ctx, pid)
	if err != nil {
		return out, err
	}
	out.Choice, err = projectChoiceFrom(firstProjectChoice(choices))
	if err != nil {
		return out, err
	}
	rule, err := latestStanding(ctx, s.db)
	if err != nil {
		return out, err
	}
	var in JanitorChoices
	if rule != nil {
		out.Enabled = rule.Enabled
		in = rule.Choices
	}
	out.InstanceLevel = in.Dependencies
	out.Level = projectLevel(in, choiceLevel(out.Choice))

	return out, nil
}

// effectiveLevelIn is shared by firing, fix execution and standing assessment.
// No project record means the instance's level applies.
func (s StandingRules) effectiveLevelIn(ctx context.Context, q database.Queryer, in JanitorChoices, pid ids.ID) (string, error) {
	rows, err := workflowsql.New(q.SQL()).LatestStandingProjectChoice(ctx, pid)
	if err != nil {
		return "", err
	}
	choice, err := projectChoiceFrom(firstProjectChoice(rows))
	if err != nil {
		return "", err
	}
	return projectLevel(in, choiceLevel(choice)), nil
}

func firstProjectChoice(rows []json.RawMessage) []byte {
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

func projectChoiceFrom(raw []byte) (*standingProjectAct, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var act standingProjectAct
	if err := json.Unmarshal(raw, &act); err != nil {
		return nil, err
	}
	switch act.Level {
	case "instance":
		return nil, nil
	case "off", "routine", "aggressive":
		return &act, nil
	default:
		return nil, fault.New(fault.Conflict, "project janitor level is invalid")
	}
}

func choiceLevel(choice *standingProjectAct) string {
	if choice == nil {
		return ""
	}
	return choice.Level
}

func projectLevel(instance JanitorChoices, choice string) string {
	if choice == "" || instance.Dependencies == "off" {
		return instance.Dependencies
	}
	return choice
}

type standingTargetProcedures interface {
	StandingTargetProcedure(context.Context, database.Queryer, ids.ID, ids.ID, string) (StandingProcedure, error)
}
