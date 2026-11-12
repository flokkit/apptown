package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

const janitorConsent = "Every night AppTown scans each project's code and running images. It fixes security findings in project dependencies one package at a time, and each Monday applies patch and minor dependency updates. When tests, start-up and scans pass, it lands the change and releases it to the running app on every deployment that follows main and is set to automatic or a window, unless the deployment is protected. An agent writes any code a change needs; nothing lands unless every check, the tests and the start-up pass. It never changes toolchain versions or base images. This covers every project, including ones created later, until a project is turned off. Problems are shown in AppTown only."

func normalizeInstanceStanding(in StandingDefinition) (StandingDefinition, error) {
	bad := func(why string) (StandingDefinition, error) { return in, fault.New(fault.Invalid, why) }
	if len(in.Projects) != 0 || len(in.Schedules) == 0 {
		return bad("instance consent selects workflows and schedules, not projects")
	}
	if in.Delivery != "full" {
		return bad("instance consent includes releases under each deployment's setting")
	}
	if in.Categories.Dependencies == "" {
		in.Categories.Dependencies = "routine"
	}
	if in.Categories.Dependencies != "routine" && in.Categories.Dependencies != "aggressive" {
		return bad("select a dependency update bound")
	}
	if in.Categories.BaseImages || in.Categories.WaysOfWorking {
		return bad("select only declared dependency fixes for scheduled instance consent")
	}
	in.Administrative = true
	in.Schedules = slices.Clone(in.Schedules)
	slices.SortFunc(in.Schedules, func(a, b standingSchedule) int { return strings.Compare(a.Workflow, b.Workflow) })
	for i, schedule := range in.Schedules {
		if schedule.Workflow == "" || i > 0 && in.Schedules[i-1].Workflow == schedule.Workflow {
			return bad("select each workflow once")
		}
		if len(schedule.Crons) != 2 {
			return bad("select only routine and aggressive crons for each workflow")
		}
		for _, level := range []string{"routine", "aggressive"} {
			if _, err := schema.ParseCron(schedule.Crons[level]); err != nil {
				return bad("select routine and aggressive crons for each workflow")
			}
		}
	}
	return in, nil
}

func (s StandingRules) instanceQuestion(ctx context.Context, q database.Queryer, in StandingDefinition) (StandingQuestion, error) {
	shown := "Update level: " + in.Categories.Dependencies + ". " + janitorConsent
	if in.Categories.Dependencies == "aggressive" {
		shown = strings.Replace(shown, "each Monday applies patch and minor dependency updates", "every night applies dependency updates, including major versions", 1)
	}
	out := StandingQuestion{Definition: in, Shown: shown}
	procedures, ok := s.deps.Procedures.(standingInstanceProcedures)
	if !ok || s.deps.Portfolio == nil {
		return out, fault.New(fault.Unavailable, "instance standing approval observations are unavailable")
	}
	for _, schedule := range in.Schedules {
		proc, err := procedures.InstanceStandingProcedure(ctx, q, schedule.Workflow)
		if err != nil {
			return out, err
		}
		out.Projects = append(out.Projects, StandingProjectMeaning{Procedure: proc})
	}
	var err error
	out.Portfolio, err = s.deps.Portfolio.StandingPortfolio(ctx, q, ids.Nil, in.Categories)
	if err != nil {
		return out, err
	}
	images := map[string]bool{}
	for i := range out.Portfolio {
		project := &out.Portfolio[i]
		project.Level, err = s.effectiveLevelIn(ctx, q, in, project.ProjectID)
		if err != nil {
			return out, err
		}
		project.OverridesInstance = project.Level != in.Categories.Dependencies
		if project.BaseImages > 0 {
			out.BaseImageFindings.Projects++
		}
		for _, digest := range project.BaseImageFindings {
			images[digest] = true
		}
	}
	out.BaseImageFindings.Images = len(images)
	out.BaseImageFindings.Message = "Images with base-image findings the janitor cannot fix yet"
	// The dynamic preview is information, not the consent's project scope.
	raw, err := json.Marshal(struct {
		Definition StandingDefinition
		Purposes   []StandingProjectMeaning
		Shown      string
	}{in, out.Projects, out.Shown})
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(raw)
	out.Digest = hex.EncodeToString(sum[:])
	return out, nil
}

func (s StandingRules) eligibleForIn(ctx context.Context, q database.Queryer, rule StandingRevision, pid ids.ID, workflow string) (StandingProcedure, error) {
	var out StandingProcedure
	if s.deps.Identity == nil || s.deps.Procedures == nil {
		return out, access.ErrAuthorityUnavailable
	}
	refuse := func(why string) (StandingProcedure, error) { return out, fault.New(fault.Conflict, why) }
	if !rule.Enabled {
		return refuse("standing approval is disabled")
	}
	if _, err := normalizeStanding(rule.Question.Definition); err != nil {
		return refuse("the standing approval's bounds changed; preview and confirm its current meaning")
	}
	if err := identity.RequireEnabledIn(ctx, q, access.Principal{Kind: "user", ID: rule.Issuer.Principal}); err != nil {
		return out, err
	}
	if err := project.RefuseUnlessActive(ctx, q, pid); err != nil {
		return out, err
	}
	level, err := s.effectiveLevelIn(ctx, q, rule.Question.Definition, pid)
	if err != nil {
		return out, err
	}
	if level == "off" {
		return refuse("turned off for this project")
	}
	in := rule.Question.Definition
	if in.Scope != "instance" {
		fresh, err := s.question(ctx, q, in, rule.Issuer, false)
		if err != nil {
			return out, err
		}
		if fresh.Digest != rule.Question.Digest {
			return refuse("the governing meaning changed; a person must enable this approval again")
		}
	}
	selected := false
	if in.Scope == "instance" {
		for _, schedule := range in.Schedules {
			if schedule.Workflow == workflow {
				selected = true
			}
		}
	} else {
		for _, p := range in.Projects {
			if p.ProjectID == pid && p.Workflow == workflow {
				selected = true
			}
		}
	}
	if !selected {
		return refuse("the workflow is outside this standing approval")
	}
	independent, err := workflowsql.New(q.SQL()).StandingIndependentApproval(ctx, pid)
	if err != nil {
		return out, err
	}
	if independent {
		return refuse("independent approval requires a person")
	}
	proc, err := s.deps.Procedures.StandingProcedure(ctx, q, pid, workflow)
	if err != nil {
		return out, err
	}
	pinned := false
	for _, meaning := range rule.Question.Projects {
		if in.Scope != "instance" && meaning.ProjectID != pid {
			continue
		}
		a, b := meaning.Procedure.Acceptance, meaning.Procedure.Landing
		if a.Workflow == workflow && a.Step == proc.Acceptance.Step && a.Means == proc.Acceptance.Means && b.Workflow == proc.Landing.Workflow && b.Step == proc.Landing.Step && b.Means == proc.Landing.Means {
			pinned = true
		}
	}
	if !pinned {
		return refuse("the standing approval meaning changed; a person must enable it again")
	}
	resource, err := project.AuthorityResourceIn(ctx, q, pid)
	if err != nil {
		return out, err
	}
	for _, cap := range []access.Capability{access.CapChangeApprove, access.CapChangeLand} {
		if _, err := s.deps.Identity.StandingIssuerAllowsIn(ctx, q, rule.Issuer.Principal, access.Permission{Capability: cap, Resource: resource}); err != nil {
			return out, err
		}
	}
	for _, purpose := range []StandingPurpose{proc.Acceptance, proc.Landing} {
		if err := requireApprover(ctx, q, operationPermission{ProjectID: pid}, purpose.Approvers, rule.Issuer.Name, rule.Issuer.Principal, s.now()); err != nil {
			return out, err
		}
	}
	if s.deps.Portfolio == nil {
		return refuse("project coverage and release health are unavailable")
	}
	portfolio, err := s.deps.Portfolio.StandingPortfolio(ctx, q, pid, in.Categories)
	if err != nil {
		return out, err
	}
	if len(portfolio) != 1 || !portfolio[0].Covered {
		return refuse("this project needs a pack upgrade")
	}
	if in.Scope != "instance" {
		var selected []StandingEnvironment
		for _, p := range in.Projects {
			if p.ProjectID == pid {
				selected = p.Environments
			}
		}
		follows := portfolio[0].FollowsMain
		if in.Delivery == "land-only" && len(follows) > 0 {
			return refuse("accept and land only cannot release to a deployment following main")
		}
		if in.Delivery == "full" {
			if len(selected) != len(follows) {
				return refuse("the deployments following main changed; a person must confirm the exact release scope again")
			}
			for _, dep := range follows {
				if !slices.ContainsFunc(selected, func(e StandingEnvironment) bool {
					return e.DeploymentID == dep.DeploymentID && e.TargetID == dep.TargetID
				}) {
					return refuse("a deployment following main is outside the selected release scope")
				}
			}
		}
	}
	hold, err := s.deps.Portfolio.StandingHold(ctx, q, pid)
	if err != nil {
		return out, err
	}
	if hold != "" {
		return refuse("the project is held back: " + hold)
	}
	return proc, nil
}

// StandingForWorkflow is the scheduler's current eligibility read. The returned
// revision is attribution for this firing; it is never an acceptance vote.
func (s StandingRules) StandingForWorkflow(ctx context.Context, pid ids.ID, workflow string) (StandingRevision, string, error) {
	rows, err := workflowsql.New(s.db.SQL()).CurrentStandingRules(ctx)
	if err != nil {
		return StandingRevision{}, "", err
	}
	for _, row := range rows {
		rule, err := readStandingRevision(row)
		if err != nil {
			return StandingRevision{}, "", err
		}
		if _, err = s.eligibleForIn(ctx, s.db, rule, pid, workflow); err != nil {
			if standingRefusal(err) {
				continue
			}
			return rule, "", err
		}
		for _, schedule := range rule.Question.Definition.Schedules {
			if schedule.Workflow == workflow {
				level, err := s.effectiveLevelIn(ctx, s.db, rule.Question.Definition, pid)
				if err != nil {
					return rule, "", err
				}
				return rule, schedule.Crons[level], nil
			}
		}
	}
	return StandingRevision{}, "", nil
}

type standingProjectChoice struct {
	ProjectID ids.ID `json:"projectId"`
	Level     string `json:"level"`
	Reason    string `json:"reason"`
}

func (s StandingRules) SetProjectChoice(ctx context.Context, pid ids.ID, level string, reason string) error {
	if level != "off" && level != "routine" && level != "aggressive" {
		return fault.New(fault.Invalid, "select off, routine or aggressive")
	}
	in := standingProjectChoice{ProjectID: pid, Level: level, Reason: reason}
	actor, err := s.human(ctx, s.db)
	if err != nil {
		return err
	}
	if strings.TrimSpace(in.Reason) == "" {
		return fault.New(fault.Invalid, "give a reason for changing janitor updates")
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
	return fmt.Sprintf("the janitor, under standing approval %s (rev %d), granted by %s", v.RuleID, v.Revision, v.Issuer.Name)
}

type standingProjectAct struct {
	standingProjectChoice
	Person access.Actor `json:"person"`
	At     string       `json:"at"`
}

type standingProjectState struct {
	Enabled bool                `json:"enabled"`
	Level   string              `json:"level"`
	Choice  *standingProjectAct `json:"choice,omitempty"`
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
	if len(choices) > 0 {
		out.Choice = &standingProjectAct{}
		if err = json.Unmarshal(choices[0], out.Choice); err != nil {
			return out, err
		}
		out.Level = out.Choice.Level
	}
	rows, err := workflowsql.New(s.db.SQL()).CurrentStandingRules(ctx)
	if err != nil {
		return out, err
	}
	for _, row := range rows {
		rule, err := readStandingRevision(row)
		if err != nil {
			return out, err
		}
		if !rule.Enabled {
			continue
		}
		if rule.Question.Definition.Scope == "instance" {
			out.Enabled = true
			if out.Level == "" {
				out.Level = rule.Question.Definition.Categories.Dependencies
			}
		}
		for _, p := range rule.Question.Definition.Projects {
			if p.ProjectID == pid {
				out.Enabled = true
				if out.Level == "" {
					out.Level = rule.Question.Definition.Categories.Dependencies
				}
			}
		}
	}
	return out, nil
}

// effectiveLevelIn is shared by firing, fix execution and standing assessment.
// No project record means the instance's level applies.
func (s StandingRules) effectiveLevelIn(ctx context.Context, q database.Queryer, in StandingDefinition, pid ids.ID) (string, error) {
	rows, err := workflowsql.New(q.SQL()).LatestStandingProjectChoice(ctx, pid)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return in.Categories.Dependencies, nil
	}
	var act standingProjectAct
	if err := json.Unmarshal(rows[0], &act); err != nil {
		return "", err
	}
	switch act.Level {
	case "off", "routine", "aggressive":
		return act.Level, nil
	}
	return "", fault.New(fault.Conflict, "project janitor level is invalid")
}
