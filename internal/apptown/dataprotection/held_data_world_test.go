package dataprotection

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/ids"
)

// releasedContracts is the canonical repository as release sources read
// it: each revision's .apptown/app.yaml. A revision it does not hold cannot
// be read, as an unreachable commit cannot.
type releasedContracts struct {
	mu sync.Mutex
	by map[string]string
}

func (r *releasedContracts) Project(_ context.Context, id ids.ID) (deployment.ReleaseProject, error) {
	return deployment.ReleaseProject{Application: deployment.Application{ID: id}}, nil
}

func (r *releasedContracts) Contract(_ context.Context, _ deployment.ReleaseProject, revision string) (*appcontract.Contract, string, error) {
	r.mu.Lock()
	yaml, ok := r.by[revision]
	r.mu.Unlock()
	if !ok {
		return nil, "", fmt.Errorf("no commit %s in the repository", revision)
	}
	c, err := appcontract.Parse([]byte(yaml))
	return c, revision, err
}

// deploymentNeedScope is deployment.NeedSecretsScope, for a function whose
// parameter shadows the package.
func deploymentNeedScope(id ids.ID) string { return deployment.NeedSecretsScope(id) }

// standRelease settles the deployment on a new release of yaml and runs
// each of its needs that keeps data, started with its kind's local.env
// over the release's params and mounting the need's named volume where its
// kind keeps it: a host as a release leaves it. It returns the revision.
func standRelease(t *testing.T, s *both, slug, deployment, yaml string) string {
	t.Helper()
	dep, err := s.Deployments.ByName(t.Context(), mustProjectOf(t, s, slug), deployment)
	if err != nil {
		t.Fatal(err)
	}
	src := s.sources.(*releasedContracts)
	src.mu.Lock()
	revision := fmt.Sprintf("r%d", len(src.by)+1)
	src.mu.Unlock()
	runRelease(t, s, dep, revision, yaml)
	c, err := appcontract.Parse([]byte(yaml))
	if err != nil {
		return revision // a release whose contract cannot be read runs nothing the host is told of
	}
	client := s.hosts.Local().Client.(*dockerfake.Daemon)
	names := hostNamesOf(t, dep, slug)
	for _, part := range c.Parts {
		if part.DataDir != "" {
			client.SetVolume(names.DataVolume(), true)
		}
	}
	mounts, started := map[string]string{}, map[string]string{}
	for _, need := range c.Needs {
		d, err := s.catalog.ByCapability(t.Context(), need)
		if err != nil || d.Local.Data == "" {
			continue
		}
		svc := names.Service(need)
		client.SetRunning(svc, true)
		mounts[svc] = names.NeedVolume(need) + "\t" + d.Local.Data
		// Started as a release starts it: the release's params and the
		// deployment's own generated credentials.
		generated, err := d.Generated(t.Context(), s.Service.env.NeedCredentials(deploymentNeedScope(dep.ID)))
		if err != nil {
			t.Fatal(err)
		}
		local := d.ResolveLocal(c.NeedParams[need], "", generated)
		client.SetStorageContainer(svc, local.Image, names.NeedVolume(need), d.Local.Data)
		cfg := struct {
			Image string
			Env   []string
		}{Image: local.Image}
		for name, value := range local.Env {
			cfg.Env = append(cfg.Env, name+"="+value)
		}
		raw, _ := json.Marshal(cfg)
		started["inspect --format {{json .Config}} "+svc] = string(raw)
	}
	hostMounts(client, mounts, func(line string) ([]byte, error, bool) {
		if cfg, ok := started[line]; ok {
			return []byte(cfg), nil, true
		}
		return nil, nil, false
	})
	return revision
}

// releaseMain is standRelease of the deployment main.
func releaseMain(t *testing.T, s *both, slug, yaml string) string {
	t.Helper()
	return standRelease(t, s, slug, "main", yaml)
}

func mustProjectOf(t *testing.T, s *both, slug string) ids.ID {
	t.Helper()
	p, err := project.New(s.db, nil).BySlug(t.Context(), slug)
	if err != nil {
		t.Fatal(err)
	}
	return p.ID
}

// runRelease commits yaml as revision's .apptown/app.yaml (when the sources
// are releasedContracts; other sources answer for themselves) and settles a
// generation of dep running it, through the deployment's own outcome
// operations: the deployment then records that revision as what it runs.
func runRelease(t *testing.T, s *both, dep *deployment.Deployment, revision, yaml string) {
	t.Helper()
	if src, ok := s.sources.(*releasedContracts); ok {
		src.mu.Lock()
		if src.by == nil {
			src.by = map[string]string{}
		}
		src.by[revision] = yaml
		src.mu.Unlock()
	}
	at, _, err := s.Outcomes.Admit(context.Background(), deployment.Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "release-" + revision, Inputs: deployment.Inputs{Revision: revision}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Outcomes.Complete(context.Background(), deployment.Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active", Health: "healthy"}}); err != nil {
		t.Fatal(err)
	}
}

// hostMounts answers which volume each container mounts where, which the
// fake daemon does not model (container → "volume\tpath" lines), after
// extra, which a test uses to script a failure.
func hostMounts(client *dockerfake.Daemon, mounts map[string]string, extra func(line string) ([]byte, error, bool)) {
	for _, lines := range mounts {
		for _, line := range strings.Split(lines, "\n") {
			if volume, _, ok := strings.Cut(line, "\t"); ok && volume != "" {
				client.SetVolume(volume, true)
			}
		}
	}
	client.SetReply(func(args []string) ([]byte, error, bool) {
		line := strings.Join(args, " ")
		if extra != nil {
			if out, err, ok := extra(line); ok {
				return out, err, true
			}
		}
		if strings.Contains(line, ".Mounts") {
			if m, ok := mounts[args[len(args)-1]]; ok {
				return []byte(m + "\n"), nil, true
			}
		}
		return nil, nil, false
	})
}
