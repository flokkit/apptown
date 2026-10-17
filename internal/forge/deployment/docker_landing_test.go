package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/ids/idstest"
)

type dockerClientFunc func(context.Context, string, docker.Command) ([]byte, error)

func (f dockerClientFunc) Run(ctx context.Context, host string, c docker.Command) ([]byte, error) {
	return f(ctx, host, c)
}
func TestLandingExecutorPreservesBootPolicyAndFailureClassification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		phase    string
		terminal bool
	}{
		{docker.PhaseResolve, true}, {docker.PhaseSecrets, true}, {docker.PhaseNeeds, false},
		{docker.PhaseParts, false}, {docker.PhaseApp, false}, {docker.PhaseProbe, true}, {docker.PhasePage, true},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			c := &appcontract.Contract{}
			checkpointed := false
			e := landingDeploymentExecution{label: "3", target: docker.BootRequest{Name: "app", Deployment: "main", Persistent: true, PageCheck: true, Probe: 90 * time.Second, Services: []deploy.ServiceSpec{{Name: "postgres", Image: "postgres:16"}}}, contract: c, artifact: "selected",
				url: func(port int) string { return fmt.Sprintf("http://app:%d", port) },
				boot: func(_ context.Context, target docker.BootRequest, got *appcontract.Contract) (docker.BootResult, error) {
					if !checkpointed || got != c || !target.Persistent || !target.PageCheck || target.Probe != 90*time.Second || len(target.Services) != 1 || target.Identity == nil || target.Identity.Deployment != "3" || target.Identity.Generation != 2 || target.Identity.Artifact != "selected" {
						t.Fatalf("changed boot policy or skipped checkpoint: %+v", target)
					}
					return docker.BootResult{HostPort: 8080}, &docker.BootError{Phase: tc.phase, Err: errors.New("boot failed"), LogTail: "last lines"}
				}}
			at := Attempt{ID: idstest.N(7), DeploymentID: idstest.N(3), Generation: 2}
			_, err := e.execute(context.Background(), at, func(values map[string]any) error {
				if values["container"] != "app" {
					t.Fatal("wrong checkpoint")
				}
				checkpointed = true
				return nil
			})
			if err == nil || deploy.IsTerminal(err) != tc.terminal {
				t.Fatalf("boot classification: %v", err)
			}
		})
	}
}

func TestLandingExecutorStopsOnCheckpointFailureOrCancellation(t *testing.T) {
	t.Parallel()
	for _, when := range []string{"checkpoint failure", "before boot", "after boot"} {
		t.Run(when, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			boots := 0
			e := landingDeploymentExecution{target: docker.BootRequest{Name: "app"}, url: func(int) string { return "http://app" }, boot: func(context.Context, docker.BootRequest, *appcontract.Contract) (docker.BootResult, error) {
				boots++
				cancel()
				return docker.BootResult{}, nil
			}}
			_, err := e.execute(ctx, Attempt{ID: idstest.N(7), DeploymentID: idstest.N(3), Generation: 2}, func(map[string]any) error {
				if when == "checkpoint failure" {
					return errors.New("receipt unavailable")
				}
				if when == "before boot" {
					cancel()
				}
				return nil
			})
			if when == "checkpoint failure" {
				if err == nil || !deploy.IsTerminal(err) || boots != 0 {
					t.Fatalf("%v boots=%d", err, boots)
				}
			} else {
				want := 0
				if when == "after boot" {
					want = 1
				}
				if !errors.Is(err, context.Canceled) || boots != want {
					t.Fatalf("%v boots=%d", err, boots)
				}
			}
		})
	}
}

func TestLandingEvidenceContainsReceiptsWithoutResolvedSecrets(t *testing.T) {
	t.Parallel()
	c := &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true, Image: "declared:image"}}}
	scope := docker.Scope{Client: dockerClientFunc(func(_ context.Context, _ string, cmd docker.Command) ([]byte, error) {
		switch strings.Join(cmd.Args, " ") {
		case "inspect -f {{.Config.Image}} app":
			return []byte("running:image"), nil
		case "inspect -f {{.Image}} app":
			return []byte("sha256:running"), nil
		default:
			return nil, errors.New("no registry digest")
		}
	})}
	receipts := []docker.SecretReceipt{{Name: "DATABASE_URL", Store: "internal", Version: "1", Consumers: []string{"web"}}}
	e := landingDeploymentExecution{target: docker.BootRequest{Name: "app", Deployment: "main", Docker: scope}, contract: c, result: LandingBoot{URL: "http://app", Boot: docker.BootResult{Stack: docker.ResolvedStack{Plan: appcontract.Prepare(c, nil), Secrets: secret.Delivery{Profile: "main", Receipts: receipts, ByConsumer: map[string]map[string]string{"web": {"DATABASE_URL": "sensitive-value"}}}.Docker()}}}}
	// The front-page check the deploy passed is part of its evidence.
	page := &docker.PageWitness{Path: "/", Host: "shop.example.com", Status: 200, Passed: true, Verdict: "GET / answered 200"}
	e.result.Boot.Page = page
	got := e.evidence(context.Background())
	if got.Image == nil || *got.Image != "running:image" || got.ImageID != "sha256:running" || got.ContractImage != "declared:image" || !reflect.DeepEqual(got.Secrets, receipts) || got.SecretProfile != "main" || got.FrontPage != page {
		t.Fatalf("lost evidence: %+v", got)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "sensitive-value") {
		t.Fatal("evidence contains a resolved credential")
	}
	e.target.Docker = docker.Scope{Client: dockerClientFunc(func(context.Context, string, docker.Command) ([]byte, error) {
		return nil, errors.New("inspection unavailable")
	})}
	fallback := e.evidence(context.Background())
	raw, err := json.Marshal(fallback)
	if err != nil || !strings.Contains(string(raw), `"digest":""`) || !strings.Contains(string(raw), `"image":"declared:image"`) {
		t.Fatalf("fallback evidence changed wire presence: %s %v", raw, err)
	}

}

// Reading the browser values fails before any container effect, so the attempt
// settles as a known failure, never uncertain, and once the setting is fixed
// another may start.
func TestAnUnreadableBrowserSettingFailsTheAttemptWithoutStrandingIt(t *testing.T) {
	t.Parallel()
	f, dep := deploymentOutcomeFixture(t)
	useRecoveryJobs(t, f)
	c, err := appcontract.Parse([]byte("app:\n  web: {static: ., public: true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	scope := docker.Scope{Client: dockerClientFunc(func(context.Context, string, docker.Command) ([]byte, error) {
		t.Fatal("Docker called before browser configuration was read")
		return nil, nil
	})}
	host := docker.NewHost(docker.Dependencies{Needs: noNeeds{}, Secrets: noSecrets{}, Integrations: unreadableBrowser{}})
	raw := `{"version":1,"revision":"selected-revision"}`
	start := Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "browser-failed", Inputs: Inputs{Revision: "selected-revision", ArtifactHash: deploy.ConfigHash(raw), DockerSource: raw}}
	e := landingDeploymentExecution{label: dep.Label(), target: docker.BootRequest{Name: "serving", Workspace: t.TempDir(), Persistent: true, Docker: scope}, contract: c, artifact: start.Inputs.ArtifactHash, boot: host.Boot, url: func(int) string { return "http://app" }}
	result, err := (Runner{Store: f.outcomes}).Run(context.Background(), start, e.execute)
	if err == nil || !strings.Contains(err.Error(), "browser attachment unavailable") {
		t.Fatalf("expected browser attachment failure, got %v", err)
	}
	_, _, retryErr := f.outcomes.Admit(context.Background(), deploymentStartFor(dep, "after-config-repair", "selected"))
	if result.Outcome == Uncertain || retryErr != nil {
		t.Fatalf("configuration failure stranded deployment: outcome=%s error=%v next attempt=%v", result.Outcome, err, retryErr)
	}
}

type noNeeds struct{}

func (noNeeds) Fulfil(context.Context, []string, map[string]map[string]string) ([]deploy.ServiceSpec, map[string]string, error) {
	return nil, nil, nil
}
func (noNeeds) Data(context.Context, string) (string, error) { return "", nil }

type noSecrets struct{}

func (noSecrets) Resolve(context.Context, string, string, string, *appcontract.Contract) (docker.Delivery, error) {
	return docker.Delivery{}, nil
}

type unreadableBrowser struct{}

func (unreadableBrowser) EnvFor(context.Context) []string { return nil }
func (unreadableBrowser) BrowserEnv(context.Context) (string, error) {
	return "", errors.New("browser attachment unavailable")
}
