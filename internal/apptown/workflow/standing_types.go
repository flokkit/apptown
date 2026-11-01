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
	DeploymentID ids.ID `json:"deploymentId"`
	TargetID     ids.ID `json:"targetId"`
	Promotion    bool   `json:"promotion"`
}
type StandingProject struct {
	Workflow     string                `json:"workflow"`
	ProjectID    ids.ID                `json:"projectId"`
	Environments []StandingEnvironment `json:"environments"`
}
type StandingDefinition struct {
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
type StandingQuestion struct {
	Definition StandingDefinition       `json:"definition"`
	Projects   []StandingProjectMeaning `json:"projects"`
	Shown      string                   `json:"shown"`
	Digest     string                   `json:"digest"`
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
	invalid := func(why string) (StandingDefinition, error) { return in, fault.New(fault.Invalid, why) }
	if in.Delivery != "full" && in.Delivery != "land-only" {
		return invalid("select full delivery or accept and land only")
	}
	if len(in.Projects) == 0 || (!in.Administrative && len(in.Projects) != 1) {
		return invalid("select an exact project set; multiple projects require administrative management")
	}
	c := in.Categories
	if c.Dependencies != "" && c.Dependencies != "patch" && c.Dependencies != "minor" {
		return invalid("dependency updates must be patch or patch and minor")
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
	b.WriteString("Application source, migrations, major versions, new dependencies/capabilities/permissions/needs/secrets, weaker checks, unsupported changes and storage-engine transitions require a person. Missing, skipped, not-applicable, waived, failed or unknown evidence is not a pass. Current authority is required at each effect. Disabling stops future claims; an already claimed effect may finish. Automatic processing is not available yet.")
	q.Shown = b.String()
	raw, err := json.Marshal(q)
	if err != nil {
		return q, err
	}
	digest := sha256.Sum256(raw)
	q.Digest = hex.EncodeToString(digest[:])
	return q, nil
}
