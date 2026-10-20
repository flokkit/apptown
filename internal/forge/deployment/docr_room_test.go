package deployment

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/deploy/digitalocean"
	"flokkit.io/forge/internal/deploy/digitalocean/dofake"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/docker/dockerfake"
	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/stock"
)

// docrWorld is a project, "expense", whose main carries a contract with a
// postgres need, a bundle cut from it, a DigitalOcean connector whose
// token resolves and whose registry is forge-test, the App Platform target
// it offers, and the fake API behind it all.
type docrWorld struct {
	s       *Service
	api     *dofake.API
	project ids.ID
	bundle  *ReleaseBundle
}

func newDOCRWorld(t *testing.T) *docrWorld {
	t.Helper()
	db := testDB(t)
	if err := jobs.Migrate(context.Background(), db.Pool()); err != nil {
		t.Fatal(err)
	}
	w := &docrWorld{api: &dofake.API{}}
	srv := httptest.NewServer(w.api.Handler())
	t.Cleanup(srv.Close)
	w.project = createTestProject(t, db, "expense", "Expense")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("DIGITALOCEAN_ACCESS_TOKEN=dop_v1_test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "workspaces", "project-expense")
	contract := []byte("app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n    health:\n      readiness: { http: /healthz }\ninit:\n  - name: migrate\n    build: .\n    command: [\"dotnet\", \"App.dll\", \"--init\"]\nneeds:\n  postgres:\n    version: \"16\"\n")
	for name, body := range map[string][]byte{".forge/app.yaml": contract, "Dockerfile": []byte("FROM scratch\n")} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "expense"}} {
		cmd := gittest.Command(args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	revision, err := releaseGit(t.Context(), repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	revision = strings.TrimSpace(revision)
	projects := project.New(db, nil)
	if err := projects.SetDefaultBranch(context.Background(), w.project, "main"); err != nil {
		t.Fatal(err)
	}
	secrets := secret.NewService(db, secret.Config{DataDir: dir})
	integrations := integration.NewService(db, integration.Dependencies{Projects: projects, Secrets: secrets})
	catalog := dependency.NewCatalog(db, nil)
	if _, _, err := catalog.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	sources := noLocalSources{repo: repo, yaml: string(contract)}
	options := digitalocean.Options{APIBase: srv.URL, PollInterval: 10 * time.Millisecond, DBPoll: 10 * time.Millisecond,
		ProbeFrontDoor: func(context.Context, string, time.Time, func(string, ...any)) error { return nil },
		OwnDatabase:    func(context.Context, string, string, string) error { return nil },
		PushImage: func(_ *digitalocean.Driver, _ context.Context, _ *digitalocean.Client, registry, repo, local, tag string, logf func(string, ...any)) (string, error) {
			logf("pushed %s as %s/%s:%s (fake)", local, registry, repo, tag)
			return "sha256:" + strings.Repeat("a", 64), nil
		}}
	w.s = New(context.Background(), db, assembled(db, testRuntime{Scope: docker.Scope{Client: dockerfake.New()}, DataDir: dir, Projects: projects, Secrets: secrets, Integrations: integrations, Needs: catalog, ChownImage: "test-helper", BuiltInTarget: "local", DigitalOcean: options}, Dependencies{Followups: recoveryJobFollowups{}, Configuration: ConfigurationDependencies{Projects: sources}, Releases: ReleaseDependencies{Authority: &requestAuthority{}, Sources: sources, Secrets: doReleaseSecrets{}, Queue: doReleaseQueue{}}}))
	config, err := json.Marshal(map[string]any{"tokenFrom": "DIGITALOCEAN_ACCESS_TOKEN", "region": "syd", "registry": "forge-test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := integrations.Bindings.Save(context.Background(), "do-syd", digitalocean.CapCloudAccount, "digitalocean", string(config), "fixture"); err != nil {
		t.Fatal(err)
	}
	w.s.SeedCloudTargets(t.Context(), "fixture")
	bid, err := w.s.Bundles.Cut(context.Background(), w.project, revision, map[string]string{"web": "sha256:web"}, map[string]string{"migrate": "sha256:web"}, contract, BundleOrigin{})
	if err != nil {
		t.Fatal(err)
	}
	if w.bundle, err = w.s.Bundles.ByID(context.Background(), bid); err != nil {
		t.Fatal(err)
	}
	return w
}

// cloud is the project's deployment on the App Platform target.
func (w *docrWorld) cloud(t *testing.T) (*Target, *Deployment) {
	t.Helper()
	tg, err := w.s.Targets.ByName(t.Context(), "do-syd")
	if err != nil {
		t.Fatal(err)
	}
	dep, err := w.s.Deployments.EnsureStanding(t.Context(), w.project, "cloud", tg.ID, Attributes{Feed: "promotion", Audience: "organisation", DataClass: "non-production", PromoteAfter: "main"})
	if err != nil {
		t.Fatal(err)
	}
	return tg, dep
}

// release requests the bundle's release to dep and runs it, as its job
// does, and answers it as recorded.
func (w *docrWorld) release(t *testing.T, dep *Deployment) *Release {
	t.Helper()
	accepted, err := w.s.RequestRelease(context.Background(), DeploySelection{ProjectID: w.project, DeploymentName: dep.Name, BundleID: w.bundle.ID})
	if err != nil {
		t.Fatal(err)
	}
	_ = w.s.ExecuteRelease(context.Background(), accepted.ID) // its outcome is the release's state
	rel, err := w.s.Releases.ByNumber(context.Background(), w.project, accepted.Number)
	if err != nil {
		t.Fatal(err)
	}
	return rel
}

// A release to App Platform refuses, before anything is made on
// DigitalOcean, when the account's container registry is full and the
// project has no repository there yet: the log says so plainly and names
// the choice. A full registry that already holds the project's repository
// is no obstacle, and a registry whose room cannot be read is logged as
// unknown while the release goes on. The target's check reports the room.
//
// Kills: no preflight (the database is made, then the copy fails);
// a preflight after the needs are fulfilled; a refusal when the forge's own
// repository for the project exists; adopting a repository of the project's
// name the forge did not create; an unreadable room taken as full.
func TestAFullDOCRRegistryRefusesANewProjectBeforeAnythingIsMade(t *testing.T) {
	t.Parallel()
	release := func(t *testing.T, seed func(*dofake.API)) (*Release, string, dofake.State) {
		t.Helper()
		w := newDOCRWorld(t)
		seed(w.api)
		tg, dep := w.cloud(t)
		rel := w.release(t, dep)
		rows, err := w.s.Releases.Events(context.Background(), rel.ID)
		if err != nil {
			t.Fatal(err)
		}
		var log []string
		for _, r := range rows {
			log = append(log, r.Text)
		}
		if c := w.s.providers.CheckTarget(t.Context(), tg); !c.OK || c.Registry == nil {
			t.Errorf("the target's check: %+v", c)
		}
		return rel, strings.Join(log, "\n"), w.api.Snapshot()
	}
	t.Run("full, new project", func(t *testing.T) {
		t.Parallel()
		rel, log, state := release(t, func(f *dofake.API) { f.SeedRegistry("forge-test", "starter", 1, "example-app") })
		if rel.State != ReleaseFailed || !strings.Contains(log, "registry plan full (1 of 1 repositories, starter tier): the container registry forge-test has no repository for expense yet") ||
			!strings.Contains(log, "Upgrade the registry plan, or take a deployment down") {
			t.Fatalf("release %s:\n%s", rel.State, log)
		}
		if len(state.Databases) != 0 || len(state.Apps) != 0 || len(state.Specs) != 0 {
			t.Fatalf("a refused release made %d databases, %d apps", len(state.Databases), len(state.Apps))
		}
		for _, r := range state.Requests {
			if r.Method != "GET" {
				t.Fatalf("a refused release wrote to DigitalOcean: %v", r)
			}
		}
	})
	t.Run("full, project's repository there but not AppTown's", func(t *testing.T) {
		t.Parallel()
		rel, log, state := release(t, func(f *dofake.API) { f.SeedRegistry("forge-test", "basic", 5, "a", "b", "c", "d", "expense") })
		if rel.State != ReleaseFailed || !strings.Contains(log, "already has a repository named expense that AppTown did not create") {
			t.Fatalf("release %s:\n%s", rel.State, log)
		}
		if len(state.Databases) != 0 || len(state.Apps) != 0 {
			t.Fatalf("a refused release made %d databases, %d apps", len(state.Databases), len(state.Apps))
		}
	})
	t.Run("full, AppTown's repository there", func(t *testing.T) {
		t.Parallel()
		w := newDOCRWorld(t)
		w.api.SeedRegistry("forge-test", "basic", 5, "a", "b", "c", "d")
		_, dep := w.cloud(t)
		for i := range 2 {
			if rel := w.release(t, dep); rel.State != Released {
				t.Fatalf("release %d: %+v", i+1, rel)
			}
			// The first release's copy made the project's repository, which
			// fills the registry; the second uses it.
			w.api.SeedManifests("expense", "sha256:copied")
		}
	})
	t.Run("room unknown", func(t *testing.T) {
		t.Parallel()
		rel, log, _ := release(t, func(f *dofake.API) {
			f.SeedRegistry("forge-test", "starter", 1, "example-app")
			f.Fail("GET", "/v2/registry/subscription", 500)
		})
		if rel.State != Released || !strings.Contains(log, "the container registry's room is unknown") {
			t.Fatalf("release %s:\n%s", rel.State, log)
		}
	})
}

// A project that released to App Platform before the forge recorded its
// repository finds its own repository refused, until the one-time
// inspection proposes it and a person approves it; then it releases.
func TestAnApprovedRepositoryReleasesAgain(t *testing.T) {
	t.Parallel()
	w := newDOCRWorld(t)
	w.api.SeedRegistry("forge-test", "basic", 5)
	w.api.SeedManifests("expense", "sha256:from-before")
	_, dep := w.cloud(t)
	if got := w.release(t, dep).State; got != ReleaseFailed {
		t.Fatalf("an unrecorded repository was used: %s", got)
	}
	inspection, err := w.s.InspectOwnership(context.Background())
	if err != nil || len(inspection.Unknown) != 0 || len(inspection.Proposals) != 1 || inspection.Proposals[0].Repository != "forge-test/expense" {
		t.Fatalf("inspection: %+v %v", inspection, err)
	}
	if _, err := w.s.ApproveOwnership(context.Background(), []string{inspection.Proposals[0].Key}, "bls"); err != nil {
		t.Fatal(err)
	}
	if got := w.release(t, dep).State; got != Released {
		t.Fatalf("the approved repository: %s", got)
	}
}

// An App Platform target's check reads its registry's plan and how many of
// its repositories are used, from DigitalOcean, and says when it is full;
// a registry it cannot read is unknown, with why, never full or empty.
//
// The gate image's repository is among those used, and the reading says
// so.
//
// Kills: a full registry read as having room; an unreadable one read as
// empty or full; the gate's repository not shown.
func TestTheTargetsCheckReadsTheRegistrysPlan(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		seed func(*dofake.API)
		want RegistryReading
		says string
	}{
		"full": {func(f *dofake.API) { f.SeedRegistry("forge-test", "starter", 1, "example-app") },
			RegistryReading{Registry: "forge-test", Tier: "starter", Included: 1, Used: 1, Full: true}, "1 of 1 repositories used, starter tier; registry plan full"},
		"room": {func(f *dofake.API) { f.SeedRegistry("forge-test", "basic", 5, "a", "b") },
			RegistryReading{Registry: "forge-test", Tier: "basic", Included: 5, Used: 2}, "container registry forge-test: 2 of 5 repositories used, basic tier"},
		"gate": {func(f *dofake.API) { f.SeedRegistry("forge-test", "basic", 5, "a", digitalocean.GateRepository) },
			RegistryReading{Registry: "forge-test", Tier: "basic", Included: 5, Used: 2, Gate: true}, "2 of 5 repositories used, basic tier (one holds AppTown's gate image, apptown_gate)"},
		"unreadable": {func(f *dofake.API) {
			f.SeedRegistry("forge-test", "basic", 5, "a")
			f.Fail("GET", "/v2/registry/subscription", 500)
		}, RegistryReading{Registry: "forge-test"}, "container registry unknown: "},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := newDOCRWorld(t)
			tc.seed(w.api)
			tg, _ := w.cloud(t)
			c := w.s.providers.CheckTarget(context.Background(), tg)
			if !c.OK || c.Registry == nil || !strings.Contains(c.Detail, tc.says) {
				t.Fatalf("check: %+v", c)
			}
			got := *c.Registry
			if name == "unreadable" {
				if got.Unknown == "" {
					t.Fatalf("an unreadable registry: %+v", got)
				}
				got.Unknown = ""
			}
			if got != tc.want {
				t.Fatalf("reading %+v want %+v", got, tc.want)
			}
		})
	}
}
