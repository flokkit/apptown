package deployment

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/releasesql"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// ReleaseApproval preserves the person, how they assented, when and the exact
// meaning accepted. Authority retains the credential bounds for execution.
type ReleaseApproval struct {
	Actor     access.Actor         `json:"actor"`
	At        time.Time            `json:"at"`
	Meaning   string               `json:"meaning"`
	Authority operations.Authority `json:"authority"`
}

func approvalMeaning(number int64, label string) string {
	return fmt.Sprintf("I accept release %d running on %s", number, label)
}

func (s *Service) approvalMeaning(ctx context.Context, r *Release) (string, error) {
	dep, err := s.Deployments.ByID(ctx, r.DeploymentID)
	if err != nil {
		return "", err
	}
	return approvalMeaning(r.Number, dep.DisplayLabel), nil
}

// admitApprovalIn runs under the project's release lock, in the request's
// transaction. Only waiting requests can be superseded; a running release keeps
// its existing exclusion. Supersession and its explanation commit together.
func (s *Service) admitApprovalIn(ctx context.Context, tx *database.Tx, dep *Deployment, r NumberedRelease) (ReleaseState, error) {
	q := deploymentsql.New(tx.SQL())
	ids, err := q.SupersedeWaitingReleases(ctx, deploymentsql.SupersedeWaitingReleasesParams{DeploymentID: dep.ID, NewRelease: r.ID})
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := appendReleaseEvent(ctx, tx, id, fmt.Sprintf("superseded by release %d", r.Number), s.now()); err != nil {
			return 0, err
		}
	}
	state := Releasing
	if dep.ApprovalRole != "" {
		state = releaseWaiting
	}
	if _, err := q.SetReleaseApprovalRule(ctx, deploymentsql.SetReleaseApprovalRuleParams{ID: r.ID, ApprovalRole: dep.ApprovalRole, State: state.String()}); err != nil {
		return 0, err
	}
	if state == releaseWaiting {
		if err := appendReleaseEvent(ctx, tx, r.ID, "waiting for approval from "+dep.ApprovalRole+": "+approvalMeaning(r.Number, dep.DisplayLabel), s.now()); err != nil {
			return 0, err
		}
	}
	return state, nil
}

func (s *Service) requireApprovalRole(ctx context.Context, pr access.Principal, r *Release) error {
	if pr.Kind != "user" || pr.ID.IsZero() || !pr.Actor().Assents() {
		return fault.New(fault.Forbidden, "release approval requires a person's own assent; sign in or use a command-line token")
	}
	if s.release.Grants == nil || s.projects == nil {
		return access.ErrAuthorityUnavailable
	}
	resource, err := s.projects.AuthorityResource(ctx, r.ProjectID)
	if err != nil {
		return err
	}
	grants, err := s.release.Grants.ActiveGrants(ctx, pr)
	if err != nil {
		return err
	}
	if r.ApprovalRole == "" || !access.HoldsRole(grants, r.ApprovalRole, access.CapReleaseApprove, resource) {
		return fault.New(fault.Forbidden, "this release needs someone holding "+r.ApprovalRole+" on the project")
	}
	return nil
}

func (s *Service) approvalCaller(ctx context.Context, r *Release, consequential bool) (access.Caller, error) {
	if s.release.Authority == nil {
		return access.Caller{}, access.ErrAuthorityUnavailable
	}
	caller, err := s.release.Authority.Require(ctx, access.Permission{Capability: access.CapReleaseApprove, Resource: access.Resource{Type: "release-target", ID: r.Deployment, ProjectID: r.ProjectID}, Context: access.Context{Consequential: consequential, Route: "release.approve"}})
	if err != nil {
		return caller, err
	}
	if !caller.Verified {
		return caller, access.ErrPrincipalRequired
	}
	if err := s.requireApprovalRole(ctx, caller.Principal, r); err != nil {
		return caller, err
	}
	metadata, err := s.Releases.RequestMetadata(ctx, r.ID)
	if err != nil {
		return caller, err
	}
	if metadata != nil && metadata.Setter == caller.Principal.ID {
		return caller, fault.New(fault.Forbidden, "the requester cannot approve their own release; ask another person holding "+r.ApprovalRole)
	}
	return caller, nil
}

// ReleaseDecision is the current question and the caller's permitted doors.
// Clients display Meaning before submitting it to ApproveRelease.
type ReleaseDecision struct {
	Meaning    string `json:"meaning"`
	CanApprove bool   `json:"canApprove"`
	CanCancel  bool   `json:"canCancel"`
}

func (s *Service) ReleaseDecision(ctx context.Context, r *Release) (ReleaseDecision, error) {
	out := ReleaseDecision{}
	if r.State != releaseWaiting {
		if r.Approval != nil {
			out.Meaning = r.Approval.Meaning
		}
		return out, nil
	}
	return s.releaseEligibility(ctx, r)
}

func (s *Service) releaseEligibility(ctx context.Context, r *Release) (ReleaseDecision, error) {
	meaning, err := s.approvalMeaning(ctx, r)
	if err != nil {
		return ReleaseDecision{}, err
	}
	out := ReleaseDecision{Meaning: meaning}
	_, err = s.approvalCaller(ctx, r, false)
	if err == nil {
		out.CanApprove, out.CanCancel = true, true
	} else if fault.KindOf(err) != fault.Forbidden {
		return out, err
	}
	if caller, ok := access.CallerFrom(ctx); ok && caller.Verified {
		metadata, err := s.Releases.RequestMetadata(ctx, r.ID)
		if err != nil {
			return out, err
		}
		if metadata != nil && !metadata.Setter.IsZero() && metadata.Setter == caller.Principal.ID {
			out.CanCancel = true
		}
	}
	return out, nil
}

// ApproveRelease records assent and enqueues execution atomically. A second
// decision, cancellation or supersession cannot race this transition.
func (s *Service) ApproveRelease(ctx context.Context, projectID ids.ID, number int64, meaning string) error {
	return s.decideRelease(ctx, projectID, number, meaning, false)
}

// CancelRelease cancels a waiting request for its requester or an approver.
func (s *Service) CancelRelease(ctx context.Context, projectID ids.ID, number int64) error {
	return s.decideRelease(ctx, projectID, number, "", true)
}

func (s *Service) decideRelease(ctx context.Context, projectID ids.ID, number int64, meaning string, cancel bool) error {
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		if _, err := releasesql.New(tx.SQL()).LockReleaseProject(ctx, projectID); err != nil {
			return err
		}
		rel, err := s.Releases.ByNumber(ctx, projectID, number)
		if err != nil {
			return err
		}
		q := deploymentsql.New(tx.SQL())
		row, err := q.RequestedReleaseForUpdate(ctx, rel.ID)
		if err != nil {
			return err
		}
		rel, err = releaseRecord(row.Release, nil)
		if err != nil {
			return err
		}
		var caller access.Caller
		var approveErr error
		if !cancel {
			caller, approveErr = s.approvalCaller(ctx, rel, true)
		}
		state := Releasing
		var raw *json.RawMessage
		event := ""
		if cancel {
			decision, err := s.releaseEligibility(ctx, rel)
			if err != nil {
				return err
			}
			if !decision.CanCancel {
				return fault.New(fault.Forbidden, "only the requester or an approver can cancel this release")
			}
			c, ok := access.CallerFrom(ctx)
			if !ok || !c.Verified {
				return access.ErrPrincipalRequired
			}
			state, event = releaseCancelled, fmt.Sprintf("cancelled by %s (principal %s) via %s", c.Principal.Name, c.Principal.ID, c.Principal.Actor().Via)
		} else {
			if approveErr != nil {
				return approveErr
			}
			expected, err := s.approvalMeaning(ctx, rel)
			if err != nil {
				return err
			}
			if meaning != expected {
				return fault.New(fault.Conflict, "read and accept this release's approval meaning before approving")
			}
			value, err := json.Marshal(ReleaseApproval{Actor: caller.Principal.Actor(), At: s.now(), Meaning: meaning, Authority: operations.AuthorityOf(caller.Principal)})
			if err != nil {
				return err
			}
			captured := json.RawMessage(value)
			raw = &captured
			event = "approved by " + caller.Principal.Name + " via " + caller.Principal.Actor().Via + ": " + meaning
		}
		// Refuse callers without authority before exposing the request's state.
		if rel.State != releaseWaiting {
			return fault.New(fault.Conflict, "this release is no longer waiting for approval; reload its deployment")
		}
		n, err := q.DecideWaitingRelease(ctx, deploymentsql.DecideWaitingReleaseParams{ID: rel.ID, State: state.String(), Approval: raw})
		if err != nil {
			return err
		}
		if n != 1 {
			return fault.New(fault.Conflict, "this release is no longer waiting for approval")
		}
		if err := appendReleaseEvent(ctx, tx, rel.ID, event, s.now()); err != nil {
			return err
		}
		if !cancel {
			return s.release.Queue.EnqueueReleaseIn(ctx, tx, rel.ID)
		}
		return nil
	})
}

func (s *Service) checkReleaseApproval(ctx context.Context, rel *Release) error {
	dep, err := s.Deployments.ByID(ctx, rel.DeploymentID)
	if err != nil {
		return err
	}
	if dep.ApprovalRole != rel.ApprovalRole {
		return fault.New(fault.Conflict, "deployment approval rule changed; request a new release")
	}
	if rel.ApprovalRole == "" {
		return nil
	}
	if rel.Approval == nil || rel.Approval.Meaning == "" {
		return fault.New(fault.Forbidden, "this release needs approval before it can run")
	}
	pr := rel.Approval.Authority.Principal()
	if err := s.requireApprovalRole(ctx, pr, rel); err != nil {
		return err
	}
	resource, err := s.projects.AuthorityResource(ctx, rel.ProjectID)
	if err != nil {
		return err
	}
	resource.Type, resource.ID = "release-target", rel.Deployment
	if s.release.Still == nil {
		return access.ErrAuthorityUnavailable
	}
	return s.release.Still.RequireStill(ctx, pr, access.Permission{Capability: access.CapReleaseApprove, Resource: resource, Context: access.Context{Consequential: true, Route: "release.execute"}})
}

// WaitingRelease is a caller-filtered approval task, linking to the deployment
// where the person can act. No authority result is cached between reads.
type WaitingRelease struct {
	Release         Release `json:"release"`
	DeploymentLabel string  `json:"deploymentLabel"`
	Href            string  `json:"href"`
	Project         string  `json:"project"`
}

func (s *Service) WaitingReleases(ctx context.Context) ([]WaitingRelease, error) {
	out := []WaitingRelease{}
	rows, err := deploymentsql.New(s.db.SQL()).WaitingReleases(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		rel, err := releaseRecord(row, nil)
		if err != nil {
			return nil, err
		}
		if _, err := s.approvalCaller(ctx, rel, false); err != nil {
			if fault.KindOf(err) == fault.Forbidden {
				continue
			}
			return nil, err
		}
		if _, err := s.release.Authority.Require(ctx, access.Permission{Capability: access.CapDeploymentRead, Resource: access.Resource{Type: "project", ID: rel.ProjectID.String(), ProjectID: rel.ProjectID}}); err != nil {
			if fault.KindOf(err) == fault.Forbidden {
				continue
			}
			return nil, err
		}
		project, err := s.release.Sources.Project(ctx, rel.ProjectID)
		if err != nil {
			return nil, err
		}
		dep, err := s.Deployments.ByID(ctx, rel.DeploymentID)
		if err != nil {
			return nil, err
		}
		out = append(out, WaitingRelease{Release: *rel, DeploymentLabel: dep.DisplayLabel, Project: project.Slug, Href: fmt.Sprintf("/projects/%s/deployments/%s", project.Slug, rel.Deployment)})
	}
	return out, nil
}
