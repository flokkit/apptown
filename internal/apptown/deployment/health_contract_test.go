package deployment

import (
	"context"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
)

// revisionSources answers each revision's contract from a map, counting reads.
type revisionSources struct {
	by    map[string]string
	reads int
}

func (*revisionSources) Project(_ context.Context, id ids.ID) (ReleaseProject, error) {
	return ReleaseProject{Application: Application{ID: id, Slug: "release-create"}}, nil
}
func (r *revisionSources) Contract(_ context.Context, _ ReleaseProject, rev string) (*appcontract.Contract, string, error) {
	r.reads++
	raw, ok := r.by[rev]
	if !ok {
		return nil, "", errors.New("no such revision")
	}
	c, err := appcontract.Parse([]byte(raw))
	return c, rev, err
}

// The health controller probes what the running release declares, not the
// project's newest contract: an unreleased change to the health check does
// not change how running software is judged. A new release moves it.
//
// Kills: reading .apptown/app.yaml from the project's repository folder.
func TestHealthProbesTheContractOfTheReleaseThatRuns(t *testing.T) {
	t.Parallel()
	s, in, _, _ := requestFixture(t)
	contract := func(path string) string {
		return "app:\n  web:\n    image: reg/x:1\n    ports: [\"8080\"]\n    public: true\n    health:\n      liveness: { http: " + path + " }\n"
	}
	sources := &revisionSources{by: map[string]string{"rev-a": contract("/alive-a"), "rev-b": contract("/alive-b")}}
	s.release.Sources = sources
	dep, err := s.Deployments.ByName(context.Background(), in.ProjectID, in.DeploymentName)
	if err != nil {
		t.Fatal(err)
	}
	run := func(revision string) {
		t.Helper()
		at, _, err := s.Outcomes.Admit(context.Background(), Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "release-" + revision, Inputs: Inputs{Revision: revision}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Outcomes.Complete(context.Background(), Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active", Health: "healthy"}}); err != nil {
			t.Fatal(err)
		}
	}
	watched := docker.Watched{ProjectID: in.ProjectID, Slug: "release-create", Name: in.DeploymentName}
	path := func() string {
		p := s.healthPublicPart(context.Background(), watched)
		if p == nil || p.Intent == nil || p.Intent.Liveness == nil {
			return ""
		}
		return p.Intent.Liveness.HTTP
	}
	if got := path(); got != "" {
		t.Fatalf("a deployment with no recorded release has no declared probe: %q", got)
	}
	run("rev-a")
	if got := path(); got != "/alive-a" {
		t.Fatalf("probe of the running release: %q, want /alive-a", got)
	}
	reads := sources.reads
	path()
	if sources.reads != reads {
		t.Fatalf("a revision's contract is read once: %d reads, then %d", reads, sources.reads)
	}
	run("rev-b")
	if got := path(); got != "/alive-b" {
		t.Fatalf("after the next release: %q, want /alive-b", got)
	}
}
