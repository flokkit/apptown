package docker

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/flokkit/apptown/internal/dockerlabel"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/strs"
)

// What a standing deployment's boot creates on a Docker host is recorded as
// it is created (deploy.Owned), so its take-down deletes exactly that: the
// containers its own runs started, a network only when its create made it,
// the app's data volume only when the boot created it, each need's data
// volume as the need's container was made with it, and the release images
// it pulled or built. What already existed is not recorded, and so is never
// deleted.

// Ownership is how a standing boot records what it creates. A nil Record
// records nothing: previews and runs are cleaned up by their own owners.
// Needs names the need services and where each keeps its data ("" keeps
// none).
type Ownership struct {
	// storage records engine identity separately from deletion ownership.
	storage        deploy.StorageBindings
	restoreStorage deploy.StorageRestore
	Record         func(deploy.Owned) error
	Needs          map[string]string
	// priorResources is the deployment's list as it stood before this boot: what
	// AppTown may delete (a synthetic need's volume, when its parameters
	// moved). Nothing else is ever deleted.
	priorResources []deploy.Owned
	// persistent is a standing deployment's stack: its parts live with it
	// (class env, restarted with the host), never idle-reaped.
	persistent bool
	// info is the informational labels a standing deployment's things carry
	// for people (dockerlabel.Informational); nil for previews and runs.
	info []string
}

func (o Ownership) recorded(kind, id string) bool {
	for _, r := range o.priorResources {
		if r.Kind == kind && r.ID == id {
			return true
		}
	}
	return false
}

func (o Ownership) created(r deploy.Owned) error {
	if o.Record == nil {
		return nil
	}
	if err := o.Record(r); err != nil {
		return fmt.Errorf("recording the %s AppTown created: %w", r.Label(), err)
	}
	return nil
}

var containerID = regexp.MustCompile(`^[a-f0-9]{64}$`)

// createdID is the identity of a container a `docker run -d` or `docker
// create` just made: the ID it printed last. A name is not an identity (a
// later container can take it), so output without an ID is an error: what
// was created is not known, and nothing is recorded that a take-down would
// remove.
func createdID(out []byte, name string) (string, error) {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); containerID.MatchString(last) {
		return last, nil
	}
	return "", fmt.Errorf("docker printed no container ID for %s, so what it created is not known and is not recorded: %s", name, strs.Truncate(strings.TrimSpace(string(out)), 200))
}

// ensureNetwork makes a stack's network when it is missing, recording it
// only when this create made it.
func ensureNetwork(ctx context.Context, dk Scope, network string, own Ownership) error {
	if dk.OK(ctx, "network", "inspect", network) {
		return nil
	}
	if out, err := dk.Run(ctx, append(append(append([]string{"network", "create"}, dk.instanceLabel()...), own.info...), network)...); err != nil {
		if dk.OK(ctx, "network", "inspect", network) {
			return nil // another boot made it: not ours to record
		}
		return fmt.Errorf("network create: %v: %s", err, out)
	}
	return own.created(deploy.Owned{Kind: "network", ID: network})
}

// recordService records a standing service's container, naming the need
// it serves.
func recordService(out []byte, cname, service string, own Ownership) error {
	if own.Record == nil {
		return nil
	}
	need := ""
	if _, isNeed := own.Needs[service]; isNeed {
		need = service
	}
	id, err := createdID(out, cname)
	if err != nil {
		return err
	}
	return own.created(deploy.Owned{Kind: "container", ID: id, Name: cname, Need: need})
}

// ensureVolume makes a standing deployment's named data volume before a
// container mounts it, labelled with its stack, and records it only when
// this create made it. need names the need whose data it holds ("" is the
// app's own).
func ensureVolume(ctx context.Context, dk Scope, volume, stackID, need string, own Ownership) error {
	exists, err := dk.Exists(ctx, "volume", "inspect", volume)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return createVolume(ctx, dk, volume, stackID, need, own)
}

// createVolume follows a successful absence observation under deployment serialization.
func createVolume(ctx context.Context, dk Scope, volume, stackID, need string, own Ownership) error {
	if out, err := dk.Run(ctx, append(append(append([]string{"volume", "create", "--label", dockerlabel.Owner + "=" + stackID}, dk.instanceLabel()...), own.info...), volume)...); err != nil {
		return fmt.Errorf("volume create %s: %v: %s", volume, err, strings.TrimSpace(string(out)))
	}
	return own.created(deploy.Owned{Kind: "volume", ID: volume, Data: true, Need: need})
}

// tearDownOwned deletes what a deployment's list names, after its serving
// container is gone (serving: its ID, not deleted again): containers, then
// networks, then data volumes (only when backedUp), then images (only
// those no container uses). What the daemon says is gone is passed over;
// what it cannot be asked about, and a failed delete, is reported Left,
// never hidden. A container goes without its anonymous volumes: only a
// volume the list names is ever deleted.
func tearDownOwned(ctx context.Context, dk Scope, owned []deploy.Owned, serving string, backedUp bool, storage deploy.StorageBindings) deploy.TakenDown {
	var out deploy.TakenDown
	left := func(o deploy.Owned, why string) {
		name := o.Name
		if name == "" {
			name = o.ID
		}
		out.Left = append(out.Left, deploy.Left{What: o.Label(), Data: o.Data,
			Handle: map[string]any{"kind": o.Kind, "name": name, "id": o.ID, "need": o.Need}, Why: why})
	}
	inspect := map[string][]string{"container": {"inspect"}, "network": {"network", "inspect"}, "volume": {"volume", "inspect"}, "image": {"image", "inspect"}}
	for _, kind := range []string{"container", "network", "volume", "image"} {
		for _, o := range owned {
			if o.Kind != kind || (kind == "container" && o.ID == serving) {
				continue
			}
			if err := ctx.Err(); err != nil {
				left(o, err.Error())
				continue
			}
			there, err := dk.Exists(ctx, append(inspect[kind], o.ID)...)
			if err != nil {
				left(o, "not known whether it is still there: "+err.Error())
				continue
			}
			if !there {
				if kind == "volume" && storage != nil {
					if err := storage.Retire(ctx, o.Need, o.ID); err != nil {
						left(o, "volume is gone but engine evidence could not be retired: "+err.Error())
					}
				}
				continue // the daemon says it is gone
			}
			switch kind {
			case "container":
				err = dk.Remove(ctx, o.ID)
			case "network":
				err = dk.RemoveNetwork(ctx, o.ID)
			case "volume":
				if o.Data && !backedUp {
					left(o, "kept: no final backup covers it")
					continue
				}
				err = dk.RemoveVolume(ctx, o.ID)
			case "image":
				var inUse bool
				if inUse, err = dk.RemoveImageUnlessUsed(ctx, o.ID); inUse {
					continue // another deployment's container runs it; it goes with that one
				}
			}
			if err != nil {
				left(o, err.Error())
				continue
			}
			if kind == "volume" && storage != nil {
				if err := storage.Retire(ctx, o.Need, o.ID); err != nil {
					left(o, "volume was deleted but engine evidence could not be retired: "+err.Error())
					continue
				}
			}
			out.Deleted = append(out.Deleted, o.Label())
		}
	}
	for _, o := range owned {
		switch o.Kind {
		case "container", "network", "volume", "image":
		default:
			left(o, "not something a Docker host deletes")
		}
	}
	return out
}

// DataMount is where a container keeps what is at a path: a volume's name,
// or a bind mount's source on the host. Found is false when nothing is
// mounted there, so the container keeps it in its own layer.
type DataMount struct {
	Volume, Bind string
	Found        bool
}

// Spec is the mount as `docker run -v` takes it, before ":path".
func (m DataMount) Spec() string {
	if m.Volume != "" {
		return m.Volume
	}
	return m.Bind
}

// DataMountAt is what a container mounts at path, of either kind: where a
// need service keeps its data.
func DataMountAt(ctx context.Context, dk Scope, container, path string) (DataMount, error) {
	out, err := dk.Run(ctx, "inspect", "-f", `{{range .Mounts}}{{.Type}}{{"\t"}}{{.Name}}{{"\t"}}{{.Source}}{{"\t"}}{{.Destination}}{{"\n"}}{{end}}`, container)
	if err != nil {
		return DataMount{}, fmt.Errorf("docker inspect %s: %v: %s", container, err, strings.TrimSpace(string(out)))
	}
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Split(strings.TrimSpace(l), "\t")
		if len(f) != 4 || strings.TrimRight(f[3], "/") != strings.TrimRight(path, "/") {
			continue
		}
		switch f[0] {
		case "volume":
			if f[1] != "" {
				return DataMount{Volume: f[1], Found: true}, nil
			}
		case "bind":
			if f[2] != "" {
				return DataMount{Bind: f[2], Found: true}, nil
			}
		}
	}
	return DataMount{}, nil
}

// VolumeAt is the volume mounted at path in a container, "" when none is:
// where a need container keeps its data, named or anonymous.
func VolumeAt(ctx context.Context, dk Scope, container, path string) (string, error) {
	out, err := dk.Run(ctx, "inspect", "-f", `{{range .Mounts}}{{if eq .Type "volume"}}{{.Name}}{{"\t"}}{{.Destination}}{{"\n"}}{{end}}{{end}}`, container)
	if err != nil {
		return "", fmt.Errorf("docker inspect %s: %w", container, err)
	}
	for _, l := range strings.Split(string(out), "\n") {
		name, dest, ok := strings.Cut(strings.TrimSpace(l), "\t")
		if ok && strings.TrimRight(dest, "/") == strings.TrimRight(path, "/") {
			return name, nil
		}
	}
	return "", nil
}
