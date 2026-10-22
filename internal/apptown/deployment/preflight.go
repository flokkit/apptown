package deployment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// Preflight is the early check of a release against its deployment, made
// before anything is built for it: what the target can run, the deployment's profile and overrides, the needs
// fulfilled by registering, and the app's secrets as the deployment binds
// them. It uses the rules delivery uses and has none of delivery's effects:
// no secret is generated, no need registered or provisioned, nothing booted,
// and no value leaves AppTown. Values a need injects at boot (a database's
// DATABASE_URL) are not settings, so a need not yet provisioned is not a
// missing setting.
//
// A pass is true when it is checked, not a reservation: the deployment's
// own doors (boot, ExecuteRelease) judge authority and settings again, and
// a setting removed after the check still refuses there.

// PreflightCategory is the stable kind of a preflight refusal.
type PreflightCategory string

const (
	// PreflightMissingSettings: required settings nothing provides for
	// the deployment.
	PreflightMissingSettings PreflightCategory = "missing-settings"
	// PreflightRefused: the release cannot run there as declared.
	PreflightRefused PreflightCategory = "refused"
	// PreflightUnassessable: something the check must read (a secret
	// store, the deployment's records) could not be read. Not a pass, and
	// not a claim that a setting is absent.
	PreflightUnassessable PreflightCategory = "unassessable"
)

// PreflightInput is the selected release: the deployment and the
// immutable commit, staged at Workspace, whose committed contract is
// Contract.
type PreflightInput struct {
	ProjectID    ids.ID
	DeploymentID ids.ID
	Commit       string
	Workspace    string
	Contract     *appcontract.Contract
}

// PreflightCheck is what a check was of, and when.
type PreflightCheck struct {
	DeploymentID ids.ID    `json:"deploymentId"`
	Deployment   string    `json:"deployment"`
	TargetID     ids.ID    `json:"targetId"`
	Commit       string    `json:"commit"`
	CheckedAt    time.Time `json:"checkedAt"`
}

// PreflightError is a preflight that did not pass. Its words are safe to
// show and keep: setting names, never values, endpoints or a store's own
// error.
type PreflightError struct {
	Category PreflightCategory
	Check    PreflightCheck
	// Missing and Consumers are the missing settings and the parts and
	// init steps (init-<name>) they would reach.
	Missing   []string
	Consumers []string
	Reason    string
	Next      string
}

func (e *PreflightError) Error() string {
	out := e.Reason
	if e.Next != "" {
		out += "; " + e.Next
	}
	return out
}

// FaultKind: an unassessable check is a dependency unavailable; a refusal
// is a release the current state cannot accept.
func (e *PreflightError) FaultKind() fault.Kind {
	if e.Category == PreflightUnassessable {
		return fault.Unavailable
	}
	return fault.Unprocessable
}

// Evidence is the refusal as a step's evidence and log keep it.
func (e *PreflightError) Evidence() map[string]any {
	out := map[string]any{"category": e.Category, "check": e.Check, "reason": e.Reason}
	if len(e.Missing) > 0 {
		out["missing"], out["consumers"] = e.Missing, e.Consumers
	}
	if e.Next != "" {
		out["next"] = e.Next
	}
	return out
}

// Preflight checks the selected release against its deployment. It
// answers what it checked; a release that would be refused is a
// *PreflightError. The caller has selected an authorized project and
// deployment; Preflight grants nothing.
func (s *Service) Preflight(ctx context.Context, in PreflightInput) (PreflightCheck, error) {
	check := PreflightCheck{DeploymentID: in.DeploymentID, Commit: in.Commit, CheckedAt: s.now()}
	if in.Contract == nil || in.Workspace == "" || in.Commit == "" {
		return check, errors.New("a preflight checks a selected commit's staged contract")
	}
	dep, err := s.Deployments.ByID(ctx, in.DeploymentID)
	if err != nil {
		return check, unassessable(check, "the deployment could not be read")
	}
	if dep.ProjectID != in.ProjectID {
		return check, errors.New("the deployment belongs to another project")
	}
	check.Deployment, check.TargetID = dep.Name, dep.TargetID
	t, err := s.Targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return check, unassessable(check, fmt.Sprintf("the %s deployment's target could not be read", dep.Name))
	}
	p, err := s.projects.ByID(ctx, in.ProjectID)
	if err != nil {
		return check, unassessable(check, "the project could not be read")
	}
	effective, _, err := s.effectiveContract(ctx, dep, in.Workspace, in.Contract)
	if err != nil {
		return check, preflightRefusal(check, err, fmt.Sprintf("the %s deployment's profile and overrides could not be read", dep.Name))
	}
	return check, s.preflight(ctx, check, in.ProjectID, t, p.Slug, in.Workspace, effective)
}

// preflight is the check of the effective contract c, staged at ws, for
// the deployment check names on target t: the part ExecuteRelease shares.
func (s *Service) preflight(ctx context.Context, check PreflightCheck, projectID ids.ID, t *Target, slug, ws string, c *appcontract.Contract) error {
	if why, err := s.env.RefusesContract(ctx, t, c); err != nil {
		return preflightRefusal(check, err, fmt.Sprintf("what %s provides could not be read", t.Name))
	} else if why != "" {
		return &PreflightError{Category: PreflightRefused, Check: check, Reason: why}
	}
	if why, err := s.env.RefusesBrowserValues(ctx, t.Kind, projectID, c); err != nil {
		return unassessable(check, "the values the integrations give the page could not be read")
	} else if why != "" {
		return &PreflightError{Category: PreflightRefused, Check: check, Reason: why}
	}
	if err := s.env.registrable(ctx, c); err != nil {
		return preflightRefusal(check, err, "the needs this app registers could not be read")
	}
	err := s.env.secrets.Check(ctx, projectID, slug, check.Deployment, ws, c)
	var missing *secret.MissingError
	var unavailable *secret.UnavailableError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &missing):
		return &PreflightError{Category: PreflightMissingSettings, Check: check, Missing: missing.Names, Consumers: missing.Consumers,
			Reason: fmt.Sprintf("the %s deployment has no value for %s", check.Deployment, strings.Join(missing.Names, ", ")),
			Next:   fmt.Sprintf("set %s for %s under Settings → Secrets, then deploy again", strings.Join(missing.Names, ", "), check.Deployment)}
	case errors.As(err, &unavailable):
		return unassessable(check, unavailable.Error())
	}
	return preflightRefusal(check, err, fmt.Sprintf("the %s deployment's secrets could not be read", check.Deployment))
}

// preflightRefusal classifies a failed rule: a declaration or state the
// owner refuses (fault-classified) is a refusal in the owner's words;
// anything else could not be assessed, and says only what was being read.
func preflightRefusal(check PreflightCheck, err error, reading string) error {
	switch fault.KindOf(err) {
	case fault.Internal, fault.Unavailable:
		return unassessable(check, reading)
	}
	return &PreflightError{Category: PreflightRefused, Check: check, Reason: fault.Message(err)}
}

func unassessable(check PreflightCheck, reason string) error {
	return &PreflightError{Category: PreflightUnassessable, Check: check, Reason: reason, Next: "the release was not built; deploy again when it can be read"}
}
