package deployment

import (
	"context"
	"errors"
	"flokkit.io/forge/internal/dockerlabel"
	"fmt"
	"sort"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/digitalocean"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/portainer"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/internal/forge/deployment/internal/deploymentsql"
	"flokkit.io/forge/internal/forge/project"
)

// The one-time ownership inspection (design/subsystems/deployment.md,
// "Removing a deployment"). Deployments made before the forge recorded what it
// created have no list, so their take-down deletes nothing of theirs but
// the serving container or app. The inspection proposes, from what each
// target holds under the forge's names and labels, the entries those lists
// would have: a person approves them, and only approved entries are
// recorded, with that approval as their note. Nothing found is recorded on
// its own: a name is not evidence the forge made something.

// OwnershipProposal is one thing the inspection proposes to record: an
// entry on a deployment's list, or a project's repository on an account.
// Key names it for approval; Evidence says why it is proposed.
type OwnershipProposal struct {
	Key        string        `json:"key"`
	Project    string        `json:"project"`
	Deployment string        `json:"deployment,omitempty"`
	Target     string        `json:"target"`
	Owned      *deploy.Owned `json:"owned,omitempty"`
	Repository string        `json:"repository,omitempty"`
	Evidence   string        `json:"evidence"`

	projectID, deploymentID ids.ID
	connector, registry     string
}

// OwnershipInspection is what the inspection proposes, and the targets it
// could not look at, each with why: unknown is not empty.
type OwnershipInspection struct {
	Proposals []OwnershipProposal `json:"proposals"`
	Unknown   []string            `json:"unknown"`
}

// InspectOwnership looks at every target with standing deployments and
// proposes what their lists are missing. It changes nothing.
func (s *Service) InspectOwnership(ctx context.Context) (OwnershipInspection, error) {
	out := OwnershipInspection{Proposals: []OwnershipProposal{}, Unknown: []string{}}
	targets, err := s.Targets.All(ctx)
	if err != nil {
		return out, err
	}
	all, err := s.Deployments.All(ctx)
	if err != nil {
		return out, err
	}
	for _, t := range targets {
		var standing []ProjectDeployment
		for _, d := range all {
			if d.TargetID == t.ID && d.Standing() {
				standing = append(standing, d)
			}
		}
		if len(standing) == 0 {
			continue
		}
		var props []OwnershipProposal
		var err error
		switch t.Kind {
		case KindDockerHost:
			props, err = s.inspectDockerHost(ctx, t, standing)
		case KindPortainer:
			props, err = s.inspectPortainer(ctx, t, standing)
		case KindDOApp:
			props, err = s.inspectAppPlatform(ctx, t, standing)
		default:
			continue
		}
		if err != nil {
			out.Unknown = append(out.Unknown, fmt.Sprintf("%s: not inspected: %v", t.Name, err))
		}
		out.Proposals = append(out.Proposals, props...)
	}
	// One repository per project and account, however many targets share it.
	seen := map[string]bool{}
	kept := out.Proposals[:0]
	for _, p := range out.Proposals {
		if !seen[p.Key] {
			seen[p.Key] = true
			kept = append(kept, p)
		}
	}
	out.Proposals = kept
	return out, nil
}

// ApproveOwnership records the proposals whose keys a person approved, as
// the inspection proposes them now: a key it no longer proposes is
// refused, nothing recorded. Each entry's note says who approved it.
func (s *Service) ApproveOwnership(ctx context.Context, keys []string, actor string) ([]OwnershipProposal, error) {
	now := s.now()
	if actor == "" {
		return nil, errors.New("an approval needs the person who approves")
	}
	inspection, err := s.InspectOwnership(ctx)
	if err != nil {
		return nil, err
	}
	byKey := map[string]OwnershipProposal{}
	for _, p := range inspection.Proposals {
		byKey[p.Key] = p
	}
	var approved []OwnershipProposal
	var missing []string
	for _, k := range keys {
		p, ok := byKey[k]
		if !ok {
			missing = append(missing, k)
			continue
		}
		approved = append(approved, p)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrNotProposed, strings.Join(missing, ", "))
	}
	note := fmt.Sprintf("approved by %s at the one-time ownership inspection, %s", actor, now.Format("2 January 2006"))
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		q := deploymentsql.New(tx.SQL())
		for _, p := range approved {
			if p.Owned != nil {
				if _, err := q.RecordOwned(ctx, deploymentsql.RecordOwnedParams{ID: ids.New(now), DeploymentID: p.deploymentID, Kind: p.Owned.Kind, Ident: p.Owned.ID, Name: p.Owned.Name,
					HoldsData: p.Owned.Data, Need: p.Owned.Need, RecordedAt: now, Note: note}); err != nil {
					return err
				}
			} else {
				repo := strings.TrimPrefix(p.Repository, p.registry+"/")
				if _, err := q.RecordRepository(ctx, deploymentsql.RecordRepositoryParams{ID: ids.New(now), ProjectID: p.projectID, Connector: p.connector, Registry: p.registry,
					Repository: repo, RecordedAt: now, Note: note}); err != nil {
					return err
				}
			}
			if err := audit.At(now).RecordIn(ctx, tx, actor, p.projectID, "ownership.approved", p.Key, map[string]any{"target": p.Target, "deployment": p.Deployment, "owned": p.Owned, "repository": p.Repository, "evidence": p.Evidence}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return approved, nil
}

// ErrNotProposed refuses an approval of something the inspection does not
// propose now.
var ErrNotProposed = errors.New("the inspection does not propose these now; inspect again")

func ownedProposal(t *Target, d ProjectDeployment, o deploy.Owned, evidence string) OwnershipProposal {
	return OwnershipProposal{Key: fmt.Sprintf("deployment:%d:%s:%s", d.ID, o.Kind, o.ID), Project: d.Project, Deployment: d.Name, Target: t.Name,
		Owned: &o, Evidence: evidence, projectID: d.ProjectID, deploymentID: d.ID}
}

// unlisted keeps the proposals not already on their deployment's list.
func (s *Service) unlisted(ctx context.Context, props []OwnershipProposal) ([]OwnershipProposal, error) {
	lists := map[ids.ID]map[string]bool{}
	var out []OwnershipProposal
	for _, p := range props {
		if lists[p.deploymentID] == nil {
			owned, err := s.Deployments.Owned(ctx, p.deploymentID)
			if err != nil {
				return nil, err
			}
			lists[p.deploymentID] = map[string]bool{}
			for _, o := range owned {
				lists[p.deploymentID][o.Kind+" "+o.ID] = true
			}
		}
		if !lists[p.deploymentID][p.Owned.Kind+" "+p.Owned.ID] {
			out = append(out, p)
		}
	}
	return out, nil
}

// inspectDockerHost proposes, for each standing deployment, what is on the
// host under its names and labels: its app container (labelled with the
// deployment's identity), its stack's containers (labelled with the
// stack), each need's data volume (named, or mounted where the need keeps
// its data), the stack's network and the app's data volume.
func (s *Service) inspectDockerHost(ctx context.Context, t *Target, standing []ProjectDeployment) ([]OwnershipProposal, error) {
	dk, err := s.hosts.DockerScope(ctx, t)
	if err != nil {
		return nil, err
	}
	var props []OwnershipProposal
	for _, d := range standing {
		app := docker.ContainerName(d.Project, d.Name)
		stack := docker.StackID(d.Project, d.Name)
		add := func(o deploy.Owned, evidence string) { props = append(props, ownedProposal(t, d, o, evidence)) }
		if line := dk.Text(ctx, "inspect", "-f", `{{.Id}} {{index .Config.Labels "`+dockerlabel.Deployment+`"}}`, app); line != "" {
			id, label, _ := strings.Cut(line, " ")
			if label == fmt.Sprint(d.ID) {
				add(deploy.Owned{Kind: "container", ID: id, Name: app}, "the app container, labelled "+dockerlabel.Deployment+"="+label)
			}
		}
		idList, err := dk.List(ctx, "ps", "-aq", "--no-trunc", "--filter", "label="+dockerlabel.Class+"=env", "--filter", "label="+dockerlabel.Owner+"="+stack)
		if err != nil {
			return props, err
		}
		for _, id := range idList {
			name := strings.TrimPrefix(dk.Text(ctx, "inspect", "-f", "{{.Name}}", id), "/")
			service := strings.TrimPrefix(name, docker.ServiceName(stack, ""))
			need := ""
			if dep := s.needKind(ctx, service); dep != nil {
				need = service
				if dep.Local.Data != "" {
					if vol, err := docker.VolumeAt(ctx, dk, id, dep.Local.Data); err == nil && vol != "" && vol != docker.NeedVolumeName(stack, service) {
						add(deploy.Owned{Kind: "volume", ID: vol, Data: true, Need: service}, "the volume the "+service+" container keeps its data in, at "+dep.Local.Data)
					}
				}
			}
			add(deploy.Owned{Kind: "container", ID: id, Name: name, Need: need}, "a container labelled "+dockerlabel.Owner+"="+stack)
		}
		vols, err := dk.List(ctx, "volume", "ls", "-q", "--filter", "label="+dockerlabel.Owner+"="+stack)
		if err != nil {
			return props, err
		}
		for _, v := range vols {
			if need, ok := strings.CutPrefix(v, docker.NeedVolumeName(stack, "")); ok {
				add(deploy.Owned{Kind: "volume", ID: v, Data: true, Need: need}, "the "+need+" data volume, labelled "+dockerlabel.Owner+"="+stack)
			}
		}
		if dk.OK(ctx, "network", "inspect", docker.NetworkName(stack)) {
			add(deploy.Owned{Kind: "network", ID: docker.NetworkName(stack)}, "the stack's network")
		}
		if vol := docker.DataVolumeName(app); dk.OK(ctx, "volume", "inspect", vol) {
			add(deploy.Owned{Kind: "volume", ID: vol, Data: true}, "the app's data volume")
		}
	}
	return s.unlisted(ctx, props)
}

// inspectPortainer proposes each standing deployment's stack, found by the
// name its handle records on the target's endpoint.
func (s *Service) inspectPortainer(ctx context.Context, t *Target, standing []ProjectDeployment) ([]OwnershipProposal, error) {
	cfg, err := s.providers.PortainerConfig(ctx, t)
	if err != nil {
		return nil, err
	}
	endpointID, err := portainer.EndpointID(ctx, cfg)
	if err != nil {
		return nil, err
	}
	stacks, err := portainer.ListStacks(ctx, cfg)
	if err != nil {
		return nil, err
	}
	var props []OwnershipProposal
	for _, d := range standing {
		name, _ := d.Handle["stack"].(string)
		for _, st := range stacks {
			if name != "" && st.Name == name && st.EndpointID == endpointID {
				props = append(props, ownedProposal(t, d, deploy.Owned{Kind: "stack", ID: fmt.Sprint(st.ID), Name: st.Name}, "the stack the deployment's handle names, on endpoint "+cfg.EnvName))
			}
		}
	}
	return s.unlisted(ctx, props)
}

// inspectAppPlatform proposes each project's DOCR repository on the
// target's account, when the registry holds one of the project's name
// that the forge has not recorded.
func (s *Service) inspectAppPlatform(ctx context.Context, t *Target, standing []ProjectDeployment) ([]OwnershipProposal, error) {
	c, _, err := s.providers.cloudClientFor(t.Provider)(ctx)
	if err != nil {
		return nil, err
	}
	registry, err := c.RegistryName(ctx)
	if err != nil || registry == "" {
		return nil, err
	}
	var props []OwnershipProposal
	done := map[ids.ID]bool{}
	for _, d := range standing {
		if done[d.ProjectID] {
			continue
		}
		done[d.ProjectID] = true
		p, err := project.ByIDIn(ctx, s.db, d.ProjectID)
		if err != nil {
			return props, err
		}
		owned, err := (repositoryRecorder{db: s.db, project: d.ProjectID, connector: t.Provider}).Owns(ctx, registry, p.Slug)
		if err != nil {
			return props, err
		}
		exists, err := digitalocean.RepositoryExists(ctx, c, registry, p.Slug)
		if err != nil {
			return props, err
		}
		if owned || !exists {
			continue
		}
		props = append(props, OwnershipProposal{Key: fmt.Sprintf("repository:%d:%s:%s/%s", d.ProjectID, t.Provider, registry, p.Slug), Project: p.Slug, Target: t.Name,
			Repository: registry + "/" + p.Slug, Evidence: "the registry holds a repository of the project's name, which its App Platform deployments ran from",
			projectID: d.ProjectID, connector: t.Provider, registry: registry})
	}
	sort.Slice(props, func(i, j int) bool { return props[i].Key < props[j].Key })
	return props, nil
}

// needKind is the dependency catalog's entry for a capability, nil when it
// names none (or there is no catalog).
func (s *Service) needKind(ctx context.Context, capability string) *dependency.Kind {
	if s.env.needs == nil {
		return nil
	}
	d, err := s.env.needs.ByCapability(ctx, capability)
	if err != nil {
		return nil
	}
	return d
}
