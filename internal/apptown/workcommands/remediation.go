package workcommands

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/remediation"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec"
)

// RemediationStarts composes a lane, frozen admission and its exact acceptance.
// Remediation supplies its observed scope; it retains attempt admission/state.
type RemediationStarts struct {
	DB          *database.DB
	Work        *workflow.Service
	Preparation *Preparation
	Adoption    *adoption.Service
	Lanes       remediation.WorkflowResolver
	Checks      *conformance.Service
	DataDir     string
}

func remediationCaller(ctx context.Context, projectID ids.ID) context.Context {
	principal := access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "remediation", ProjectID: projectID}
	return access.WithCaller(ctx, access.Caller{Principal: principal, Verified: true, Receipt: principal.Name})
}

// --- the resolver -------------------------------------------------------------------

func (a RemediationStarts) Resolve(ctx context.Context, item remediation.Item, scope remediation.VerificationScope) (remediation.StartDefinition, error) {
	var def remediation.StartDefinition
	record, projectErr := project.ByIDIn(ctx, a.DB, item.ProjectID)
	if errors.Is(projectErr, sql.ErrNoRows) {
		def.Skip = "project no longer exists"
		return def, nil
	}
	if projectErr != nil {
		return def, projectErr
	}
	p := &record
	lane := remediation.WorkflowFor(ctx, a.Lanes, p)
	if lane == "" {
		def.Skip = "this project has no quick-change or implement lane to work the fix on"
		return def, nil
	}
	req := workflow.StartRequest{ProjectID: p.ID, WorkflowName: lane}
	source, err := a.Work.StartSource(remediationCaller(ctx, p.ID), req)
	if err != nil {
		return def, err
	}
	def.Run, err = a.Preparation.Resolve(ctx, req, source)
	if err != nil {
		return def, err
	}
	// Acceptance and workflow must describe the same adopted revision.
	// Read failures are errors, not evidence that the check was removed.
	var checks []conformance.DeclaredCheck
	if !def.Run.AdoptionID.IsZero() {
		ad, readErr := a.Adoption.ByID(ctx, def.Run.AdoptionID)
		if readErr == nil && ad.ProjectID != p.ID {
			readErr = fmt.Errorf("adoption %d is not project %d's", def.Run.AdoptionID, p.ID)
		}
		if readErr != nil {
			return def, readErr
		}
		checks, err = verification.ChecksOfReceipt(ctx, a.Adoption, ad)
	} else {
		checks, err = a.Checks.AdoptedChecks(conformance.WithVerification(ctx, scope), p.ID)
	}
	if err != nil {
		return def, err
	}
	var sc conformance.DeclaredCheck
	for _, check := range checks {
		if check.Block == item.Block && check.Check.Name == item.Check {
			sc = check
			break
		}
	}
	if sc.Check.Name == "" {
		def.Skip = "the project no longer adopts this check"
		return def, nil
	}
	if why := remediation.Unprovable(sc); why != "" {
		def.Skip = why
		return def, nil
	}
	def.Title = remediation.ChangeTitle(sc)
	def.Acceptance = &workflow.Acceptance{Checks: []workflow.AcceptanceCheck{remediation.AcceptanceCheck(sc)}}
	def.Body = remediation.Brief(item.Block, sc.Version, sc, item.OriginDetail)
	if item.Reason != "" {
		def.Body += "\n\nPrevious attempt: " + item.Reason + "\n"
	}
	// Preserve the current-head observation at the remediation command boundary.
	if sourceProject, err := project.ByIDIn(ctx, a.DB, p.ID); err == nil && sourceProject.DefaultBranch != "" {
		repo := filepath.Join(a.DataDir, "workspaces", "project-"+sourceProject.Slug)
		if _, err := os.Stat(filepath.Join(repo, ".git")); err == nil {
			if sha, err := gitexec.Run(ctx, repo, "rev-parse", sourceProject.DefaultBranch); err == nil {
				def.Base = strings.TrimSpace(sha)
			}
		}
	}
	return def, nil
}
