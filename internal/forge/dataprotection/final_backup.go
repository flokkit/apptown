package dataprotection

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/portainer"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/project"
)

// The final backup (design/subsystems/deployment.md, "Removing a
// deployment"):
// before a take-down deletes a deployment's data, the forge copies exactly
// that data, from the deployment's own list, not from the contract at the
// project's head. Every application part is stopped first so nothing writes
// after the copy; only the need containers run, for their copy commands.
// The data is deleted only when the set is complete: every data volume still
// on the host has an item taken without error. Otherwise the parts are
// started again and nothing is removed.

// TriggerTakeDown names a take-down's final set. Final sets are not counted
// in BackupKeep: their deployment takes no more sets to push them out. A
// project's sets, final ones included, belong to the project: they are
// kept while it is, and go when it is purged (PurgeProjectBackups).
const TriggerTakeDown = "take-down"

// ErrFinalBackupIncomplete refuses a take-down whose final set does not
// cover the deployment's data. Nothing was removed.
var ErrFinalBackupIncomplete = fault.New(fault.Conflict, "the final backup does not cover the deployment's data; nothing was removed")

// stoppedParts are the application containers a take-down stopped, which
// a refusal starts again.
type stoppedParts struct {
	dk  docker.Scope
	ids []string
	// idle are the parts that are there and were not running: left as
	// they were, never started.
	idle []string
}

func (p stoppedParts) restart(ctx context.Context) []string {
	var failed []string
	for _, id := range p.ids {
		if out, err := p.dk.Run(ctx, "start", id); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %s", id, strings.TrimSpace(string(out))))
		}
	}
	return failed
}

// finalCopy is a take-down's final set, the parts it stopped (which a
// failed teardown starts again), and the data it keeps: volumes the
// deployment holds that are not on its list, which nothing deletes.
type finalCopy struct {
	set   *Backup
	parts stoppedParts
	kept  []deploy.Left
}

// finalBackup takes the final set for a take-down, or answers no set when
// there is nothing to take: no data held, or a target that keeps no forge
// backups (its data is then kept, and the teardown says so). It plans with
// the held-data planner before stopping anything, so a take-down that
// cannot cover the data on its list refuses having changed nothing.
func (s *Service) finalBackup(ctx context.Context, dep *deployment.Deployment, t *deployment.Target, owned []deploy.Owned, actor string) (finalCopy, error) {
	if t.HoldsBackups() != nil {
		return finalCopy{}, nil
	}
	p, err := project.ByIDIn(ctx, s.db, dep.ProjectID)
	if err != nil {
		return finalCopy{}, fmt.Errorf("reading the project: %w", err)
	}
	if t.DataAccess() == deployment.DataThroughPortainer {
		return s.finalBackupOnPortainer(ctx, dep, t, actor)
	}
	dk, err := s.hosts.DockerScope(ctx, t)
	if err != nil {
		return finalCopy{}, err
	}
	// Teardown deletes every need container on the list; one holding data
	// in its own files, or whose data place cannot be read, refuses here,
	// whatever the release says, before anything stops.
	if why, err := containerHeldData(ctx, dk, s.catalog, owned); err != nil || why != "" {
		if err != nil {
			why = err.Error()
		}
		return finalCopy{}, fmt.Errorf("%w: %s", ErrFinalBackupIncomplete, why)
	}
	// The release names data the list may lack. Unread, the list alone is
	// planned, as before: nothing off the list is ever deleted.
	c, _ := s.releaseContract(ctx, dep)
	plan, _, err := s.heldFrom(ctx, dk, p.Slug, dep, owned, c)
	if err != nil {
		return finalCopy{}, fmt.Errorf("%w: %s", ErrFinalBackupIncomplete, err)
	}
	if len(plan) == 0 {
		return finalCopy{}, nil // no data held, or the data on the list is already gone
	}
	var uncovered []string
	for i := range plan {
		it := &plan[i]
		if it.Listed && it.Unresolved != "" {
			uncovered = append(uncovered, strings.TrimPrefix(it.Unresolved, "not copied: "))
		}
		if it.Listed && it.Kind == "volume" {
			// Every application part is stopped before the copy: nothing writes.
			it.Copy, it.How = CopyFiles, "tar of the volume "+it.Volume
		}
	}
	if len(uncovered) > 0 {
		return finalCopy{}, fmt.Errorf("%w: the forge has no way to copy %s", ErrFinalBackupIncomplete, strings.Join(uncovered, "; "))
	}
	if err := oneFileEach(plan); err != nil {
		return finalCopy{}, fmt.Errorf("%w: %s", ErrFinalBackupIncomplete, err)
	}
	stopped, err := stopParts(ctx, dk, owned)
	refuse := func(why string) (finalCopy, error) {
		if failed := stopped.restart(ctx); len(failed) > 0 {
			why += "; these could not be started again, so the deployment is not serving: " + strings.Join(failed, "; ")
		}
		return finalCopy{}, fmt.Errorf("%w: %s", ErrFinalBackupIncomplete, why)
	}
	if err != nil {
		return refuse(err.Error())
	}
	b, err := s.takeSet(ctx, dk, dep.ProjectID, p.Slug, dep.Name, plan, TriggerTakeDown, actor)
	if err != nil {
		return refuse(err.Error())
	}
	if why := b.uncovered(plan); why != "" {
		return refuse(fmt.Sprintf("set %d is %s: %s", b.ID, b.Status, why))
	}
	return finalCopy{set: b, parts: stopped, kept: keptOffList(b, plan)}, nil
}

// finalBackupOnPortainer is the final set of a deployment in a Portainer
// environment, by the Docker host's rule: planned before anything stops
// (a listed volume the forge has no way to copy refuses, having changed
// nothing); the stack's running writers stopped; the set taken; and only a
// set holding a good copy of every listed volume lets the data go. A volume
// the stack declares that is not on the list is copied into the set and
// kept. An environment the forge cannot reach for data refuses with why.
func (s *Service) finalBackupOnPortainer(ctx context.Context, dep *deployment.Deployment, t *deployment.Target, actor string) (finalCopy, error) {
	held, err := s.heldData(ctx, dep, t)
	switch {
	case errors.Is(err, ErrNothingToBackUp), errors.Is(err, ErrHeldDataAbsent):
		return finalCopy{}, nil
	case errors.Is(err, portainer.ErrCannotBackUp):
		return finalCopy{}, &portainerUnreachable{why: err.Error()}
	case err != nil:
		return finalCopy{}, err
	}
	plan := held.items
	var uncovered []string
	for i := range plan {
		it := &plan[i]
		if it.Listed && it.Unresolved != "" {
			uncovered = append(uncovered, strings.TrimPrefix(it.Unresolved, "not copied: "))
		}
		if it.Kind == "volume" {
			// Every writer is stopped before the copy: nothing writes.
			it.Copy, it.How = CopyFiles, "tar of the volume "+it.Volume
		}
	}
	if len(uncovered) > 0 {
		return finalCopy{}, fmt.Errorf("%w: the forge has no way to copy %s", ErrFinalBackupIncomplete, strings.Join(uncovered, "; "))
	}
	writers, err := s.stackWriters(ctx, held.dk, *held.layout)
	if err != nil {
		return finalCopy{}, fmt.Errorf("%w: %s", ErrFinalBackupIncomplete, err)
	}
	stopped, err := stopParts(ctx, held.dk, writers)
	refuse := func(why string) (finalCopy, error) {
		if failed := stopped.restart(ctx); len(failed) > 0 {
			why += "; these could not be started again, so the deployment is not serving: " + strings.Join(failed, "; ")
		}
		return finalCopy{}, fmt.Errorf("%w: %s", ErrFinalBackupIncomplete, why)
	}
	if err != nil {
		return refuse(err.Error())
	}
	p, err := project.ByIDIn(ctx, s.db, dep.ProjectID)
	if err != nil {
		return refuse("reading the project: " + err.Error())
	}
	b, err := s.takeSet(ctx, held.dk, dep.ProjectID, p.Slug, dep.Name, plan, TriggerTakeDown, actor)
	if err != nil {
		return refuse(err.Error())
	}
	if why := b.uncovered(plan); why != "" {
		return refuse(fmt.Sprintf("set %d is %s: %s", b.ID, b.Status, why))
	}
	return finalCopy{set: b, parts: stopped, kept: keptOffList(b, plan)}, nil
}

// portainerUnreachable is a final copy refused because the forge cannot
// reach the deployment's Portainer environment for data: the take-down then
// asks for a person's choice to go without one (NoFinalCopy).
type portainerUnreachable struct{ why string }

func (e *portainerUnreachable) Error() string { return e.why }

// containerHeldData says which need containers on the list keep data in
// their own files, which deleting the container would delete uncopied:
// those with no volume where their kind keeps data, or whose kind cannot be
// read. Data in a volume outlives its container (teardown removes
// containers without their volumes). An error is a daemon that could not
// be asked.
func containerHeldData(ctx context.Context, dk docker.Scope, kinds *dependency.Catalog, owned []deploy.Owned) (string, error) {
	var held []string
	for _, o := range owned {
		if o.Kind != "container" || o.Need == "" {
			continue
		}
		there, err := dk.Exists(ctx, "inspect", o.ID)
		if err != nil {
			return "", fmt.Errorf("not known whether %s is there: %v", o.Label(), err)
		}
		if !there {
			continue
		}
		d, err := kinds.ByCapability(ctx, o.Need)
		if err != nil {
			held = append(held, fmt.Sprintf("%s (%s, so where it keeps data is not known)", o.Label(), unresolvedNeed(o.Need, err)))
			continue
		}
		if d == nil || d.Local.Data == "" {
			continue
		}
		vol, err := docker.VolumeAt(ctx, dk, o.ID, d.Local.Data)
		if err != nil {
			return "", fmt.Errorf("reading what %s mounts: %v", o.Label(), err)
		}
		if vol == "" {
			held = append(held, fmt.Sprintf("%s (it keeps its %s data in its own files at %s, which deleting it would delete)", o.Label(), o.Need, d.Local.Data))
		}
	}
	if len(held) == 0 {
		return "", nil
	}
	return "the forge has no way to copy the data in " + strings.Join(held, "; "), nil
}

// keptOffList is each held volume not on the list: nothing deletes it, so
// it is kept, retained with why and whether the final set holds a copy.
func keptOffList(b *Backup, plan []backupPlanItem) []deploy.Left {
	var out []deploy.Left
	for _, p := range plan {
		if p.Listed {
			continue
		}
		why := "kept: not on the deployment's list, so the forge did not delete it; "
		if c := b.copyOf(p); c != nil {
			why += fmt.Sprintf("final set %d holds a copy", b.ID)
		} else {
			why += fmt.Sprintf("final set %d holds no copy of it", b.ID)
			for _, it := range b.Items {
				if it.Kind == p.Kind && it.Name == p.Name && it.Volume == p.Volume && it.Error != "" {
					why += ": " + it.Error
				}
			}
		}
		need := ""
		if p.Kind == "need" {
			need = p.Name
		}
		if p.Unresolved != "" {
			why += "; " + strings.TrimPrefix(p.Unresolved, "not copied: ")
		}
		// It is named as what the host answered for: its volume, else the
		// container whose own files hold it.
		kind, id := "volume", p.Volume
		if id == "" {
			kind, id = "container", p.Container
		}
		out = append(out, deploy.Left{What: kind + " " + id, Data: true,
			Handle: map[string]any{"kind": kind, "name": id, "id": id, "need": need}, Why: why})
	}
	return out
}

// copyOf is the set's good copy of exactly the planned item's volume.
func (b *Backup) copyOf(p backupPlanItem) *BackupItem {
	for i := range b.Items {
		it := &b.Items[i]
		if it.Kind == p.Kind && it.Name == p.Name && it.Volume == p.Volume && it.Error == "" {
			return it
		}
	}
	return nil
}

// uncovered names the listed items the set holds no good copy of, ""
// when every one is covered: those are what a take-down deletes. A copy
// counts only for the volume it was taken from.
func (b *Backup) uncovered(plan []backupPlanItem) string {
	var missing []string
	for _, p := range plan {
		if p.Listed && b.copyOf(p) == nil {
			missing = append(missing, p.Kind+" "+p.Name+" from volume "+p.Volume)
		}
	}
	return strings.Join(missing, ", ")
}

// stopParts stops every application container on the list that is
// running: the public part, the other parts and any sidecar; restart starts
// exactly those again, so a completed init container or a worker someone
// stopped stays as it was. Need containers keep running for their copy
// commands. A container the daemon cannot be asked about refuses: it may be
// running and writing.
func stopParts(ctx context.Context, dk docker.Scope, owned []deploy.Owned) (stoppedParts, error) {
	out := stoppedParts{dk: dk}
	for _, o := range owned {
		if o.Kind != "container" || o.Need != "" || slices.Contains(out.ids, o.ID) {
			continue
		}
		there, running, err := containerState(ctx, dk, o.ID)
		if err != nil {
			return out, fmt.Errorf("not known whether %s is running: %v", o.Label(), err)
		}
		if !running {
			if there {
				out.idle = append(out.idle, o.ID)
			}
			continue
		}
		if res, err := dk.Run(ctx, "stop", o.ID); err != nil {
			return out, fmt.Errorf("stopping %s: %s", o.Label(), strings.TrimSpace(string(res)))
		}
		out.ids = append(out.ids, o.ID)
	}
	return out, nil
}

// containerState asks the daemon whether a container is there and whether
// it runs; only the daemon's own "no such" answer is absence.
func containerState(ctx context.Context, dk docker.Scope, id string) (there, running bool, err error) {
	if there, err = dk.Exists(ctx, "inspect", id); err != nil || !there {
		return false, false, err
	}
	out, err := dk.Run(ctx, "inspect", "-f", "{{.State.Running}}", id)
	if err != nil {
		return true, false, fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return true, strings.TrimSpace(string(out)) == "true", nil
}

// needCopy copies a need's data volume with the need's declared command,
// run in the need's container on the list that mounts exactly that volume
// where the need keeps its data. A volume no such container mounts is
// named uncovered: a dump of the need's current database is not a copy of
// another volume.
func (s *core) needCopy(ctx context.Context, dk docker.Scope, owned []deploy.Owned, vol deploy.Owned) (backupPlanItem, string, error) {
	d, err := s.catalog.ByCapability(ctx, vol.Need)
	if err != nil {
		return backupPlanItem{}, fmt.Sprintf("the %s data in volume %s (%s)", vol.Need, vol.ID, unresolvedNeed(vol.Need, err)), nil
	}
	if d.Local.Data == "" {
		return backupPlanItem{}, fmt.Sprintf("the %s data in volume %s (the catalog declares no copy for it)", vol.Need, vol.ID), nil
	}
	container := ""
	for _, o := range owned {
		if o.Kind != "container" || o.Need != vol.Need {
			continue
		}
		there, err := dk.Exists(ctx, "inspect", o.ID)
		if err != nil {
			return backupPlanItem{}, "", fmt.Errorf("not known whether %s is there: %v", o.Label(), err)
		}
		if !there {
			continue
		}
		mounted, err := docker.VolumeAt(ctx, dk, o.ID, d.Local.Data)
		if err != nil {
			return backupPlanItem{}, "", fmt.Errorf("reading what %s mounts: %v", o.Label(), err)
		}
		if mounted == vol.ID {
			container = o.ID
		}
	}
	if container == "" {
		return backupPlanItem{}, fmt.Sprintf("the %s data in volume %s (no running %s container on the list mounts it)", vol.Need, vol.ID, vol.Need), nil
	}
	it := needItem(d, vol.Need, container)
	it.Volume = vol.ID
	return it, "", nil
}
