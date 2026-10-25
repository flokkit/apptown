package deployment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
)

// An AppTown with no local Docker releases to attached Docker hosts only
// (design/operations/workers.md, "Running AppTown without a local Docker
// daemon").
// The attached host here is a recording daemon reached at its own address:
// every command names the daemon it went to, and it holds the images it
// built until a test takes one away.

// multipartApp has a static public part, a part built from the tree, an
// image-only part, an init step built from the tree and an image-only one.
const multipartApp = `app:
  web:
    public: true
    static: site
    ports: ["8080"]
  worker:
    build: worker
  cache:
    image: example/cache:7
init:
  - name: migrate
    build: migrations
  - name: seed
    image: example/seed:1
`

const attachedHost = "ssh://" + "apptown-target-remote"

// attachedDaemon records which daemon each command went to and holds the
// images that daemon has.
type attachedDaemon struct {
	*dockerfake.Daemon
	mu     sync.Mutex
	hosts  map[string]int
	images map[string]bool
	ran    map[string]string // container → the image it was started with
}

func newAttachedDaemon() *attachedDaemon {
	d := &attachedDaemon{Daemon: dockerfake.New(), hosts: map[string]int{}, images: map[string]bool{}, ran: map[string]string{}}
	d.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) < 2 || args[0] != "image" {
			return nil, nil, false
		}
		line := strings.Join(args, " ")
		ref := args[len(args)-1]
		switch {
		case strings.Contains(line, "{{.Config.User}}"), strings.Contains(line, "RepoDigests"):
			return []byte("\n"), nil, true
		case !strings.Contains(line, "{{.Id}}"):
			return nil, nil, false
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if strings.HasPrefix(ref, "sha256:") {
			if !d.images[ref] {
				return []byte("Error response from daemon: No such image: " + ref), errors.New("exit status 1"), true
			}
			return []byte(ref + "\n"), nil, true
		}
		// A tag the daemon built or pulled: its image is now held.
		sum := sha256.Sum256([]byte(ref))
		id := "sha256:" + hex.EncodeToString(sum[:8])
		d.images[id] = true
		return []byte(id + "\n"), nil, true
	})
	return d
}

// Run records the daemon asked, and answers what a container runs as the
// image it was started with, as Docker does.
func (d *attachedDaemon) Run(ctx context.Context, host string, req docker.Command) ([]byte, error) {
	d.mu.Lock()
	d.hosts[host]++
	d.mu.Unlock()
	out, err := d.Daemon.Run(ctx, host, req)
	args := req.Args
	if len(args) >= 2 && args[0] == "--config" {
		args = args[2:]
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case len(args) > 0 && args[0] == "run":
		name := ""
		for i, a := range args {
			if a == "--name" && i+1 < len(args) {
				name = args[i+1]
			}
			if strings.HasPrefix(a, "sha256:") && name != "" {
				d.ran[name] = a
				break
			}
		}
	case err == nil && len(args) == 4 && args[0] == "inspect" && args[2] == docker.RuntimeFormat && d.ran[args[3]] != "":
		var rt docker.Runtime
		if json.Unmarshal(out, &rt) == nil {
			rt.Image = d.ran[args[3]]
			out, err = json.Marshal(rt)
		}
	}
	return out, err
}

func (d *attachedDaemon) forget(id string) {
	d.mu.Lock()
	delete(d.images, id)
	d.mu.Unlock()
}

// localCalls counts commands that went anywhere but the attached host.
func (d *attachedDaemon) otherHosts() map[string]int {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]int{}
	for h, n := range d.hosts {
		if h != attachedHost {
			out[h] = n
		}
	}
	return out
}

func (d *attachedDaemon) verbs(verb string) []string {
	var out []string
	for _, args := range d.Commands() {
		if len(args) > 0 && args[0] == verb {
			out = append(out, strings.Join(args, " "))
		}
	}
	return out
}

func (d *attachedDaemon) remove(container string) {
	if _, err := d.Daemon.Run(context.Background(), "", docker.Command{Args: []string{"rm", "-f", container}}); err != nil {
		panic(err)
	}
}

type noLocalSources struct {
	repo, yaml string
}

func (s noLocalSources) Project(_ context.Context, id ids.ID) (ReleaseProject, error) {
	return ReleaseProject{Application: Application{ID: id, Slug: "game"}, Repository: s.repo}, nil
}
func (s noLocalSources) Contract(_ context.Context, _ ReleaseProject, revision string) (*appcontract.Contract, string, error) {
	c, err := appcontract.Parse([]byte(s.yaml))
	return c, revision, err
}

type landingClaim struct{ projectID, runID ids.ID }

func (c landingClaim) ClaimedStep(kind string) (ids.ID, ids.ID, ids.ID, error) {
	if kind != "deploy" {
		return ids.Nil, ids.Nil, ids.Nil, fmt.Errorf("claimed %s, not deploy", kind)
	}
	return c.projectID, c.runID, idstest.N(10), nil // the deploy step
}

type noLocalWorld struct {
	db       *database.DB
	dir      string
	daemon   *attachedDaemon
	project  ids.ID
	remote   *Target
	repo     string
	revision string
	sources  noLocalSources
	// service builds a Service on the same records, with or without the mode.
	service func(noLocal bool) *Service
}

func noLocalDockerWorld(t *testing.T, yaml string) *noLocalWorld {
	t.Helper()
	db := testDB(t)
	w := &noLocalWorld{db: db, dir: t.TempDir(), daemon: newAttachedDaemon()}
	w.project = createTestProject(t, db, "game", "Game")
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == appcontract.ReleasePath {
			// AppTown's file server answers the release its last
			// container was told.
			var told string
			for _, run := range w.daemon.verbs("run") {
				if _, after, ok := strings.Cut(run, " -e "+appcontract.ReleaseEnvVar+"="); ok {
					told, _, _ = strings.Cut(after, " ")
				}
			}
			_, _ = rw.Write([]byte(told))
			return
		}
		_, _ = rw.Write([]byte("<html><script>game()</script></html>"))
	}))
	t.Cleanup(server.Close)
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	n, _ := strconv.Atoi(port)
	w.daemon.SetPort(n)
	// The attached host's SSH key lives in AppTown's secrets store.
	if err := os.WriteFile(filepath.Join(w.dir, "secrets.env"), []byte("REMOTE_KEY=-----BEGIN OPENSSH PRIVATE KEY-----\\nabc\\n-----END OPENSSH PRIVATE KEY-----\n"), 0o600); err != nil { // gitleaks:allow (test fixture, not a credential)
		t.Fatal(err)
	}
	projects := project.New(db, nil)
	secrets := secret.NewService(db, secret.Config{DataDir: w.dir})
	integrations := integration.NewService(db, integration.Dependencies{Projects: projects, Secrets: secrets})
	w.repo = filepath.Join(w.dir, "workspaces", "project-"+w.project.String())
	files := map[string]string{".apptown/app.yaml": yaml, "site/index.html": "<html>game</html>", "worker/Dockerfile": "FROM scratch\n", "migrations/Dockerfile": "FROM scratch\n"}
	for name, body := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(w.repo, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(w.repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "game"}} {
		cmd := gittest.Command(args...)
		cmd.Dir = w.repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	cmd := gittest.Command("rev-parse", "HEAD")
	cmd.Dir = w.repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	w.revision = strings.TrimSpace(string(out))
	w.sources = noLocalSources{repo: w.repo, yaml: yaml}
	mustExec(t, db, `CREATE TABLE test_release_queue(release_id UUID PRIMARY KEY REFERENCES releases(id))`)
	w.service = func(noLocal bool) *Service {
		return New(context.Background(), db, assembled(db, testRuntime{Scope: docker.Scope{Client: w.daemon}, Probe: &docker.Probe{Settle: time.Millisecond}, DataDir: w.dir, SSHConfigPath: filepath.Join(w.dir, "ssh-config"), Projects: projects, Secrets: secrets, Integrations: integrations, Needs: dependency.NewCatalog(db, nil), ChownImage: "test-helper", BuiltInTarget: "local", NoLocalDocker: noLocal}, Dependencies{Followups: noFollowups{}, Configuration: ConfigurationDependencies{Projects: w.sources}, Releases: ReleaseDependencies{Authority: &requestAuthority{}, Still: &requestAuthority{}, Sources: w.sources, Secrets: requestSecrets{}, Queue: requestQueue{}}}))
	}
	w.remote, err = w.service(true).Targets.Put(context.Background(), &Target{Name: "remote", Kind: KindDockerHost, Origin: OriginAttached, Status: TargetReady,
		Address: map[string]any{"host": "ssh://deploy@127.0.0.1", "keyFrom": "REMOTE_KEY"}, NeedsMode: "emulators", SecretsDelivery: "inject"})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *noLocalWorld) deployment(t *testing.T, s *Service, name string, target *Target) *Deployment {
	t.Helper()
	dep, err := s.Deployments.EnsureStanding(t.Context(), w.project, name, target.ID, Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	return dep
}

// land runs a landing of the repository's head to a deployment, then cuts
// its bundle from what the landing booted, where it booted (what the
// workflow's deploy step does: workflowexec.cutBundle).
func (w *noLocalWorld) land(t *testing.T, s *Service, dep *Deployment, runID ids.ID) (LandingResult, *ReleaseBundle) {
	t.Helper()
	ctx := context.Background()
	art, cleanup, err := docker.PrepareSource(ctx, w.repo, w.revision, "game", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	out, err := s.RunLanding(ctx, landingClaim{w.project, runID}, LandingInput{
		Start:  Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: fmt.Sprintf("run-%d", runID), Inputs: Inputs{ReleaseRun: runID, Revision: w.revision, ArtifactHash: art.Hash, DockerSource: string(art.Body)}},
		Source: *art.Source, DataClass: "synthetic",
	})
	if err != nil || out.Boot.Err != nil || out.Execution.Outcome != Applied {
		t.Fatalf("landing: %+v %v %v", out.Execution, err, out.Boot.Err)
	}
	if told := " -e " + appcontract.ReleaseEnvVar + "=" + appcontract.ReleaseIdentity("web", w.revision) + " "; !strings.Contains(strings.Join(w.daemon.verbs("run"), "\n"), told) {
		t.Fatalf("the landing did not tell the static part its release (%s): %v", told, w.daemon.verbs("run"))
	}
	parts, init := out.Images(ctx)
	id, err := s.Bundles.Cut(ctx, w.project, w.revision, parts, init, []byte(multipartApp), BundleOrigin{RunID: runID, TargetID: out.TargetID})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Bundles.OfRun(ctx, w.project, runID)
	if err != nil || b == nil || b.ID != id {
		t.Fatalf("the landing's bundle: %+v %v", b, err)
	}
	return out, b
}

func (w *noLocalWorld) release(t *testing.T, s *Service, bundleID ids.ID, deployment string) (AcceptedRelease, error) {
	t.Helper()
	accepted, err := s.RequestRelease(context.Background(), DeploySelection{ProjectID: w.project, DeploymentName: deployment, BundleID: bundleID})
	if err != nil {
		return accepted, err
	}
	return accepted, s.ExecuteRelease(context.Background(), accepted.ID)
}

func (w *noLocalWorld) rebuild(t *testing.T, s *Service, dep *Deployment) (map[string]any, error) {
	t.Helper()
	current, err := s.Deployments.ByID(context.Background(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	return JobWorker{Service: s, Projects: project.New(w.db, nil), DataDir: w.dir}.Rebuild(context.Background(), "game/"+dep.Name,
		map[string]any{"deployment": current.ID, "generation": current.ObservedGeneration})
}

// expectRecordedBoot says a boot ran the bundle's images and nothing else:
// no build, no pull, every part and init step its recorded ID, the static
// part served by the file server with its browser values, and every
// command on the attached host.
func expectRecordedBoot(t *testing.T, w *noLocalWorld, b *ReleaseBundle, container string) {
	t.Helper()
	if builds, pulls := w.daemon.verbs("build"), w.daemon.verbs("pull"); len(builds)+len(pulls) != 0 {
		t.Fatalf("a recorded release built or pulled: %v %v", builds, pulls)
	}
	if other := w.daemon.otherHosts(); len(other) != 0 {
		t.Fatalf("commands went to another daemon: %v", other)
	}
	runs := strings.Join(w.daemon.verbs("run"), "\n")
	for part, id := range b.Parts {
		if !strings.Contains(runs, id) {
			t.Fatalf("part %s did not run its recorded image %s:\n%s", part, id, runs)
		}
	}
	for step, id := range b.Init {
		if !strings.Contains(runs, id) {
			t.Fatalf("init %s did not run its recorded image %s:\n%s", step, id, runs)
		}
	}
	for _, run := range w.daemon.verbs("run") {
		if strings.Contains(run, "--name "+container+" ") {
			// The bundle's static image carries its own command: the host
			// adds none. The page is given its browser values.
			if !strings.HasSuffix(run, " "+b.Parts["web"]) || !strings.Contains(run, docker.BrowserEnvVar+"=") {
				t.Fatalf("the static part is not run as its bundle says: %s", run)
			}
			// It is told the release it serves, answered at /_apptown/release.
			if !strings.Contains(run, " -e "+appcontract.ReleaseEnvVar+"="+appcontract.ReleaseIdentity("web", b.ContractRevision)+" ") {
				t.Fatalf("the static part is not told its release %s: %s", b.ContractRevision, run)
			}
			return
		}
	}
	t.Fatalf("%s never ran:\n%s", container, runs)
}

// Landing, promotion and missing-container recovery on an attached target
// in the mode: the landing builds on the target and the bundle names it;
// promotion and recovery, however often repeated, boot the recorded images
// there with the prepared runtime configuration and build nothing; an image
// the target lost is refused before any attempt.
func TestNoLocalDockerReleasesReuseTheTargetsImages(t *testing.T) {
	t.Parallel()
	w := noLocalDockerWorld(t, multipartApp)
	s := w.service(true)
	main := w.deployment(t, s, "main", w.remote)
	staging := w.deployment(t, s, "staging", w.remote)

	out, bundle := w.land(t, s, main, idstest.N(7))
	if out.TargetID != w.remote.ID || out.Docker.Host != attachedHost || bundle.BuiltFrom.TargetID != w.remote.ID {
		t.Fatalf("the landing's target and daemon: %d %q, bundle %+v", out.TargetID, out.Docker.Host, bundle.BuiltFrom)
	}
	if len(w.daemon.verbs("build")) != 3 || len(w.daemon.otherHosts()) != 0 {
		t.Fatalf("the landing builds on the attached host only: %v %v", w.daemon.verbs("build"), w.daemon.otherHosts())
	}
	if len(bundle.Parts) != 3 || len(bundle.Init) != 2 {
		t.Fatalf("the bundle records every part and init step: %+v", bundle)
	}
	for _, id := range append(mapValues(bundle.Parts), mapValues(bundle.Init)...) {
		if !w.daemon.images[id] {
			t.Fatalf("the bundle's ID %s was not read from the attached daemon: %+v", id, bundle)
		}
	}

	// Promotion to another deployment on the same target.
	w.daemon.ResetCommands()
	if _, err := w.release(t, s, bundle.ID, "staging"); err != nil {
		t.Fatal(err)
	}
	expectRecordedBoot(t, w, bundle, hostNamesOf(t, s, w.project, "game", "staging").App())
	if dep, _ := s.Deployments.ByID(context.Background(), staging.ID); dep.ObservedGeneration != 1 || dep.Readiness != Reconciled {
		t.Fatalf("staging after the promotion: %+v", dep)
	}

	// Missing-container recovery: of the promotion, and of the landing,
	// each twice — a recovered generation still names its bundle.
	for _, dep := range []*Deployment{staging, main} {
		for n := 1; n <= 2; n++ {
			w.daemon.remove(hostNamesOf(t, s, w.project, "game", dep.Name).App())
			w.daemon.ResetCommands()
			if got, err := w.rebuild(t, s, dep); err != nil || got["outcome"] != Applied {
				t.Fatalf("recovering %s, time %d: %v %v", dep.Name, n, got, err)
			}
			expectRecordedBoot(t, w, bundle, hostNamesOf(t, s, w.project, "game", dep.Name).App())
		}
	}

	// The target lost an image: promotion and recovery are refused before
	// any attempt, and nothing runs.
	w.daemon.forget(bundle.Init["seed"])
	before, _ := s.Deployments.ByID(context.Background(), staging.ID)
	w.daemon.ResetCommands()
	if _, err := w.release(t, s, bundle.ID, "staging"); !errors.Is(err, ErrReleaseRefused) || !strings.Contains(err.Error(), "this release's images live only on remote") {
		t.Fatalf("a release whose image is gone: %v", err)
	}
	w.daemon.remove(hostNamesOf(t, s, w.project, "game", "staging").App())
	if _, err := w.rebuild(t, s, staging); !errors.Is(err, ErrReleaseRefused) {
		t.Fatalf("a recovery whose image is gone: %v", err)
	}
	if after, _ := s.Deployments.ByID(context.Background(), staging.ID); after.DesiredGeneration != before.DesiredGeneration {
		t.Fatalf("a refused release admitted a generation: %d → %d", before.DesiredGeneration, after.DesiredGeneration)
	}
	if runs := w.daemon.verbs("run"); len(runs) != 0 || len(w.daemon.verbs("build")) != 0 {
		t.Fatalf("a refused release ran or built: %v", runs)
	}
}

func mapValues(m map[string]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// In the mode a release goes only to the attached target its bundle was
// recorded on: another attached target, the built-in host, Portainer and
// App Platform are refused, naming the next action, before anything is
// built or any daemon is asked. Release previews and intranet releases are
// refused too.
func TestNoLocalDockerRefusesOtherTargetsBeforeAnyBuild(t *testing.T) {
	t.Parallel()
	w := noLocalDockerWorld(t, multipartApp)
	s := w.service(true)
	bundle := cutBundleFixture(t, w.db, w.project, w.revision, BundleOrigin{TargetID: w.remote.ID})
	targets := map[string]*Target{
		"other":  {Name: "other", Kind: KindDockerHost, Origin: OriginAttached, Address: map[string]any{"host": "ssh://deploy@127.0.0.2", "keyFrom": "REMOTE_KEY"}},
		"local":  {Name: "local", Kind: KindDockerHost, Origin: OriginBuiltIn},
		"lan":    {Name: "lan", Kind: KindPortainer, Origin: OriginAttached},
		"cloud":  {Name: "cloud", Kind: KindDOApp, Origin: OriginAttached},
		"remote": w.remote,
	}
	want := map[string]string{
		"other":  "this release's images live only on remote",
		"local":  "this AppTown has no local Docker; attach a Docker host",
		"lan":    "needs this release's images in the project's container registry, and this release was not published there. Attach a container registry",
		"cloud":  "needs this release's images in the project's container registry, and this release was not published there. Attach a container registry",
		"remote": "",
	}
	landing := map[string]string{"local": want["local"], "lan": "a landing deploys to a Docker host", "cloud": "a landing deploys to a Docker host"}
	for name, target := range targets {
		if target.ID.IsZero() {
			target.Status = TargetReady
			saved, err := s.Targets.Put(context.Background(), target)
			if err != nil {
				t.Fatal(err)
			}
			targets[name] = saved
		}
		w.deployment(t, s, "on-"+name, targets[name])
	}
	var queued AcceptedRelease
	for name, words := range want {
		accepted, err := s.RequestRelease(context.Background(), DeploySelection{ProjectID: w.project, DeploymentName: "on-" + name, BundleID: bundle})
		if words == "" {
			if err != nil {
				t.Fatalf("the bundle's own target: %v", err)
			}
			queued = accepted
			continue
		}
		if !errors.Is(err, ErrReleaseRefused) || !strings.Contains(err.Error(), words) {
			t.Fatalf("a release to %s: %v", name, err)
		}
	}
	if _, err := s.hosts.DockerScope(context.Background(), targets["local"]); !errors.Is(err, ErrNoLocalDocker) {
		t.Fatalf("the built-in host: %v", err)
	}
	// A landing on the built-in host, Portainer or App Platform is refused
	// at admission, before any boot, naming the next action.
	for _, name := range []string{"local", "lan", "cloud"} {
		dep, _ := s.Deployments.ByName(t.Context(), w.project, "on-"+name)
		_, err := s.RunLanding(context.Background(), landingClaim{w.project, idstest.N(3)}, LandingInput{
			Start:  Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "run-3", Inputs: Inputs{ReleaseRun: idstest.N(3), Revision: w.revision}},
			Source: deploy.Source{Workspace: w.repo, Revision: w.revision, Slug: "game", Contract: &appcontract.Contract{}},
		})
		var runErr *RunError
		if !errors.As(err, &runErr) || runErr.Stage != Admission || !errors.Is(err, ErrReleaseRefused) || !strings.Contains(err.Error(), landing[name]) {
			t.Fatalf("a landing on %s: %v", name, err)
		}
	}
	// A release recorded on a daemon, never published, has no preview here
	// either (Preview's TestInTheNoLocalDockerModeOnlyPublishedReleasesArePreviewed).
	if err := s.ExecuteRelease(context.Background(), queued.ID); err != nil {
		t.Fatal(err)
	}
}

// An AppTown that once ran with local Docker holds a bundle with no
// target and an attached deployment released from it. Started in the mode,
// its manual promotion and its missing-container recovery are refused with
// the next action, nothing is built or run, and the records stay. Outside
// the mode the same recovery builds from source, as before. A fresh landing
// then releases on the same target by reuse.
func TestNoLocalDockerRefusesReleasesRecordedBeforeTargets(t *testing.T) {
	t.Parallel()
	w := noLocalDockerWorld(t, multipartApp)
	before := w.service(false)
	staging := w.deployment(t, before, "staging", w.remote)
	legacy := cutBundleFixture(t, w.db, w.project, w.revision, BundleOrigin{})
	first, err := w.release(t, before, legacy, "staging")
	if err != nil {
		t.Fatal(err)
	}
	queued, err := before.RequestRelease(context.Background(), DeploySelection{ProjectID: w.project, DeploymentName: "staging", BundleID: legacy})
	if err != nil {
		t.Fatal(err)
	}

	const message = "this release does not say which Docker host its images are on; land the change again to release it in this mode"
	s := w.service(true)
	w.daemon.remove(hostNamesOf(t, s, w.project, "game", "staging").App())
	w.daemon.ResetCommands()
	if _, err := s.RequestRelease(context.Background(), DeploySelection{ProjectID: w.project, DeploymentName: "staging", BundleID: legacy}); !errors.Is(err, ErrReleaseRefused) || !strings.Contains(err.Error(), message) {
		t.Fatalf("promoting a legacy bundle: %v", err)
	}
	if err := s.ExecuteRelease(context.Background(), queued.ID); !errors.Is(err, ErrReleaseRefused) || !strings.Contains(err.Error(), message) {
		t.Fatalf("a legacy release queued before the mode: %v", err)
	}
	if _, err := w.rebuild(t, s, staging); !errors.Is(err, ErrReleaseRefused) || !strings.Contains(err.Error(), message) {
		t.Fatalf("recovering a legacy release: %v", err)
	}
	if len(w.daemon.verbs("build")) != 0 || len(w.daemon.verbs("run")) != 0 {
		t.Fatalf("a refused legacy release built or ran: %v", w.daemon.Commands())
	}
	if b, err := s.Bundles.ByID(context.Background(), legacy); err != nil || !b.BuiltFrom.TargetID.IsZero() {
		t.Fatalf("the legacy bundle is kept as it was: %+v %v", b, err)
	}
	if rel, err := s.Releases.ByNumber(context.Background(), w.project, first.Number); err != nil || rel.State != Released {
		t.Fatalf("the legacy release's history is kept: %+v %v", rel, err)
	}
	if dep, _ := s.Deployments.ByID(context.Background(), staging.ID); dep.ObservedGeneration != 1 {
		t.Fatalf("the legacy deployment is kept: %+v", dep)
	}

	// Outside the mode, the same recovery builds from source, as before.
	if got, err := w.rebuild(t, before, staging); err != nil || got["outcome"] != Applied || len(w.daemon.verbs("build")) == 0 {
		t.Fatalf("recovery outside the mode: %v %v", got, err)
	}

	// A fresh landing names its target, and its bundle is reused there.
	main := w.deployment(t, s, "main", w.remote)
	_, bundle := w.land(t, s, main, idstest.N(9))
	w.daemon.ResetCommands()
	if _, err := w.release(t, s, bundle.ID, "staging"); err != nil {
		t.Fatal(err)
	}
	expectRecordedBoot(t, w, bundle, hostNamesOf(t, s, w.project, "game", "staging").App())
}

// Observing a deployment's container on its own target tells running,
// stopped and missing (the daemon's answers) apart from what could not be
// observed: the built-in host of an AppTown with no local Docker is
// unavailable, and a target whose connection cannot be resolved, a daemon
// that does not answer or a deployment with no container is unknown, never
// stopped or missing.
func TestObservingAContainerKeepsUnknownApartFromStoppedAndMissing(t *testing.T) {
	t.Parallel()
	w := noLocalDockerWorld(t, multipartApp)
	s := w.service(true)
	w.daemon.SetRunning("app-running", true)
	w.daemon.SetRunning("app-stopped", false)
	w.daemon.SetCommandReply(func(c docker.Command) ([]byte, error, bool) {
		switch name := c.Args[len(c.Args)-1]; {
		case c.Args[0] != "inspect":
			return nil, nil, false
		case name == "app-gone":
			return []byte("Error: No such object: app-gone\n"), errors.New("exit status 1"), true
		case name == "app-unreachable":
			return []byte("Cannot connect to the Docker daemon\n"), errors.New("exit status 1"), true
		}
		return nil, nil, false
	})
	builtIn, err := s.Targets.Put(context.Background(), &Target{Name: "local", Kind: KindDockerHost, Origin: OriginBuiltIn, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	unreadable, err := s.Targets.Put(context.Background(), &Target{Name: "unreadable", Kind: KindDockerHost, Origin: OriginAttached, Status: TargetReady,
		Address: map[string]any{"host": "ssh://deploy@127.0.0.1", "keyFrom": "MISSING_KEY"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		target    ids.ID
		container string
		want      ContainerPresence
	}{
		{w.remote.ID, "app-running", ContainerRunning},
		{w.remote.ID, "app-stopped", ContainerStopped},
		{w.remote.ID, "app-gone", ContainerGone},
		{w.remote.ID, "app-unreachable", ContainerUnknown},
		{w.remote.ID, "", ContainerUnknown},
		{unreadable.ID, "app-running", ContainerUnknown},
		{builtIn.ID, "app-running", ContainerUnavailable},
	} {
		got := s.ObserveContainer(context.Background(), &Deployment{TargetID: c.target, Container: c.container})
		if got.State != c.want || (c.want == ContainerUnknown || c.want == ContainerUnavailable) != (got.Reason != "") {
			t.Errorf("target %d, container %q: %v (%s), want %v", c.target, c.container, got.State, got.Reason, c.want)
		}
	}
}

// A landing on an attached host records, and the owner answers, the host's
// own address for main as for any standing deployment: this AppTown's routes
// (here http://localhost:<port>) serve only its own host, so they would
// send people nowhere.
func TestALandingOnAnAttachedHostIsReachedAtThatHost(t *testing.T) {
	t.Parallel()
	w := noLocalDockerWorld(t, multipartApp)
	s := w.service(true)
	main := w.deployment(t, s, "main", w.remote)
	out, _ := w.land(t, s, main, idstest.N(7))
	want := fmt.Sprintf("http://127.0.0.1:%d", out.Boot.Boot.HostPort)
	if out.Boot.URL != want {
		t.Fatalf("the landing recorded %q, want %q", out.Boot.URL, want)
	}
	// The port the landing published, recorded as the workflow's deploy
	// step reserves it (workflowexec/standing.go).
	if port, err := s.Deployments.ReserveHostPort(t.Context(), w.project, "main", hostNamesOf(t, s, w.project, "game", "main").App(), out.Boot.Boot.HostPort); err != nil || port != out.Boot.Boot.HostPort {
		t.Fatalf("reserving the landing's port: %d %v", port, err)
	}
	landed, err := s.Deployments.ByID(context.Background(), main.ID)
	if err != nil {
		t.Fatal(err)
	}
	if landed.URL != want || s.URL(t.Context(), "game", landed) != want {
		t.Fatalf("the deployment is reached at %q (recorded %q), want %q", s.URL(t.Context(), "game", landed), landed.URL, want)
	}
	// What a deployment recorded before this fix is answered from the host.
	landed.URL = "http://localhost:1"
	if got := s.URL(t.Context(), "game", landed); got != want {
		t.Fatalf("a stale recorded URL: %q", got)
	}
}

// App Platform runs linux/amd64: a digest bundle built for another
// platform is refused when its release is requested, before anything is
// copied or the app changes, in the mode and outside it.
//
// Kills: App Platform admitting a bundle of another platform, to fail (or
// crash-loop) later.
func TestAppPlatformRefusesADigestBundleOfAnotherPlatform(t *testing.T) {
	t.Parallel()
	w := noLocalDockerWorld(t, multipartApp)
	cloud, err := w.service(false).Targets.Put(context.Background(), &Target{Name: "cloud", Kind: KindDOApp, Origin: OriginAttached, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	origin := digestOrigin(&claimedAttempt{db: w.db, project: w.project, run: idstest.N(7), step: idstest.N(70), at: 1})
	origin.Platform = "linux/arm64"
	id, err := NewBundles(w.db).Cut(t.Context(), w.project, w.revision, map[string]string{"web": releaseRepo + digestA}, nil, []byte(multipartApp), origin)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []bool{false, true} {
		s := w.service(mode)
		w.deployment(t, s, "on-cloud", cloud)
		_, err := s.RequestRelease(context.Background(), DeploySelection{ProjectID: w.project, DeploymentName: "on-cloud", BundleID: id})
		if !errors.Is(err, ErrReleaseRefused) || !strings.Contains(err.Error(), "built for linux/arm64, and cloud runs linux/amd64") {
			t.Fatalf("no local Docker %v: %v", mode, err)
		}
	}
}

// A landing's missing deployment goes where the project can be served: the
// built-in host outside the mode; in the mode the one attached Docker host
// offered to the project, and with none or several a refusal naming the
// choice to make.
func TestNoLocalDockerLandingTargetIsTheOneOfferedAttachedHost(t *testing.T) {
	w := noLocalDockerWorld(t, multipartApp)
	ctx := context.Background()
	attrs := Attributes{Feed: "landing", Audience: "team", DataClass: "synthetic"}
	mode := w.service(true)

	if _, err := mode.LandingTarget(ctx, w.project, "main", attrs); !errors.Is(err, ErrReleaseRefused) || !strings.Contains(err.Error(), "no attached Docker host is offered to game") {
		t.Fatalf("no host offered: %v", err)
	}
	if err := mode.Targets.Offer(ctx, w.remote, "*", OfferCaps{}, "boss"); err != nil {
		t.Fatal(err)
	}
	if got, err := mode.LandingTarget(ctx, w.project, "main", attrs); err != nil || got.ID != w.remote.ID {
		t.Fatalf("one host offered: %+v %v", got, err)
	}
	// An offer whose caps refuse the landing's deployment is no offer for it.
	spare, err := mode.Targets.Put(ctx, &Target{Name: "spare", Kind: KindDockerHost, Origin: OriginAttached, Status: TargetReady, Address: map[string]any{"host": "ssh://deploy@127.0.0.2", "keyFrom": "REMOTE_KEY"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := mode.Targets.Offer(ctx, spare, "*", OfferCaps{Data: []string{"production"}}, "boss"); err != nil {
		t.Fatal(err)
	}
	if got, err := mode.LandingTarget(ctx, w.project, "main", attrs); err != nil || got.ID != w.remote.ID {
		t.Fatalf("a capped offer counted: %+v %v", got, err)
	}
	if err := mode.Targets.Offer(ctx, spare, "*", OfferCaps{}, "boss"); err != nil {
		t.Fatal(err)
	}
	if _, err := mode.LandingTarget(ctx, w.project, "main", attrs); !errors.Is(err, ErrReleaseRefused) || !strings.Contains(err.Error(), "several attached Docker hosts are offered to game (remote, spare); create main on the one it should run on") {
		t.Fatalf("two hosts offered: %v", err)
	}

	// Outside the mode the built-in host takes it, offered or not.
	outside := w.service(false)
	builtIn, err := outside.Targets.Put(ctx, &Target{Name: "local", Kind: KindDockerHost, Origin: OriginBuiltIn, Status: TargetReady, Address: map[string]any{"daemon": "local"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := outside.LandingTarget(ctx, w.project, "main", attrs); err != nil || got.ID != builtIn.ID {
		t.Fatalf("outside the mode: %+v %v", got, err)
	}
}
