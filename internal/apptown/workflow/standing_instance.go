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

func normalizeInstanceStanding(in StandingDefinition) (StandingDefinition, error) {
	bad := func(why string) (StandingDefinition, error) { return in, fault.New(fault.Invalid, why) }
	if len(in.Projects) != 0 {
		return bad("instance consent selects categories, not projects")
	}
	if in.Delivery != "full" {
		return bad("instance consent includes releases under each deployment's setting")
	}
	if in.Categories.Dependencies == "" && !in.Categories.WaysOfWorking {
		in.Categories.Dependencies = "routine"
	}
	if in.Categories.Dependencies != "" && in.Categories.Dependencies != "routine" && in.Categories.Dependencies != "aggressive" {
		return bad("select a dependency update bound")
	}
	if in.Categories.BaseImages || in.Categories.WaysOfWorking && (in.Categories.Dependencies != "" || in.Categories.SecurityFixes) {
		return bad("keep packs current is its own category; enable it separately from dependency fixes")
	}
	in.Administrative = true
	return in, nil
}

func (s StandingRules) instanceQuestion(ctx context.Context, q database.Queryer, in StandingDefinition) (StandingQuestion, error) {
	out := StandingQuestion{Definition: in}
	procedures, ok := s.deps.Procedures.(standingInstanceProcedures)
	if !ok || s.deps.Portfolio == nil || s.deps.Publications == nil {
		return out, fault.New(fault.Unavailable, "instance standing approval observations are unavailable")
	}
	library, err := s.deps.Publications.PublishedWorkflows(ctx, q)
	if err != nil {
		return out, err
	}
	var covered, docs []*schema.Doc
	for _, w := range library {
		docs = append(docs, w.Doc)
	}
	for _, doc := range docs {
		trigger, ok := doc.Standing()
		if !ok || trigger.Publication != in.Categories.WaysOfWorking {
			continue
		}
		proc, err := procedures.InstanceStandingProcedure(doc.Metadata.Name, library)
		if err != nil {
			return out, err
		}
		out.Projects = append(out.Projects, standingProjectMeaning{Procedure: proc})
		covered = append(covered, doc)
	}
	out.Shown = standingConsent(in.Categories, covered, docs)
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
		Purposes   []standingProjectMeaning
		Shown      string
	}{in, out.Projects, out.Shown})
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(raw)
	out.Digest = hex.EncodeToString(sum[:])
	return out, nil
}

func (s StandingRules) eligibleForIn(ctx context.Context, q database.Queryer, rule StandingRevision, pid ids.ID, workflow string, target ...ids.ID) (StandingProcedure, error) {
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
		selected = pinsWorkflow(rule, workflow)
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
	var proc StandingProcedure
	if in.Categories.WaysOfWorking && in.Scope == "instance" {
		if len(target) > 0 {
			reader, ok := s.deps.Procedures.(standingTargetProcedures)
			if !ok {
				return refuse("target procedure observation is unavailable")
			}
			proc, err = reader.StandingTargetProcedure(ctx, q, pid, target[0], workflow)
		} else {
			reader, ok := s.deps.Procedures.(standingInstanceProcedures)
			if !ok || s.deps.Publications == nil {
				return refuse("published procedure observation is unavailable")
			}
			library, readErr := s.deps.Publications.PublishedWorkflows(ctx, q)
			if readErr != nil {
				return out, readErr
			}
			proc, err = reader.InstanceStandingProcedure(workflow, library)
		}
	} else {
		proc, err = s.deps.Procedures.StandingProcedure(ctx, q, pid, workflow)
	}
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
	if len(portfolio) == 0 {
		return refuse("this project has no repository for the janitor to update")
	}
	if len(portfolio) != 1 {
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
// The level belongs to the eligible rule. A refusal describes the first
// covered rule when none is eligible; errors mean eligibility is unknown.
func (s StandingRules) StandingForWorkflow(ctx context.Context, pid ids.ID, workflow string) (StandingRevision, string, string, error) {
	return s.standingForWorkflow(ctx, pid, workflow, ids.Nil)
}

func pinsWorkflow(rule StandingRevision, name string) bool {
	return slices.ContainsFunc(rule.Question.Projects, func(p standingProjectMeaning) bool { return p.Procedure.Acceptance.Workflow == name })
}

// Execution uses its frozen adoption when matching approval meanings.
func (s StandingRules) standingForWorkflow(ctx context.Context, pid ids.ID, workflow string, target ids.ID) (StandingRevision, string, string, error) {
	var targets []ids.ID
	if !target.IsZero() {
		targets = []ids.ID{target}
	}
	rows, err := workflowsql.New(s.db.SQL()).CurrentStandingRules(ctx)
	if err != nil {
		return StandingRevision{}, "", "", err
	}
	refused := "waits for a standing approval"
	covered := false
	for _, row := range rows {
		rule, err := readStandingRevision(row)
		if err != nil {
			return StandingRevision{}, "", "", err
		}
		if !pinsWorkflow(rule, workflow) {
			continue
		}
		if _, err = s.eligibleForIn(ctx, s.db, rule, pid, workflow, targets...); err != nil {
			if !standingRefusal(err) {
				return StandingRevision{}, "", "", err
			}
			if !covered {
				refused = err.Error()
				covered = true
			}
			continue
		}
		level, err := s.effectiveLevelIn(ctx, s.db, rule.Question.Definition, pid)
		return rule, level, "", err
	}
	return StandingRevision{}, "", refused, nil
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

type standingTargetProcedures interface {
	StandingTargetProcedure(context.Context, database.Queryer, ids.ID, ids.ID, string) (StandingProcedure, error)
}
