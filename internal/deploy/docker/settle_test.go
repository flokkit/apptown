package docker

import (
	"context"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// Every way a release on a Docker host can end without a known outcome
// leaves a record that a later look can settle: whatever the host answers
// about the attempt's container, Resume says applied or failed. Only a host
// that cannot be asked leaves it unknown, for the next look. An attempt
// nothing can ever settle blocks every release and take-down of its
// deployment.
//
// Kills: Resume leaving any answered case unknown (a missing boot record,
// a container replaced or stopped).
func TestEveryUnsettledDockerAttemptCanBeSettled(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte("app:\n  game:\n    static: .\n    public: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"version":1,"revision":"abc"}`)
	art := deploy.Artifact{Kind: "docker-source", Body: body, Hash: deploy.ConfigHash(string(body)),
		Source: &deploy.Source{Workspace: t.TempDir(), Revision: "abc", Slug: "game", Contract: c}}
	place := deploy.Placement{ID: idstest.N(7), Label: "7", Name: "main"}
	const generation = 2

	// The ways in: each runs the release as it happens and keeps the
	// checkpoint it left.
	ways := map[string]func(ctx context.Context, cancel func(), daemon *fakeDaemon) (Booter, func(map[string]any) error){
		"a boot failure no phase reported": func(_ context.Context, _ func(), _ *fakeDaemon) (Booter, func(map[string]any) error) {
			return failingBoot{errors.New("it failed")}, nil
		},
		"AppTown stopped during the boot": func(ctx context.Context, cancel func(), _ *fakeDaemon) (Booter, func(map[string]any) error) {
			return bootFunc(func() (BootResult, error) { cancel(); return BootResult{}, ctx.Err() }), nil
		},
		"the host stopped answering after a healthy boot": func(_ context.Context, _ func(), daemon *fakeDaemon) (Booter, func(map[string]any) error) {
			return bootFunc(func() (BootResult, error) {
				res, err := (&fakeBoot{daemon: daemon}).Boot(context.Background(), BootRequest{Identity: &deploy.DockerIdentity{Deployment: "7", Generation: generation, Artifact: art.Hash}}, c)
				daemon.unreachable = true
				return res, err
			}), nil
		},
		"the boot record could not be written": func(_ context.Context, _ func(), daemon *fakeDaemon) (Booter, func(map[string]any) error) {
			return &fakeBoot{daemon: daemon}, func(values map[string]any) error {
				if _, ok := values["dockerReady"]; ok {
					return errors.New("the database went away")
				}
				return nil
			}
		},
		"the completion could not be written": func(_ context.Context, _ func(), daemon *fakeDaemon) (Booter, func(map[string]any) error) {
			return &fakeBoot{daemon: daemon}, nil
		},
	}
	for way, run := range ways {
		t.Run(way, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			daemon := &fakeDaemon{}
			boot, fail := run(ctx, cancel, daemon)
			noted := map[string]any{}
			d := &Driver{ServingLabel: "stable-serving", Scope: func(context.Context) (Scope, error) { return Scope{Client: daemon}, nil }, Boot: boot,
				URL: func(_, _ string, _ int) string { return "http://game" }, AfterRelease: func(deploy.Placement) {}}
			_, err := d.Reconcile(ctx, place, generation, art, "key", func(string, ...any) {}, func(values map[string]any) error {
				if fail != nil {
					if err := fail(values); err != nil {
						return err
					}
				}
				for k, v := range values {
					noted[k] = v
				}
				return nil
			})
			if deploy.IsTerminal(err) {
				t.Fatalf("this way is not into an unknown outcome: %v", err)
			}
			// What a later look can find on a host that answers.
			booted := deploy.DockerIdentity{Deployment: "7", Generation: generation, Artifact: art.Hash}.Labels()
			answers := map[string]*fakeDaemon{
				"running as booted":          {exists: true, running: true, image: "sha256:game", labels: booted},
				"running another generation": {exists: true, running: true, image: "sha256:game", labels: deploy.DockerIdentity{Deployment: "7", Generation: generation - 1, Artifact: art.Hash}.Labels()},
				"stopped":                    {exists: true, image: "sha256:game", labels: booted},
				"gone":                       {},
			}
			for answer, host := range answers {
				o, err := Resume(context.Background(), Scope{Client: host}, place, generation, noted)
				if err != nil && !deploy.IsTerminal(err) {
					t.Fatalf("%s: nothing can settle it: %v", answer, err)
				}
				if err == nil && (answer != "running as booted" || o.Generation != generation) {
					t.Fatalf("%s: settled applied: %+v", answer, o)
				}
			}
			if _, err := Resume(context.Background(), Scope{Client: &fakeDaemon{unreachable: true}}, place, generation, noted); err == nil || (deploy.IsTerminal(err) && noted["dockerReady"] != nil) {
				t.Fatalf("a host that cannot be asked settled a recorded boot: %v", err)
			}
		})
	}
}

type bootFunc func() (BootResult, error)

func (f bootFunc) Boot(context.Context, BootRequest, *appcontract.Contract) (BootResult, error) {
	return f()
}
