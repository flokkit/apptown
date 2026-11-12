package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// StandingCategories is consent to bounded classes, not a classification result.
type StandingCategories struct {
	Dependencies  string `json:"dependencies"`
	BaseImages    bool   `json:"baseImages"`
	SecurityFixes bool   `json:"securityFixes"`
	WaysOfWorking bool   `json:"waysOfWorking"`
}

// StandingEnvironment preserves exactly which environment and release purposes
// a person selected. Names and mutable project defaults cannot substitute.
type StandingEnvironment struct {
	// These labels describe a preview; authority always uses the exact IDs.
	ProjectID    ids.ID `json:"projectId,omitzero"`
	Project      string `json:"project,omitempty"`
	Deployment   string `json:"deployment,omitempty"`
	Setting      string `json:"setting,omitempty"`
	DeploymentID ids.ID `json:"deploymentId"`
	TargetID     ids.ID `json:"targetId"`
	Promotion    bool   `json:"promotion"`
}
type StandingProject struct {
	Workflow     string                `json:"workflow"`
	ProjectID    ids.ID                `json:"projectId"`
	Environments []StandingEnvironment `json:"environments"`
}
type standingSchedule struct {
	Workflow string            `json:"workflow"`
	Crons    map[string]string `json:"crons"`
}

type StandingDefinition struct {
	Scope          string             `json:"scope,omitempty"`
	Schedules      []standingSchedule `json:"schedules,omitempty"`
	Administrative bool               `json:"administrative"`
	Projects       []StandingProject  `json:"projects"`
	Categories     StandingCategories `json:"categories"`
	Delivery       string             `json:"delivery"`
}

// StandingPurpose is the currently selected declaration, never a vote.
type StandingPurpose struct {
	Workflow  string   `json:"workflow"`
	Version   int64    `json:"version"`
	Step      string   `json:"step"`
	Means     string   `json:"means"`
	Approvers []string `json:"approvers"`
}
type StandingProcedure struct {
	Acceptance StandingPurpose `json:"acceptance"`
	Landing    StandingPurpose `json:"landing"`
}

// standingProcedures resolves declarations and current law under the caller's
// project lock. The workflow owner separately checks approval eligibility.
type standingProcedures interface {
	StandingProcedure(context.Context, database.Queryer, ids.ID, string) (StandingProcedure, error)
}
type StandingDestination struct {
	Resource          access.Resource
	PromotionResource access.Resource
	PromotionRequired bool
	Name              string
}

// standingDestinations reads an exact deployment/target pair. Its owner retains
// lifecycle and resource construction; no provider effect belongs in this read.
type standingDestinations interface {
	StandingDestination(context.Context, database.Queryer, ids.ID, StandingEnvironment) (StandingDestination, error)
}
type StandingProjectMeaning struct {
	ProjectID ids.ID            `json:"projectId"`
	Procedure StandingProcedure `json:"procedure"`
}

type StandingProjectPreview struct {
	Level             string                `json:"level"`
	OverridesInstance bool                  `json:"overridesInstance"`
	NoMajorCommand    []string              `json:"noMajorCommand"`
	FollowsMain       []StandingEnvironment `json:"followsMain"`
	Group             string                `json:"group"`
	Problem           string                `json:"problem,omitempty"`
	PackOffers        []string              `json:"packOffers"`
	BaseImageFindings []string              `json:"baseImageFindings"`
	ProjectID         ids.ID                `json:"projectId"`
	Project           string                `json:"project"`
	Covered           bool                  `json:"covered"`
	NeedsPackUpgrade  []string              `json:"needsPackUpgrade"`
	OnlyChecksStarts  bool                  `json:"onlyChecksStarts"`
	Deployments       []StandingEnvironment `json:"deployments"`
	BaseImages        int                   `json:"baseImages"`
}

type standingBaseImagePreview struct {
	Images   int    `json:"images"`
	Projects int    `json:"projects"`
	Message  string `json:"message"`
}

type StandingQuestion struct {
	BaseImageFindings standingBaseImagePreview `json:"baseImageFindings"`
	Portfolio         []StandingProjectPreview `json:"portfolio,omitempty"`
	Definition        StandingDefinition       `json:"definition"`
	Projects          []StandingProjectMeaning `json:"projects"`
	Shown             string                   `json:"shown"`
	Digest            string                   `json:"digest"`
}
type StandingCommand struct {
	RuleID           ids.ID             `json:"ruleId"`
	ExpectedRevision int64              `json:"expectedRevision"`
	Definition       StandingDefinition `json:"definition"`
	ShownDigest      string             `json:"shownDigest"`
}
type StandingRevision struct {
	RuleID    ids.ID           `json:"ruleId"`
	Revision  int64            `json:"revision"`
	Enabled   bool             `json:"enabled"`
	Question  StandingQuestion `json:"question"`
	Issuer    access.Actor     `json:"issuer"`
	EnabledAt time.Time        `json:"enabledAt"`
	ChangedBy access.Actor     `json:"changedBy"`
	ChangedAt time.Time        `json:"changedAt"`
	Reason    string           `json:"reason"`
}

func normalizeStanding(in StandingDefinition) (StandingDefinition, error) {
	if in.Scope == "instance" {
		return normalizeInstanceStanding(in)
	}
	if in.Scope != "" {
		return in, fault.New(fault.Invalid, "standing scope must be instance or an exact project selection")
	}
	invalid := func(why string) (StandingDefinition, error) { return in, fault.New(fault.Invalid, why) }
	if in.Delivery != "full" && in.Delivery != "land-only" {
		return invalid("select full delivery or accept and land only")
	}
	if len(in.Projects) == 0 || (!in.Administrative && len(in.Projects) != 1) {
		return invalid("select an exact project set; multiple projects require administrative management")
	}
	c := in.Categories
	if c.WaysOfWorking && (c.Dependencies != "" || c.BaseImages || c.SecurityFixes) {
		return invalid("keep packs current is its own category; enable it separately from dependency and image fixes")
	}
	if c.Dependencies != "" && c.Dependencies != "routine" && c.Dependencies != "aggressive" {
		return invalid("dependency update level must be routine or aggressive")
	}
	if c.Dependencies == "" && !c.BaseImages && !c.WaysOfWorking {
		return invalid("select at least one bounded update category")
	}
	if c.SecurityFixes && c.Dependencies == "" && !c.BaseImages {
		return invalid("security fixes require a permitted dependency or base-image class")
	}
	in.Projects = slices.Clone(in.Projects)
	slices.SortFunc(in.Projects, func(a, b StandingProject) int { return strings.Compare(a.ProjectID.String(), b.ProjectID.String()) })
	for i := range in.Projects {
		p := &in.Projects[i]
		p.Workflow = strings.TrimSpace(p.Workflow)
		if p.Workflow == "" {
			return invalid("select the working workflow explicitly for every project")
		}
		if p.ProjectID.IsZero() || (i > 0 && p.ProjectID == in.Projects[i-1].ProjectID) {
			return invalid("project identities must be nonempty and unique")
		}
		if in.Delivery == "full" && len(p.Environments) == 0 {
			return invalid("full delivery requires an explicitly selected environment for every project")
		}
		if in.Delivery == "land-only" && len(p.Environments) != 0 {
			return invalid("accept and land only cannot include deployment consent")
		}
		p.Environments = slices.Clone(p.Environments)
		for j := range p.Environments {
			p.Environments[j].ProjectID = ids.Nil
			p.Environments[j].Project = ""
			p.Environments[j].Deployment = ""
			p.Environments[j].Setting = ""
		}
		slices.SortFunc(p.Environments, func(a, b StandingEnvironment) int {
			return strings.Compare(a.DeploymentID.String(), b.DeploymentID.String())
		})
		for j, e := range p.Environments {
			if e.DeploymentID.IsZero() || e.TargetID.IsZero() || (j > 0 && e.DeploymentID == p.Environments[j-1].DeploymentID) {
				return invalid("environment and target identities must be nonempty; environments must be unique")
			}
		}
	}
	return in, nil
}

func standingQuestion(in StandingDefinition, projects []StandingProjectMeaning) (StandingQuestion, error) {
	q := StandingQuestion{Definition: in, Projects: projects}
	var b strings.Builder
	b.WriteString("I authorize the Janitor to accept and land only classified routine updates within these bounds, after every required check passes outright. This is standing consent, not a vote on work I have personally read.\n")
	fmt.Fprintf(&b, "Dependency updates: %s; same-version base images: %t; security fixes within those bounds: %t; governed routine ways-of-working transitions: %t.\n", in.Categories.Dependencies, in.Categories.BaseImages, in.Categories.SecurityFixes, in.Categories.WaysOfWorking)
	for _, p := range projects {
		fmt.Fprintf(&b, "Project %s: acceptance %s v%d / %s: %s; landing %s v%d / %s: %s.\n", p.ProjectID, p.Procedure.Acceptance.Workflow, p.Procedure.Acceptance.Version, p.Procedure.Acceptance.Step, p.Procedure.Acceptance.Means, p.Procedure.Landing.Workflow, p.Procedure.Landing.Version, p.Procedure.Landing.Step, p.Procedure.Landing.Means)
	}
	if in.Delivery == "land-only" {
		b.WriteString("Accept and land only. Running software remains unchanged.\n")
	} else {
		b.WriteString("Full delivery is unfinished until the exact qualified update is deployed and healthy in every selected environment.\n")
		for _, p := range in.Projects {
			for _, e := range p.Environments {
				fmt.Fprintf(&b, "Project %s: deploy to environment %s on target %s; promotion authorized: %t.\n", p.ProjectID, e.DeploymentID, e.TargetID, e.Promotion)
			}
		}
	}
	b.WriteString("Mixed supported updates require every component to belong to an enabled category and pass its complete independent check. Any unsupported component requires a person. ")
	b.WriteString("An agent writes any application code the declared fix needs; every required check must be met. New packages exceed dependency-update consent. The selected level bounds version changes. A check AppTown judges not applicable with a reason is met. Missing, skipped, waived, failed or unknown evidence is not met. Current authority is required at each effect. Disabling stops future claims; an already claimed effect may finish. Automatic processing checks the current rule, project choice and release health at every effect.")
	q.Shown = b.String()
	raw, err := json.Marshal(q)
	if err != nil {
		return q, err
	}
	digest := sha256.Sum256(raw)
	q.Digest = hex.EncodeToString(digest[:])
	return q, nil
}
