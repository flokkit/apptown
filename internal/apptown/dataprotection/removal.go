package dataprotection

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/oci"

	"github.com/flokkit/apptown/internal/apptown/dataprotection/internal/protectionsql"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/database"
)

type RemoveDeployment struct {
	ProjectID ids.ID
	Name      string
	// withProject: the project is being deleted, so main goes too, the
	// same way as any other deployment (TakeDownWithProject).
	withProject bool
	// WithoutFinalCopy is a person's explicit choice to take down a
	// deployment whose data AppTown cannot take a final copy of now (no
	// store, no copy runner, nothing it can reach): it names the deployment,
	// as a confirmation. The data is kept at the provider as a retained
	// resource; the receipt records the choice and why there was no copy.
	// Without it such a take-down is refused (ErrNoFinalCopy).
	WithoutFinalCopy string
	// noCopy is why the take-down goes with no final copy, when the person
	// chose that: recorded in its receipt.
	noCopy string
	// binding, when set, is the deletion's scoped system binding the
	// take-down runs under, instead of the caller (TakeDownForDeletion).
	binding *access.Principal
}

// takeDownActor is who a take-down runs as: the deletion's binding when it
// runs under one (checked as any captured authority is, before effects),
// else the caller, who needs deployment:teardown in the project.
func (s *Service) takeDownActor(ctx context.Context, in RemoveDeployment, route string) (access.Principal, error) {
	if in.binding != nil {
		if s.ops == nil {
			return access.Principal{}, errors.New("operations are not configured")
		}
		if err := s.ops.Allowed(ctx, *in.binding, access.CapDeploymentTeardown, in.ProjectID, route); err != nil {
			return access.Principal{}, err
		}
		return *in.binding, nil
	}
	if s.authority == nil {
		return access.Principal{}, errors.New("deployment removal is not configured")
	}
	actor, err := s.authority.Require(ctx, access.Permission{Capability: access.CapDeploymentTeardown, Resource: access.Resource{Type: "project", ID: in.ProjectID.String(), ProjectID: in.ProjectID}, Context: access.Context{Route: route, Consequential: true}})
	if err != nil {
		return access.Principal{}, err
	}
	if actor.Principal.Name == "" {
		return access.Principal{}, errors.New("removal caller required")
	}
	return actor.Principal, nil
}

// RemovalResult is what a take-down did: what it deleted, and what it left
// at the target (data kept where there is no backup, and anything it failed
// to delete), each also a retained-resources row.
type RemovalResult struct {
	Deleted  []string
	Retained []string
	// FinalBackup is the set taken before the data was deleted; nil when
	// the deployment held no data AppTown backs up.
	FinalBackup *FinalSet
}

// FinalSet names a take-down's final backup: what to restore from and its
// size. It is kept with its project until the project is purged.
type FinalSet struct {
	ID    ids.ID `json:"id"`
	Bytes int64  `json:"bytes"`
}

// Remove is the checked teardown door. Preflight and settlement are short
// transactions; the provider call holds no database transaction. A stale response
// cannot delete a newer binding or transfer its needs into the retained ledger.
// This does not fence external provider mutation between those transactions.
func (s *Service) Remove(ctx context.Context, in RemoveDeployment) (RemovalResult, error) {
	if err := ctx.Err(); err != nil {
		return RemovalResult{}, err
	}
	principal, err := s.takeDownActor(ctx, in, "deployment.remove")
	if err != nil {
		return RemovalResult{}, err
	}
	return s.remove(ctx, in, principal, nil, nil)
}

// remove is the removal, as principal. allowed, when set (a take-down
// operation's check), is asked again before each consequential effect:
// the final backup, the teardown, and the keys' revocation with the
// records, which follow it at once.
// Refused, nothing more is done and the error says what already was.
// stop, when set (a take-down operation a person asked to stop), is asked
// before the final backup and before the teardown, the safe boundaries:
// stopping there keeps the backup taken and starts stopped parts again.
func (s *Service) remove(ctx context.Context, in RemoveDeployment, principal access.Principal, allowed func(context.Context) error, stop func(context.Context) bool) (RemovalResult, error) {
	stopped := func() bool { return stop != nil && stop(ctx) }
	ask := func(done string) error {
		if allowed == nil {
			return nil
		}
		if err := allowed(ctx); err != nil {
			return fmt.Errorf("%s%w", done, err)
		}
		return nil
	}
	actor := access.Caller{Principal: principal}
	selected, err := database.Transact(ctx, s.db, func(tx *database.Tx) (*deployment.Deployment, error) { return selectRemovalIn(ctx, tx, in) })
	if err != nil {
		return RemovalResult{}, err
	}
	// Nothing of the deployment is stopped, copied or deleted while a
	// restore writes it (dataWork).
	release, err := s.dataWork.claim(selected.ID, "a take-down of "+selected.Name)
	if err != nil {
		return RemovalResult{}, err
	}
	defer release()
	target, err := s.targets.ByID(ctx, selected.TargetID)
	if err != nil {
		return RemovalResult{}, fmt.Errorf("resolving removal target: %w", err)
	}
	driver, err := s.providers.Driver(ctx, target, selected)
	if err != nil {
		return RemovalResult{}, err
	}
	owned, err := s.deployments.Owned(ctx, selected.ID)
	if err != nil {
		return RemovalResult{}, fmt.Errorf("reading what the deployment owns: %w", err)
	}
	// The target deletes what it made; the AI models keys AppTown made
	// through a connector it revokes itself, after the target is done.
	owned, keys := splitOwned(owned)
	if err := ctx.Err(); err != nil {
		return RemovalResult{}, err
	}
	// The data goes only after a complete final backup of exactly that
	// data; a backup that falls short refuses the take-down here. An eval
	// copy's data is synthetic and nothing will restore it: it goes with no
	// final backup (evals.go).
	purpose, err := s.env.Purpose(ctx, selected.ProjectID)
	if err != nil {
		return RemovalResult{}, err
	}
	synthetic := !purpose.KeepsData()
	var final *Backup
	var parts stoppedParts
	var kept []deploy.Left
	if stopped() {
		return RemovalResult{}, fault.New(fault.Conflict, "stopped as asked before its final backup; nothing was changed")
	}
	if err := ask(""); err != nil {
		return RemovalResult{}, err
	}
	if !synthetic {
		fc, err := s.finalBackup(ctx, selected, target, owned, actor.Principal.Name)
		var cannot *portainerUnreachable
		switch {
		case errors.As(err, &cannot) && in.WithoutFinalCopy == selected.Name:
			// The person chose to go without one: the data stays.
			in.noCopy = cannot.why
		case errors.As(err, &cannot):
			return RemovalResult{}, &NoFinalCopy{Deployment: selected.Name, Cause: &deployment.Unavailable{Reason: "cannot-back-up", Why: cannot.why}}
		case err != nil:
			return RemovalResult{}, err
		}
		final, parts, kept = fc.set, fc.parts, fc.kept
	}
	others, err := s.deployments.OthersOnConnector(ctx, selected, target)
	if err != nil {
		return RemovalResult{}, fmt.Errorf("reading the project's other deployments on this account (%s): %w", restartParts(ctx, parts), err)
	}
	if stopped() {
		return RemovalResult{}, fault.New(fault.Conflict, "stopped as asked before its teardown; "+finalDone(final)+restartParts(ctx, parts))
	}
	if err := ask(finalDone(final)); err != nil {
		return RemovalResult{}, fmt.Errorf("%w (%s)", err, restartParts(ctx, parts))
	}
	taken, err := driver.Teardown(ctx, selected.Placement(), selected.ObservedGeneration, deploy.TakeDown{Owned: owned, BackedUp: final != nil, LastUser: others == 0, Synthetic: synthetic})
	// Even a failed/ambiguous provider call can change what inventory observes.
	if s.applied != nil {
		s.applied()
	}
	if err != nil {
		// The serving container stands; so should its parts.
		return RemovalResult{}, &deployment.RemovalIncompleteError{Cause: fmt.Errorf("%w (%s)", err, restartParts(ctx, parts))}
	}
	// Held data off the list is kept, and retained with the rest of what
	// the teardown left, once each: the final copy's account of a volume
	// (whether the set holds it) stands for the teardown's.
	taken.Left = append(withoutLeft(taken.Left, kept), kept...)
	const removedApp = "the app was taken down; its keys and records were not changed: "
	if err := ask(finalDone(final) + removedApp); err != nil {
		return RemovalResult{}, err
	}
	s.releaseDeploymentKeys(ctx, selected, keys, &taken)
	if synthetic && others == 0 {
		s.deleteCopyImages(ctx, selected.ProjectID, &taken)
	}
	result, err := database.Transact(ctx, s.db, func(tx *database.Tx) (RemovalResult, error) {
		retained, err := settleRemovalIn(ctx, tx, in, selected, deployment.RemovalSettlement{Target: target.Name, By: actor.Principal, Taken: taken, FinalBackup: finalBackupID(final), Synthetic: synthetic, NoCopy: in.noCopy, At: s.now()})
		if err != nil {
			return RemovalResult{}, err
		}
		out := RemovalResult{Deleted: taken.Deleted, Retained: retained}
		if final != nil {
			out.FinalBackup = &FinalSet{ID: final.ID, Bytes: final.Bytes}
		}
		return out, nil
	})
	if err != nil {
		return RemovalResult{}, &deployment.RemovalIncompleteError{Cause: err, ProviderAccepted: true}
	}
	return result, nil
}

// selectRemovalIn is the take-down's selection (SelectRemovalIn) under the
// same project lock, refusing while a restored copy of the deployment
// stands, so no copy is left on a target with nothing that answers for it
// (a project's deletion stops here too).
func selectRemovalIn(ctx context.Context, tx *database.Tx, in RemoveDeployment) (*deployment.Deployment, error) {
	dep, err := deployment.SelectRemovalIn(ctx, tx, in.ProjectID, in.Name, in.withProject)
	if err != nil {
		return nil, err
	}
	if err := refuseStandingCopiesIn(ctx, tx, dep); err != nil {
		return nil, err
	}
	return dep, nil
}

// refuseStandingCopiesIn refuses a take-down of dep while a restored copy
// of it stands.
func refuseStandingCopiesIn(ctx context.Context, tx *database.Tx, dep *deployment.Deployment) error {
	if n, err := protectionsql.New(tx.SQL()).StandingRestoredCopies(ctx, dep.ID); err != nil {
		return err
	} else if n > 0 {
		return fmt.Errorf("%w: %d restored copies of %s still stand; take them down from its card first", ErrRestoredCopiesStand, n, dep.Name)
	}
	return nil
}

// settleRemovalIn settles the take-down of the deployment selected before
// the provider was asked, under the project's lock, refusing while a
// restored copy of it stands (selectRemovalIn's rule, asked again).
func settleRemovalIn(ctx context.Context, tx *database.Tx, in RemoveDeployment, selected *deployment.Deployment, s deployment.RemovalSettlement) ([]string, error) {
	if err := deployment.LockProjectIn(ctx, tx, in.ProjectID); err != nil {
		return nil, err
	}
	if err := refuseStandingCopiesIn(ctx, tx, selected); err != nil {
		return nil, err
	}
	return deployment.RemoveRecordedIn(ctx, tx, selected, in.withProject, s)
}

// TakeDownWithProject takes a deployment down because its project is being
// deleted: main too, under the same authority, through the same routing as
// TakeDown. A deployment the take-down operation protects (App Platform data
// AppTown copies, with a store attached and a copy runner named) gets its
// final copy to the store, filled into a new place and checked, before its
// cluster is deleted; the deletion runs that operation here and waits for
// it, and one that does not end verified refuses the deletion with what the
// operation says it left. Anything else is removed now, by Remove: a final
// backup where AppTown backs the data up, then only what AppTown
// recorded creating.
//
// An eval copy is the exception, on purpose: its data is synthetic and
// nothing will restore it, so its deployments go with no final copy on any
// target. beginManagedTakeDown never starts the operation for a copy, and
// Remove takes no final backup of one (evals.go).
func (s *Service) TakeDownWithProject(ctx context.Context, projectID ids.ID, name string) (RemovalResult, error) {
	in := RemoveDeployment{ProjectID: projectID, Name: name, withProject: true}
	op, noCopy, err := s.beginManagedTakeDown(ctx, in)
	if err != nil {
		return RemovalResult{}, err
	}
	if op == nil {
		in.noCopy = noCopy
		return s.Remove(ctx, in)
	}
	return s.awaitTakeDown(ctx, op)
}

// awaitTakeDown runs a project deletion's take-down operation to its end,
// here when it claims the operation first, else by waiting for the
// worker that has it. It runs on a context the caller's cancellation does
// not stop: a take-down stopped halfway leaves the app deleted and the data
// kept, which is safe but leaves the deletion to be asked again for no
// reason.
func (s *Service) awaitTakeDown(ctx context.Context, op *operations.Operation) (RemovalResult, error) {
	done, err := s.ops.RunHere(context.WithoutCancel(ctx), op.ID)
	if err != nil {
		return RemovalResult{}, fmt.Errorf("its take-down (operation %d) was not recorded as it ended: %w", op.ID, err)
	}
	if done.State != operations.Succeeded || done.Verification != operations.Verified {
		return RemovalResult{}, fault.New(fault.Conflict, fmt.Sprintf("its take-down with a final copy (operation %d) ended %s: %s", op.ID, done.State, done.Detail))
	}
	out := RemovalResult{}
	switch deleted := done.Result["deleted"].(type) {
	case []string:
		out.Deleted = deleted
	case []any:
		for _, d := range deleted {
			if text, ok := d.(string); ok {
				out.Deleted = append(out.Deleted, text)
			}
		}
	}
	id := inputID(done.Result, "finalBackup")
	if !id.IsZero() {
		name, _ := done.Input["deployment"].(string)
		final, err := s.backups.ByID(ctx, done.ProjectID, name, id)
		if err != nil {
			return out, fmt.Errorf("its take-down (operation %d) ended verified, but its final copy, set %d, could not be read: %w", op.ID, id, err)
		}
		out.FinalBackup = &FinalSet{ID: final.ID, Bytes: final.Bytes}
	}
	return out, nil
}

func finalBackupID(b *Backup) ids.ID {
	if b == nil {
		return ids.Nil
	}
	return b.ID
}

// finalDone says what a removal refused part way had already done.
func finalDone(final *Backup) string {
	if final == nil {
		return ""
	}
	return fmt.Sprintf("the final backup (set %d) was taken; ", final.ID)
}

// restartParts starts again the parts a final backup stopped, and says
// what happened: started again, or which did not start and why. Nothing
// stopped, it says so.
func restartParts(ctx context.Context, parts stoppedParts) string {
	if len(parts.ids) == 0 {
		return "nothing of it was stopped"
	}
	if failed := parts.restart(ctx); len(failed) > 0 {
		return "these parts did not start again: " + strings.Join(failed, "; ")
	}
	return "its parts were started again"
}

// --- what AppTown itself made for the deployment --------------------------

// splitOwned separates what a target driver deletes from the keys the
// register step made, which AppTown revokes itself.
// withoutLeft is left without the entries kept names again.
func withoutLeft(left, kept []deploy.Left) []deploy.Left {
	names := map[string]bool{}
	for _, k := range kept {
		if name, _ := k.Handle["name"].(string); name != "" {
			names[name] = true
		}
	}
	var out []deploy.Left
	for _, l := range left {
		if name, _ := l.Handle["name"].(string); !names[name] {
			out = append(out, l)
		}
	}
	return out
}

// splitStorage takes a deployment's object storage (its bucket and the
// bucket's key) out of what a take-down hands the target's teardown first:
// they stay until the bucket's final copy has come back.
func splitStorage(owned []deploy.Owned) (rest, storage []deploy.Owned) {
	for _, o := range owned {
		if o.Kind == digitalocean.KindBucket || o.Kind == digitalocean.KindSpacesKey {
			storage = append(storage, o)
		} else {
			rest = append(rest, o)
		}
	}
	return rest, storage
}

func splitOwned(owned []deploy.Owned) (target, keys []deploy.Owned) {
	for _, o := range owned {
		if o.Kind == deployment.KindOpenRouterKey {
			keys = append(keys, o)
		} else {
			target = append(target, o)
		}
	}
	return target, keys
}

// releaseDeploymentKeys revokes a removed deployment's keys, after its
// target let go of everything else: each one revoked is deleted, each one
// that is not is left, with why, for a person to revoke by hand.
func (s *Service) releaseDeploymentKeys(ctx context.Context, dep *deployment.Deployment, keys []deploy.Owned, taken *deploy.TakenDown) {
	for _, o := range keys {
		held, ok := integration.ParseHeldKey(o.ID)
		var err error
		switch {
		case !ok:
			err = fmt.Errorf("the record %q does not name a key", o.ID)
		case s.integrations == nil:
			err = errors.New("this AppTown has no integrations to revoke it with")
		default:
			err = s.integrations.ReleaseLLMKey(ctx, held, fmt.Sprintf("llm:deployment:%d", dep.ID))
		}
		if err == nil {
			taken.Deleted = append(taken.Deleted, "AI models key "+o.Name)
			continue
		}
		taken.Left = append(taken.Left, deploy.Left{What: "AI models key", Why: err.Error(),
			Handle: map[string]any{"name": o.Name, "kind": deployment.KindOpenRouterKey, "connector": held.Connector, "hash": held.Hash}})
	}
}

// deleteCopyImages deletes, with an eval copy's last deployment, the
// release images AppTown published under the copy's own repository: its
// digest bundles' images there, and nothing elsewhere. What cannot be
// deleted is reported left, never hidden.
func (s *Service) deleteCopyImages(ctx context.Context, projectID ids.ID, taken *deploy.TakenDown) {
	images, err := s.bundles.DigestImages(ctx, projectID)
	if err != nil || len(images) == 0 {
		if err != nil {
			taken.Left = append(taken.Left, deploy.Left{What: "the copy's release images", Why: "not known which were published: " + err.Error()})
		}
		return
	}
	repo, err := s.env.ReleaseRepository(ctx, projectID)
	if err != nil {
		taken.Left = append(taken.Left, deploy.Left{What: "the copy's release images", Why: "its repository could not be read: " + err.Error()})
		return
	}
	for _, ref := range images {
		name, digest, ok := strings.Cut(ref, "@")
		if !ok || name != repo.Name {
			continue
		}
		if err := oci.Delete(ctx, repo, digest); err != nil {
			taken.Left = append(taken.Left, deploy.Left{What: "image " + ref, Handle: map[string]any{"kind": "image", "name": ref}, Why: err.Error()})
			continue
		}
		taken.Deleted = append(taken.Deleted, "image "+ref)
	}
}
