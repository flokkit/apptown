package dataprotection

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/portainer"
)

// heldOnPortainer plans a copy of each data volume a deployment on a
// Portainer environment holds (design/subsystems/deployment.md, "Backups and
// restore", Portainer): every volume on its list (AppTown's deploy made
// it) and every volume its current stack declares, each asked of the
// environment's Docker. The stack file classifies: a volume a need's
// service mounts at its kind's data path is that need's, copied by its
// kind's copy in that service's container; any other is copied as files.
// Only listed volumes are the deployment's to delete.
func (s *core) heldOnPortainer(ctx context.Context, dk docker.Scope, layout portainer.StackLayout, dep *deployment.Deployment) (heldItems, error) {
	owned, err := s.owned.Owned(ctx, dep.ID)
	if err != nil {
		return heldItems{}, fmt.Errorf("%w: reading its list: %v", ErrDataNotKnown, err)
	}
	listed := map[string]bool{}
	var wants []string
	for _, o := range owned {
		if o.Kind == "volume" && o.Data {
			listed[o.ID] = true
			wants = append(wants, o.ID)
		}
	}
	for _, v := range layout.Volumes {
		if !listed[v.Name] {
			wants = append(wants, v.Name)
		}
	}
	if len(wants) == 0 {
		return heldItems{}, ErrNothingToBackUp
	}
	var items []backupPlanItem
	seen := map[string]bool{}
	for _, vol := range wants {
		if seen[vol] {
			continue
		}
		seen[vol] = true
		there, err := dk.Exists(ctx, "volume", "inspect", vol)
		if err != nil {
			return heldItems{}, fmt.Errorf("%w: not known whether the volume %s is there: %v", ErrDataNotKnown, vol, err)
		}
		if !there {
			continue
		}
		mount, mounted := layout.MountOf(vol)
		need := ""
		if mounted {
			need = mount.Service
		}
		var it backupPlanItem
		switch {
		case need != "" && layout.IsNeed(need):
			d, kerr := s.catalog.ByCapability(ctx, need)
			if kerr != nil {
				it = backupPlanItem{Kind: "need", Name: need, Volume: vol, File: vol + ".unread", Unresolved: unresolvedNeed(need, kerr)}
				break
			}
			if d.Local.Data != mount.Path {
				it = backupPlanItem{Kind: "need", Name: need, Volume: vol, File: vol + ".uncopied",
					Unresolved: fmt.Sprintf("not copied: %s mounts %s at %s, not where the %s kind keeps its data (%s)", need, vol, mount.Path, need, d.Local.Data)}
				break
			}
			container, err := s.serviceContainer(ctx, dk, layout.Stack, need)
			if err != nil {
				return heldItems{}, fmt.Errorf("%w: %v", ErrDataNotKnown, err)
			}
			if container == "" {
				it = backupPlanItem{Kind: "need", Name: need, Volume: vol, File: vol + ".uncopied", Unresolved: "not copied: no running " + need + " container of the stack mounts " + vol}
				break
			}
			it = needItem(d, need, container)
			it.Volume = vol
			it = volumeFile(it)
		default:
			it = appVolumeItem(s.env.ChownImage(), vol)
		}
		it.Listed = listed[vol]
		items = append(items, it)
	}
	if len(items) == 0 {
		return heldItems{}, ErrHeldDataAbsent
	}
	if err := oneFileEach(items); err != nil {
		return heldItems{}, err
	}
	return heldItems{items: items}, nil
}

// serviceContainer is the running container of a stack's service, by
// compose's labels; "" when none runs.
func (s *core) serviceContainer(ctx context.Context, dk docker.Scope, stack, service string) (string, error) {
	names, err := dk.List(ctx, "ps", "--filter", "label=com.docker.compose.project="+stack, "--filter", "label=com.docker.compose.service="+service, "--filter", "status=running", "--format", "{{.Names}}")
	if err != nil {
		return "", fmt.Errorf("listing the %s container of %s: %v", service, stack, err)
	}
	if len(names) == 0 {
		return "", nil
	}
	return names[0], nil
}

// stackWriters are the stack's running containers that are not a need's:
// what a restore or a final copy stops, and starts again.
func (s *core) stackWriters(ctx context.Context, dk docker.Scope, layout portainer.StackLayout) ([]deploy.Owned, error) {
	raw, err := dk.List(ctx, "ps", "--filter", "label=com.docker.compose.project="+layout.Stack, "--filter", "status=running", "--format", "{{.Names}}\t{{.Label \"com.docker.compose.service\"}}")
	if err != nil {
		return nil, fmt.Errorf("listing the running containers of %s: %v", layout.Stack, err)
	}
	var out []deploy.Owned
	for _, line := range raw {
		name, service, _ := strings.Cut(line, "\t")
		if layout.IsNeed(service) {
			continue
		}
		out = append(out, deploy.Owned{Kind: "container", ID: name, Name: name})
	}
	return out, nil
}

// portainerLayout reads the stack a Portainer deployment runs in: the one
// on its list (AppTown's own create), else the project's stack of its
// name on the target's environment. Reading it copies nothing and deletes
// nothing.
func (s *core) portainerLayout(ctx context.Context, t *deployment.Target, dep *deployment.Deployment, slug string) (portainer.StackLayout, error) {
	cfg, err := s.providers.PortainerConfig(ctx, t)
	if err != nil {
		return portainer.StackLayout{}, err
	}
	owned, err := s.owned.Owned(ctx, dep.ID)
	if err != nil {
		return portainer.StackLayout{}, fmt.Errorf("reading its list: %v", err)
	}
	prefix, base := portainer.StackNames(dep.ID, dep.HostPrefix, slug)
	name, id := portainer.StackName(prefix, base), ""
	for _, o := range owned {
		if o.Kind == "stack" {
			name, id = o.Name, o.ID
		}
	}
	if id == "" {
		endpoint, err := portainer.EndpointID(ctx, cfg)
		if err != nil {
			return portainer.StackLayout{}, err
		}
		stacks, err := portainer.ListStacks(ctx, cfg)
		if err != nil {
			return portainer.StackLayout{}, err
		}
		for _, st := range stacks {
			if st.Name == name && st.EndpointID == endpoint {
				id = strconv.FormatInt(st.ID, 10)
			}
		}
		if id == "" {
			return portainer.StackLayout{}, fmt.Errorf("Portainer runs no stack %s in %s", name, cfg.EnvName)
		}
	}
	compose, err := portainer.StackFile(ctx, cfg, id)
	if err != nil {
		return portainer.StackLayout{}, fmt.Errorf("reading the stack %s: %v", name, err)
	}
	return portainer.ReadLayout(name, base, compose)
}
