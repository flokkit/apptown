package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/strs"
)

const ReleaseJob = "deployment-release"

type DeploySelection struct {
	ProjectID      ids.ID
	DeploymentName string
	// Address edits require bind authority and the revision the caller saw.
	AddressRevision *int64
	CustomHostname  *string
	BundleID        ids.ID
	// AcceptUncopiedData is a person's choice to release onto a deployment
	// holding data AppTown cannot copy now (DataCopyCheck); the release
	// records it with who and why.
	AcceptUncopiedData bool
	// AcceptUncheckedCopy is a person's choice to fill a deployment that
	// starts from a copy, on its first release, though an item of the copy
	// was not shown to come back by its last check (CopyStarter.UncheckedStart);
	// the release records it with who and which items.
	AcceptUncheckedCopy bool
}
type ReleaseProject struct {
	Application
	Repository string
	OwnerGroup string
}
type ProjectSource interface {
	Project(context.Context, ids.ID) (ReleaseProject, error)
}
type ReleaseSources interface {
	ProjectSource
	Contract(context.Context, ReleaseProject, string) (*appcontract.Contract, string, error)
}
type ReleaseQueue interface {
	EnqueueReleaseIn(context.Context, *database.Tx, ids.ID) error
}

type AcceptedRelease struct {
	// URL is the admitted address, when this placement has one.
	URL string
	NumberedRelease
	Deployment string
	State      ReleaseState
}

var (
	ErrDeploymentMissing = fault.New(fault.NotFound, "no such deployment")
	ErrBundleMissing     = fault.New(fault.NotFound, "no bundle on this project")
	ErrNoBundle          = fault.New(fault.Unprocessable, "nothing has landed with a runtime contract yet — there is no bundle to ship")
	ErrPlacementChanged  = fault.New(fault.Conflict, "deployment placement changed; request a new release")
)

type ContractReadError struct{ Cause error }

func (e *ContractReadError) Error() string {
	return fmt.Sprintf("reading the selected contract: %v", e.Cause)
}
func (e *ContractReadError) Unwrap() error         { return e.Cause }
func (e *ContractReadError) FaultKind() fault.Kind { return fault.Unprocessable }

func (e *Deployment) releaseAttributes() Attributes {
	return Attributes{Feed: e.Feed, Audience: e.Audience, DataClass: e.DataClass, PromoteAfter: e.PromoteAfter}
}

// matchesRelease is the placement a release was accepted for, still: the
// same target, generation and attributes, and still deployable by hand with
// bundle, given the bundle the deployment runs (running).
func (e *Deployment) matchesRelease(target ids.ID, generation int64, attrs Attributes, bundle, running ids.ID) error {
	if e.TargetID != target || e.DesiredGeneration != generation || e.releaseAttributes() != attrs {
		return ErrPlacementChanged
	}
	return e.DeployableByHandAt(bundle, running)
}

// requireDeploy is the authority to change what a deployment runs: the
// deploy grant on its project, and for a protected deployment the release
// grant on it too. Route names the act in the decision's receipt.
func (s *Service) requireDeploy(ctx context.Context, projectID ids.ID, deployment string, protected bool, route string) (access.Caller, error) {
	actor, err := s.release.Authority.Require(ctx, access.Permission{Capability: access.CapDeploymentDeploy, Resource: access.Resource{Type: "project", ID: projectID.String(), ProjectID: projectID}, Context: access.Context{Consequential: true, Route: route}})
	if err != nil {
		return access.Caller{}, err
	}
	if protected {
		if _, err := s.release.Authority.Require(ctx, access.Permission{Capability: access.CapReleasePromote, Resource: access.Resource{Type: "release-target", ID: deployment, ProjectID: projectID}, Context: access.Context{Consequential: true, Route: route}}); err != nil {
			return access.Caller{}, err
		}
	}
	return actor, nil
}

// RequestRelease authorizes manual deployment and atomically records a release,
// its selected placement and durable job intent. A zero BundleID selects the
// project's latest bundle; a bundle from another project is refused.
// Contract resolution happens before the transaction, which rechecks placement
// and generation. ErrPlacementChanged requires a new request. Success means the
// request was accepted; provider execution happens later through ExecuteRelease.
func (s *Service) RequestRelease(ctx context.Context, in DeploySelection) (AcceptedRelease, error) {
	return s.requestRelease(ctx, in, nil)
}

func (s *Service) requestRelease(ctx context.Context, in DeploySelection, selectedDefault *ReleaseDefault) (AcceptedRelease, error) {
	if err := ctx.Err(); err != nil {
		return AcceptedRelease{}, err
	}
	if s.release.Authority == nil || s.release.Sources == nil || s.release.Queue == nil || s.release.Secrets == nil {
		return AcceptedRelease{}, errors.New("release operations are not configured")
	}
	var dep *Deployment
	var err error
	if selectedDefault == nil {
		dep, err = s.Deployments.ByName(ctx, in.ProjectID, in.DeploymentName)
	} else {
		dep, err = s.Deployments.ByID(ctx, selectedDefault.DeploymentID)
		if err == nil {
			if dep.ProjectID != in.ProjectID {
				return AcceptedRelease{}, ErrDeploymentMissing
			}
			in.DeploymentName = dep.Name
		}
	}
	found, err := database.Lookup(err)
	if err != nil {
		return AcceptedRelease{}, err
	}
	actor, err := s.requireDeploy(ctx, in.ProjectID, in.DeploymentName, found && dep.needsPromotion(), "deployment.request")
	if err != nil {
		return AcceptedRelease{}, err
	}
	if !found {
		return AcceptedRelease{}, ErrDeploymentMissing
	}
	if actor.Principal.Name == "" {
		return AcceptedRelease{}, errors.New("release caller required")
	}
	if in.CustomHostname != nil {
		if in.AddressRevision == nil || *in.AddressRevision < 0 {
			return AcceptedRelease{}, fault.New(fault.Invalid, "send the address revision the edit started from")
		}
		if _, err := s.addressAuthority(ctx, in.ProjectID, true); err != nil {
			return AcceptedRelease{}, err
		}
	}
	// A landing-fed deployment may still take the bundle it runs, named
	// explicitly; that is decided once the bundle is known
	// (DeployableByHandAt). Asking it for the latest bundle is landing's.
	if err := dep.DeployableByHand(); err != nil && (!errors.Is(err, ErrFedByLanding) || in.BundleID.IsZero()) {
		return AcceptedRelease{}, err
	}
	// A project being deleted, or deleted, takes no release; while a
	// take-down runs, nothing deploys the app it is deleting. (Said early
	// here; release creation decides both under the lock.)
	if err := project.RefuseUnlessActive(ctx, s.db, in.ProjectID); err != nil {
		return AcceptedRelease{}, err
	}
	// A take-down holds the deployment whole (SubjectOf); data operations
	// hold only its data and do not meet a release.
	if op, err := s.RunningOperation(ctx, dep); err != nil {
		return AcceptedRelease{}, err
	} else if op != nil && slices.Contains(op.Holds, SubjectOf(dep)) {
		return AcceptedRelease{}, &operations.HeldError{Holder: op}
	}
	// The first release of a deployment that starts from a copy fills its
	// data: it is a restore, so its caller needs the restore grant in the
	// copy's project and in this one, as whoever chose the start did.
	// It fills only from a copy each item of which its last check showed
	// to come back, unless a person accepts one that was not.
	uncheckedNote := ""
	if !dep.StartFrom.IsZero() && !dep.StartedFrom.Valid {
		if err := s.checkStart(ctx, dep.StartFrom, in.ProjectID, dep.DataClass); err != nil {
			return AcceptedRelease{}, err
		}
		if uncheckedNote, err = s.uncheckedStart(ctx, dep, in.AcceptUncheckedCopy, actor.Principal.Name); err != nil {
			return AcceptedRelease{}, err
		}
	}
	var bundle *ReleaseBundle
	if in.BundleID.IsZero() {
		bundle, err = s.Bundles.Latest(ctx, in.ProjectID)
	} else {
		bundle, err = s.Bundles.ByID(ctx, in.BundleID)
	}
	if found, e := database.Lookup(err); e != nil {
		return AcceptedRelease{}, e
	} else if !found {
		return AcceptedRelease{}, ErrBundleMissing
	}
	if bundle == nil {
		return AcceptedRelease{}, ErrNoBundle
	}
	if bundle.ProjectID != in.ProjectID {
		return AcceptedRelease{}, ErrBundleMissing
	}
	running, err := s.handBundle(ctx, dep)
	if err != nil {
		return AcceptedRelease{}, err
	}
	if err := dep.DeployableByHandAt(bundle.ID, running); err != nil {
		return AcceptedRelease{}, err
	}
	t, err := s.Targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return AcceptedRelease{}, fmt.Errorf("%w: %w", ErrNoTarget, err)
	}
	if err := s.refusesPlacement(ctx, in.ProjectID, t); err != nil {
		return AcceptedRelease{}, err
	}
	if err := s.providers.RefusesBundle(ctx, bundle, t); err != nil {
		return AcceptedRelease{}, err
	}
	if d, err := s.providers.Driver(ctx, t, dep); err != nil {
		return AcceptedRelease{}, err
	} else if r, ok := d.(releaseRefuser); ok {
		if err := r.refusesRelease(bundle); err != nil {
			return AcceptedRelease{}, err
		}
	}
	project, err := s.release.Sources.Project(ctx, in.ProjectID)
	if err != nil {
		return AcceptedRelease{}, err
	}
	contract, revision, err := s.release.Sources.Contract(ctx, project, bundle.ContractRevision)
	if err != nil {
		return AcceptedRelease{}, &ContractReadError{err}
	}
	if contract == nil || revision == "" {
		return AcceptedRelease{}, &ContractReadError{errors.New("selected source has no committed contract")}
	}
	if why, err := s.env.RefusesContract(ctx, t, contract); err != nil || why != "" {
		return AcceptedRelease{}, errors.Join(err, refusal(why))
	}
	if why, err := s.env.RefusesBrowserValues(ctx, t.Kind, in.ProjectID, contract); err != nil || why != "" {
		return AcceptedRelease{}, errors.Join(err, refusal(why))
	}
	refused, accepted, err := s.uncopiedData(ctx, dep, t, bundle.ID, in.AcceptUncopiedData, actor.Principal.Name)
	if err != nil {
		return AcceptedRelease{}, err
	}
	if refused != "" {
		return AcceptedRelease{}, refusal(refused)
	}
	binding, err := providerBindingOf(t)
	if err != nil {
		return AcceptedRelease{}, err
	}
	if t.Kind == KindPortainer {
		if _, err := s.providers.PortainerConfig(ctx, t); err != nil {
			return AcceptedRelease{}, err
		}
	}
	if !actor.Verified {
		return AcceptedRelease{}, errReleaseAuthority
	}
	authority, err := json.Marshal(operations.AuthorityOf(actor.Principal))
	if err != nil {
		return AcceptedRelease{}, err
	}
	if _, err := releasePrincipal(authority); err != nil {
		return AcceptedRelease{}, err
	}
	captured := json.RawMessage(authority)
	var admitted AcceptedRelease
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		rel, err := startReleaseIn(ctx, tx, ReleaseDraft{Source: ReleaseFromBundle, ProjectID: in.ProjectID, DeploymentID: dep.ID, BundleID: bundle.ID, Deployment: dep.Name, Revision: revision, Actor: actor.Principal.Name, At: s.now()}, fmt.Sprintf("to %s started by %s — bundle %d (tree %s) on %s", dep.Name, actor.Principal.Name, bundle.ID, strs.ShortSHA(revision), t.Name)+accepted+uncheckedNote)
		if err != nil {
			return err
		}
		q := deploymentsql.New(tx.SQL())
		if selectedDefault != nil {
			settings, err := q.LockProjectReleaseSettings(ctx, in.ProjectID)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrReleaseDefaultChanged
			}
			if err != nil {
				return err
			}
			if settings.Revision != selectedDefault.Revision || settings.DefaultDeploymentID != dep.ID {
				return ErrReleaseDefaultChanged
			}
		}
		lockedTarget, err := targetRecord(q.TargetByNameForUpdate(ctx, t.Name))
		if err != nil {
			return err
		}
		if lockedTarget.ID != t.ID || lockedTarget.SettingsRevision != t.SettingsRevision {
			return ErrTargetSettingsMoved
		}
		current, err := LockDeploymentIn(ctx, tx, dep.ID)
		if err != nil {
			return err
		}
		if selectedDefault != nil {
			if err := s.defaultEligible(ctx, q, in.ProjectID, current, lockedTarget); err != nil {
				return err
			}
		}
		if _, err := s.requireDeploy(ctx, current.ProjectID, current.Name, current.needsPromotion(), "deployment.request"); err != nil {
			return err
		}
		running, err := s.handBundle(ctx, current)
		if err != nil {
			return err
		}
		if err := current.matchesRelease(dep.TargetID, dep.DesiredGeneration, dep.releaseAttributes(), bundle.ID, running); err != nil {
			return err
		}
		// The start this caller was authorised for above is the one the
		// release records and fills.
		if current.StartFrom != dep.StartFrom || current.StartedFrom.Valid != dep.StartedFrom.Valid {
			return ErrPlacementChanged
		}
		address, err := s.admitAddressIn(ctx, tx, current, lockedTarget, in.AddressRevision, in.CustomHostname, actor.Principal.Name)
		if err != nil {
			return err
		}
		var ingress *json.RawMessage
		if address != nil {
			raw, err := addressSnapshotJSON(address)
			if err != nil {
				return err
			}
			ingress = &raw
		}
		startFrom := ids.ID(ids.Nil)
		if !dep.StartedFrom.Valid {
			startFrom = dep.StartFrom
		}
		attrs, err := json.Marshal(dep.releaseAttributes())
		if err != nil {
			return err
		}
		if _, err := deploymentsql.New(tx.SQL()).InsertReleaseRequest(ctx, deploymentsql.InsertReleaseRequestParams{ReleaseID: rel.ID, TargetID: t.ID, BaseGeneration: dep.DesiredGeneration, AttributesJson: attrs, StartFromBackup: startFrom, ProviderBindingJson: binding, IngressJson: ingress, AuthorityJson: &captured, RequirePromotion: current.needsPromotion()}); err != nil {
			return err
		}
		if err := s.release.Queue.EnqueueReleaseIn(ctx, tx, rel.ID); err != nil {
			return err
		}
		admitted = AcceptedRelease{NumberedRelease: rel, Deployment: dep.Name, State: Releasing}
		if address != nil {
			admitted.URL = address.Ingress.URL
		}
		return nil
	})
	if err != nil {
		return AcceptedRelease{}, err
	}
	return admitted, nil
}
