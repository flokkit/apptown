package dataprotection

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/portainer"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/project"
)

// What a standing deployment on a Docker host holds
// (design/subsystems/deployment.md, "Backups and restore"): every data volume
// on its list plus every data volume its running release declares, each
// looked up on its host. The project's head plays no part: a deployment on
// an older release, or one still running a need its newer release dropped,
// holds that data all the same.
//
// A copy belongs to one volume. Each item names the volume it copies and its
// file is named for that volume, so two volumes of one role (the old and new
// app volumes after a slug change, say) are never one file, and evidence and
// restore follow the volume (placeCopies).

// ErrNothingToBackUp is a deployment whose list records no data volume and
// whose running release declares none: a scheduled backup of it is
// skipped, not failed. It is never said when either could not be read.
var ErrNothingToBackUp = errors.New("the deployment holds no data: its list records no data volume and its release declares none — there is nothing to back up")

// ErrDataNotKnown is a deployment whose data could not be read: its
// release, its list or its host. Nothing is said to be held or not held.
var ErrDataNotKnown = fault.New(fault.Unavailable, "what this deployment holds is not known")

// ErrHeldDataAbsent is a deployment whose list or release names data
// none of which is on its host: nothing could be copied, which is not the
// same as holding nothing.
var ErrHeldDataAbsent = fault.New(fault.Conflict, "the deployment's list or release names data, but none of it is on its host; nothing was copied")

// heldItems is what a deployment holds: on a Docker host with the
// release contract it runs and the host's scope it was read through.
type heldItems struct {
	items    []backupPlanItem
	contract *appcontract.Contract
	dk       docker.Scope
	// layout is a Portainer deployment's stack, which says its writers.
	layout *portainer.StackLayout
}

// heldData is what a standing deployment holds, on every target whose data
// AppTown copies, as one list of items: on a Docker host each data volume
// its list and its release name, reached in its container (heldOnHost); on
// a managed target each recorded managed instance, reached from the copy
// runner (heldManaged). An item that cannot be copied is in the list with
// why. ErrNothingToBackUp when the deployment names no data;
// ErrDataNotKnown when what it holds could not be read.
func (s *core) heldData(ctx context.Context, dep *deployment.Deployment, t *deployment.Target) (heldItems, error) {
	switch {
	case t.DataAccess() == deployment.DataThroughPortainer:
		dk, err := s.providers.PortainerDataScope(ctx, t)
		if err != nil {
			return heldItems{}, fmt.Errorf("%w: %w", ErrDataNotKnown, err)
		}
		p, err := project.ByIDIn(ctx, s.db, dep.ProjectID)
		if err != nil {
			return heldItems{}, fmt.Errorf("%w: reading the project: %v", ErrDataNotKnown, err)
		}
		layout, err := s.portainerLayout(ctx, t, dep, p.Slug)
		if err != nil {
			return heldItems{}, fmt.Errorf("%w: %v", ErrDataNotKnown, err)
		}
		held, err := s.heldOnPortainer(ctx, dk, layout, dep)
		held.dk, held.layout = dk, &layout
		return held, err
	case t.DataAccess() == deployment.DataOnDockerHost:
		dk, err := s.hosts.DockerScope(ctx, t)
		if err != nil {
			// Not known, and why: an AppTown with no local Docker refuses by name.
			return heldItems{}, fmt.Errorf("%w: %w", ErrDataNotKnown, err)
		}
		p, err := project.ByIDIn(ctx, s.db, dep.ProjectID)
		if err != nil {
			return heldItems{}, fmt.Errorf("%w: reading the project: %v", ErrDataNotKnown, err)
		}
		held, err := s.heldOnHost(ctx, dk, p.Slug, dep)
		held.dk = dk
		return held, err
	case managedProvider(t) != "":
		return s.heldManaged(ctx, dep, t)
	}
	return heldItems{}, fmt.Errorf("AppTown copies no data of deployments on %s", t.Name)
}

// heldOnHost plans a copy of each data volume a deployment on a Docker host
// holds. An item that cannot be copied is planned with why (Unresolved),
// so the set that takes it reads partial. ErrNothingToBackUp only when both
// sources name no data; ErrDataNotKnown when either, or the host, could not
// be read.
func (s *core) heldOnHost(ctx context.Context, dk docker.Scope, slug string, dep *deployment.Deployment) (heldItems, error) {
	unknown := func(format string, a ...any) (heldItems, error) {
		return heldItems{}, fmt.Errorf("%w: %s", ErrDataNotKnown, fmt.Sprintf(format, a...))
	}
	owned, err := s.owned.Owned(ctx, dep.ID)
	if err != nil {
		return unknown("reading its list: %v", err)
	}
	c, err := s.releaseContract(ctx, dep)
	if err != nil {
		return unknown("%v", err)
	}
	items, named, err := s.heldFrom(ctx, dk, slug, dep, owned, c)
	switch {
	case err != nil:
		return unknown("%v", err)
	case !named:
		return heldItems{}, ErrNothingToBackUp
	case len(items) == 0:
		// Data its sources name, none of it on the host, is not "nothing to
		// back up": nothing was there to copy, and that is said.
		return heldItems{}, ErrHeldDataAbsent
	}
	if err := oneFileEach(items); err != nil {
		return heldItems{}, err
	}
	return heldItems{items: items, contract: c}, nil
}

// releaseContract is the .apptown/app.yaml of the release the deployment
// runs (its recorded revision), read through the release sources.
func (s *core) releaseContract(ctx context.Context, dep *deployment.Deployment) (*appcontract.Contract, error) {
	if dep.ReleaseSHA == "" {
		return nil, fmt.Errorf("no release revision is recorded for %s; release it again", dep.Name)
	}
	if s.sources == nil {
		return nil, errors.New("release sources are not configured")
	}
	proj, err := s.sources.Project(ctx, dep.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("reading the project: %v", err)
	}
	c, _, err := s.sources.Contract(ctx, proj, dep.ReleaseSHA)
	if err != nil || c == nil {
		return nil, fmt.Errorf("reading .apptown/app.yaml at its release %s: %v", dep.ReleaseSHA, err)
	}
	return c, nil
}

// heldFrom plans each data volume on the list and, when c is the release's
// contract (nil when it was not read), each the release declares, merged
// by volume and each asked of the host. named says whether either source
// names data. An error is a host that could not be asked.
func (s *core) heldFrom(ctx context.Context, dk docker.Scope, slug string, dep *deployment.Deployment, owned []deploy.Owned, c *appcontract.Contract) ([]backupPlanItem, bool, error) {
	stackID := docker.StackID(slug, dep.Name)
	named := false
	type want struct {
		need   string // "" is the app's own data
		volume string
		listed bool
	}
	var wants []want
	for _, o := range owned {
		if o.Kind == "volume" && o.Data {
			named = true
			wants = append(wants, want{need: o.Need, volume: o.ID, listed: true})
		}
	}
	var unresolved []backupPlanItem
	if c != nil {
		if pub := c.PublicPart(); pub != nil && pub.DataDir != "" {
			named = true
			wants = append(wants, want{volume: docker.DataVolumeName(docker.ContainerName(slug, dep.Name))})
		}
		for _, need := range c.Needs {
			d, err := s.catalog.ByCapability(ctx, need)
			if err != nil {
				// A need whose kind cannot be read may keep data: the set says
				// it was not copied rather than counting the rest as complete.
				// It names what the host answered for: its volume, else its
				// container, else nothing is there.
				named = true
				it, there, herr := unreadNeed(ctx, dk, stackID, need, unresolvedNeed(need, err))
				if herr != nil {
					return nil, named, herr
				}
				if there {
					unresolved = append(unresolved, it)
				}
				continue
			}
			if d == nil || d.Local.Data == "" {
				continue
			}
			named = true
			wants = append(wants, want{need: need, volume: docker.NeedVolumeName(stackID, need)})
		}
	}

	var items []backupPlanItem
	// Items are one per volume; an item with no volume (a need whose data
	// is in no volume AppTown can name) is one per need, never merged on
	// the empty identity.
	seen := map[string]bool{}
	key := func(it backupPlanItem) string {
		if it.Volume == "" {
			return "need:" + it.Name
		}
		return it.Volume
	}
	add := func(it backupPlanItem) {
		seen[key(it)] = true
		items = append(items, it)
	}
	for _, w := range wants {
		if seen[w.volume] {
			continue
		}
		there, err := dk.Exists(ctx, "volume", "inspect", w.volume)
		if err != nil {
			return nil, named, fmt.Errorf("not known whether the data volume %s is there: %v", w.volume, err)
		}
		if !there {
			if w.listed || w.need == "" {
				continue // the daemon says it is gone, or the app never made it: nothing held
			}
			// A need made before needs had named volumes keeps its data in
			// an anonymous volume of its service container: its real
			// identity is read from the mount, so a list entry for it is
			// never planned twice.
			it, held, err := s.anonymousNeed(ctx, dk, stackID, w.need)
			if err != nil {
				return nil, named, err
			}
			if held && !seen[key(it)] {
				add(it)
			}
			continue
		}
		if w.need == "" {
			it := appVolumeItem(s.env.ChownImage(), w.volume)
			it.Listed = w.listed
			add(it)
			continue
		}
		it, why, err := s.needCopy(ctx, dk, needCandidates(owned, stackID, w.need), deploy.Owned{Kind: "volume", ID: w.volume, Data: true, Need: w.need})
		if err != nil {
			return nil, named, err
		}
		if why != "" {
			it = backupPlanItem{Kind: "need", Name: w.need, Volume: w.volume, File: w.volume + ".uncopied", Unresolved: "not copied: " + why}
		}
		it.Listed = w.listed
		add(volumeFile(it))
	}
	for _, it := range unresolved {
		if !seen[key(it)] {
			add(it)
		}
	}
	return items, named, nil
}

// oneFileEach refuses a plan in which two items would be written to one
// file: one copy would be written over another.
func oneFileEach(items []backupPlanItem) error {
	files := map[string]string{}
	for _, it := range items {
		if other, taken := files[it.File]; taken {
			return fmt.Errorf("the copies of %s and %s would both be written to %s; nothing was taken", other, it.Volume, it.File)
		}
		files[it.File] = it.Volume
	}
	return nil
}

// unreadNeed is a declared need whose kind cannot be read, as the host
// holds it: an item for its named volume when that is there, else for its
// service container when that is there (Volume ""), else none. An item's
// Volume is always one the host answered for.
func unreadNeed(ctx context.Context, dk docker.Scope, stackID, need, why string) (backupPlanItem, bool, error) {
	vol := docker.NeedVolumeName(stackID, need)
	there, err := dk.Exists(ctx, "volume", "inspect", vol)
	if err != nil {
		return backupPlanItem{}, false, fmt.Errorf("not known whether the data volume %s is there: %v", vol, err)
	}
	if there {
		return backupPlanItem{Kind: "need", Name: need, Volume: vol, File: vol + ".unread", Unresolved: why}, true, nil
	}
	svc := docker.ServiceName(stackID, need)
	if there, err = dk.Exists(ctx, "inspect", svc); err != nil {
		return backupPlanItem{}, false, fmt.Errorf("not known whether %s is there: %v", svc, err)
	}
	return backupPlanItem{Kind: "need", Name: need, Container: svc, File: svc + ".unread", Unresolved: why}, there, nil
}

// appVolumeItem copies the app's data volume while the app runs.
func appVolumeItem(chownImage, vol string) backupPlanItem {
	return backupPlanItem{Kind: "volume", Name: vol, Volume: vol, File: vol + ".tar.gz", How: "tar of the volume " + vol, Copy: CopyLiveFiles,
		Step: docker.Step{Image: chownImage, Options: []string{"--entrypoint", "", "--network", "none", "-v", vol + ":/src:ro"}, Argv: []string{"tar", "cz", "-C", "/src", "."}}}
}

// volumeFile names a need item's file for the volume it copies, keeping the
// kind's extension.
func volumeFile(it backupPlanItem) backupPlanItem {
	if it.Volume != "" && it.Unresolved == "" {
		it.File = it.Volume + strings.TrimPrefix(it.File, it.Name)
	}
	return it
}

// needCandidates are the containers that may hold a need's data: the need
// containers on the list and the running release's service container.
func needCandidates(owned []deploy.Owned, stackID, need string) []deploy.Owned {
	svc := docker.ServiceName(stackID, need)
	out := []deploy.Owned{}
	for _, o := range owned {
		if o.Kind == "container" && o.Need == need {
			out = append(out, o)
		}
	}
	for _, o := range out {
		if o.ID == svc || o.Name == svc {
			return out
		}
	}
	return append(out, deploy.Owned{Kind: "container", ID: svc, Name: svc, Need: need})
}

// anonymousNeed is a declared need whose named volume is absent: its
// service container, if there, copied where it runs and bound to the
// volume it mounts at the kind's data path. Neither container nor volume
// holds nothing.
func (s *core) anonymousNeed(ctx context.Context, dk docker.Scope, stackID, need string) (backupPlanItem, bool, error) {
	svc := docker.ServiceName(stackID, need)
	there, err := dk.Exists(ctx, "inspect", svc)
	if err != nil {
		return backupPlanItem{}, false, fmt.Errorf("not known whether %s is there: %v", svc, err)
	}
	if !there {
		return backupPlanItem{}, false, nil
	}
	d, err := s.catalog.ByCapability(ctx, need)
	if err != nil {
		return backupPlanItem{}, false, err
	}
	vol, err := docker.VolumeAt(ctx, dk, svc, d.Local.Data)
	if err != nil {
		return backupPlanItem{}, false, fmt.Errorf("reading what %s mounts: %v", svc, err)
	}
	if vol == "" {
		// Its data is in no volume AppTown can name (the container's own
		// filesystem, or a mount at a path the kind no longer declares): an
		// item not copied, never a copy of an empty directory, and never
		// merged with another on an empty identity.
		return backupPlanItem{Kind: "need", Name: need, Container: svc, File: svc + ".unidentified",
			Unresolved: fmt.Sprintf("not copied: %s mounts no volume at %s, where the %s kind keeps its data, so which data is the need's is not known", svc, d.Local.Data, need)}, true, nil
	}
	it := needItem(d, need, svc)
	it.Volume = vol
	return volumeFile(it), true, nil
}

// copyRole is what an item holds, whichever volume: the app's data, or one
// need's.
func copyRole(kind, name string) string {
	if kind == "volume" {
		return "volume"
	}
	return kind + ":" + name
}

// copyVolume is the volume a recorded copy was taken from. An app-volume
// copy from before items named their volume names it as the item.
func copyVolume(it BackupItem) string {
	if it.Volume == "" && it.Kind == "volume" {
		return it.Name
	}
	return it.Volume
}

// placeCopies puts each good copy of a set onto the held item it belongs
// to: the item holding the volume it was taken from; or, when that volume
// is not here (an older set, another deployment's), the one item of its
// role, only when the set holds one copy of that role and the deployment
// one item of it. Nothing is chosen by order. into maps a copy's index in
// items to a held item's index; unplaced names each good copy with no
// certain place.
func placeCopies(items []BackupItem, held []backupPlanItem) (into map[int]int, unplaced []string) {
	into = map[int]int{}
	taken := map[int]bool{}
	setRoles, heldRoles := map[string]int{}, map[string]int{}
	for _, it := range items {
		setRoles[copyRole(it.Kind, it.Name)]++
	}
	for _, p := range held {
		heldRoles[copyRole(p.Kind, p.Name)]++
	}
	var byRole []int
	for i, it := range items {
		if it.Error != "" {
			continue
		}
		v := copyVolume(it)
		found := -1
		for j, p := range held {
			if v != "" && p.Kind == it.Kind && p.Volume == v && !taken[j] {
				found = j
				break
			}
		}
		if found < 0 {
			byRole = append(byRole, i)
			continue
		}
		into[i], taken[found] = found, true
	}
	for _, i := range byRole {
		it := items[i]
		role := copyRole(it.Kind, it.Name)
		found := -1
		if setRoles[role] == 1 && heldRoles[role] == 1 {
			for j, p := range held {
				if copyRole(p.Kind, p.Name) == role && !taken[j] {
					found = j
				}
			}
		}
		if found < 0 {
			what := it.Name
			if it.Kind == "volume" {
				what = "the app's data"
			}
			if v := copyVolume(it); v != "" {
				what += " (volume " + v + ")"
			}
			switch {
			case heldRoles[role] == 0 && it.Kind == "volume":
				what += ": this deployment keeps no app data (declare a dataDir)"
			case heldRoles[role] == 0:
				what += ": this deployment has no such need"
			default:
				what += ": not certain which of this deployment's volumes it belongs in"
			}
			unplaced = append(unplaced, what)
			continue
		}
		into[i], taken[found] = found, true
	}
	return into, unplaced
}
