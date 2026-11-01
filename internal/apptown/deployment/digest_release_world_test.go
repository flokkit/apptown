package deployment

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/oci"
	"github.com/flokkit/apptown/stock"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// A digest bundle (design/subsystems/deployment.md, "Release bundles") as
// this package receives it: the deploy step's attempt publishes each
// image of a multipart app to the project's registry, and the bundle names
// every one by the digest the registry answers (PublishRelease). The
// registry is go-containerregistry's, in memory, behind a password; the
// Docker hosts are recording daemons that pull an image by digest from it
// with the credential in the pull's own configuration, as Docker does, and
// answer its RepoDigests. How a landing builds and publishes the bundle on a
// worker is the composition root's journey
// (TestALandingBuildsOnTheWorkerPublishesAndPullsByDigest).

const digestAppYAML = `app:
  web:
    public: true
    static: site
    ports: ["8080"]
  worker:
    build: worker
    environment:
      GREETING: hello
  cache:
    image: %[1]s/upstream/cache:7
init:
  - name: migrate
    build: migrations
  - name: seed
    image: %[1]s/upstream/seed:1
`

// digestOneImageYAML is an app App Platform runs: one image, so its spec
// names one version of the project's DOCR repository. App Platform refuses
// an app naming two versions of one repository (restore test RT-L3), which
// the multipart app's distinct images would be.
const digestOneImageYAML = `app:
  web:
    public: true
    static: site
    ports: ["8080"]
`

// The test registry's credential, and the platform the project releases for.
const (
	digestRegistryUser, digestRegistryToken = "robot", "registry-token-secret"
	digestPlatform                          = "linux/amd64"
)

// platformImage is one image for platform whose one layer is content.
func platformImage(t *testing.T, platform, content string) layoutFiles {
	t.Helper()
	files := layoutFiles{}
	put := func(b []byte) map[string]any {
		sum := sha256.Sum256(b)
		files["blobs/sha256/"+hex.EncodeToString(sum[:])] = b
		return map[string]any{"digest": "sha256:" + hex.EncodeToString(sum[:]), "size": len(b)}
	}
	encode := func(v any) []byte {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	osName, arch, _ := strings.Cut(platform, "/")
	layer := put([]byte(content))
	layer["mediaType"] = "application/vnd.oci.image.layer.v1.tar"
	config := put(encode(map[string]any{"architecture": arch, "os": osName, "rootfs": map[string]any{"type": "layers", "diff_ids": []any{layer["digest"]}}}))
	config["mediaType"] = "application/vnd.oci.image.config.v1+json"
	manifest := put(encode(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "config": config, "layers": []any{layer}}))
	manifest["mediaType"] = "application/vnd.oci.image.manifest.v1+json"
	files["index.json"] = encode(map[string]any{"schemaVersion": 2, "manifests": []any{manifest}})
	return files
}

// digestRegistry is a container registry in memory: it answers only its
// credential, and can be made unavailable or refusing.
type digestRegistry struct {
	host        string
	user, token string
	unavailable atomic.Bool
	refusing    atomic.Bool // answers every credential as wrong
	// onRequest, if set, sees each request before it is answered.
	onRequest atomic.Pointer[func(*http.Request)]
}

// newDigestRegistry is a registry in memory that answers only user and token.
func newDigestRegistry(t *testing.T, user, token string) *digestRegistry {
	t.Helper()
	r := &digestRegistry{user: user, token: token}
	inner := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if hook := r.onRequest.Load(); hook != nil {
			(*hook)(req)
		}
		if r.unavailable.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		if u, p, ok := req.BasicAuth(); !ok || u != r.user || p != r.token || r.refusing.Load() {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			http.Error(w, `{"errors":[{"code":"UNAUTHORIZED","message":"authentication required"}]}`, http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)
	r.host = strings.TrimPrefix(srv.URL, "http://")
	return r
}

func (r *digestRegistry) repository(path string) oci.Repository {
	return oci.Repository{Name: r.host + "/" + path, Credential: oci.Credential{Username: r.user, Password: r.token}}
}

// digestOf is the digest the registry answers for a reference, or "" when it
// has none.
func (r *digestRegistry) digestOf(t *testing.T, ref string) string {
	t.Helper()
	parsed, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	d, err := remote.Head(parsed, remote.WithAuth(&authn.Basic{Username: r.user, Password: r.token}))
	var te *transport.Error
	if errors.As(err, &te) && te.StatusCode == http.StatusNotFound {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return d.Digest.String()
}

// remove deletes an image from the registry by digest, as a registry's
// retention would.
func (r *digestRegistry) remove(t *testing.T, ref string) {
	t.Helper()
	d, err := name.NewDigest(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Delete(d, remote.WithAuth(&authn.Basic{Username: r.user, Password: r.token})); err != nil {
		t.Fatal(err)
	}
}

// digestHostOf is the Docker host an attached target is reached at ("" is
// AppTown's own).
func digestHostOf(target string) string {
	if target == "" {
		return ""
	}
	return "ssh://" + sshAlias(target)
}

// digestHosts are the Docker daemons by host: recording daemons that pull
// images by digest from the registry, with the credential written in the
// pull's own configuration, and hold them under their digests.
type digestHosts struct {
	t        *testing.T
	mu       sync.Mutex
	daemons  map[string]*dockerfake.Daemon
	held     map[string]map[string]bool // host -> references by digest it holds
	platform string                     // what `docker info` answers
	// told keeps the last release identity a `run` gave a static part
	// (APPTOWN_RELEASE): what AppTown's file server answers.
	told atomic.Pointer[string]
	// onPull, if set, sees each pull before it is answered.
	onPull atomic.Pointer[func(host string)]
}

func (h *digestHosts) daemon(target string) *dockerfake.Daemon {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.daemons[digestHostOf(target)]
}

func (h *digestHosts) Run(ctx context.Context, host string, req docker.Command) ([]byte, error) {
	h.mu.Lock()
	d := h.daemons[host]
	h.mu.Unlock()
	if d == nil {
		h.t.Errorf("docker %s was sent to %q, which is no host of this test", strings.Join(req.Args, " "), host)
		return nil, docker.ErrNoLocalBackend
	}
	args, config := req.Args, ""
	if len(args) >= 2 && args[0] == "--config" {
		config, args = args[1], args[2:]
	}
	out, err := d.Run(ctx, "", req) // recorded, as the daemon saw it
	switch {
	case len(args) >= 2 && args[0] == "context" && args[1] == "show":
		return []byte("default\n"), nil
	case len(args) >= 3 && args[0] == "info" && strings.Contains(args[2], "MemTotal"):
		return out, err // what the host can give containers: the daemon's answer
	case len(args) >= 1 && args[0] == "info":
		return []byte(h.platform + "\n"), nil
	case len(args) == 2 && args[0] == "pull":
		if hook := h.onPull.Load(); hook != nil {
			(*hook)(host)
		}
		return h.pull(host, config, args[1])
	case len(args) >= 3 && args[0] == "image" && args[1] == "inspect" && docker.IsDigestReference(args[len(args)-1]):
		ref := args[len(args)-1]
		h.mu.Lock()
		held := h.held[host][ref]
		h.mu.Unlock()
		if !held {
			return []byte("Error response from daemon: No such image: " + ref), errors.New("exit status 1")
		}
		return []byte(`sha256:built ["` + ref + `"]`), nil
	case len(args) > 0 && args[0] == "run":
		for i := 0; i+1 < len(args); i++ {
			if v, ok := strings.CutPrefix(args[i+1], appcontract.ReleaseEnvVar+"="); ok && args[i] == "-e" {
				h.told.Store(&v)
			}
		}
	}
	return out, err
}

// pull answers as Docker does: the registry's own refusal, or the image
// held under its digest.
func (h *digestHosts) pull(host, config, ref string) ([]byte, error) {
	failed := errors.New("exit status 1")
	d, err := name.NewDigest(ref)
	if err != nil {
		return []byte("invalid reference format"), failed
	}
	var cfg struct {
		Auths map[string]struct{ Auth string }
	}
	raw, _ := os.ReadFile(filepath.Join(config, "config.json"))
	_ = json.Unmarshal(raw, &cfg)
	var opts []remote.Option
	if a := cfg.Auths[d.RegistryStr()].Auth; a != "" {
		plain, _ := base64.StdEncoding.DecodeString(a)
		user, pass, _ := strings.Cut(string(plain), ":")
		opts = append(opts, remote.WithAuth(&authn.Basic{Username: user, Password: pass}))
	}
	desc, err := remote.Head(d, opts...)
	var te *transport.Error
	switch {
	case errors.As(err, &te) && te.StatusCode == http.StatusNotFound:
		return []byte("Error response from daemon: manifest for " + ref + " not found: manifest unknown: manifest unknown"), failed
	case errors.As(err, &te) && te.StatusCode == http.StatusUnauthorized:
		return []byte("Error response from daemon: Head \"https://" + d.RegistryStr() + "/v2/\": unauthorized: authentication required"), failed
	case err != nil:
		return []byte("Error response from daemon: " + err.Error()), failed
	case desc.Digest.String() != d.DigestStr():
		return []byte("Error response from daemon: the registry answered another digest"), failed
	}
	h.mu.Lock()
	if h.held[host] == nil {
		h.held[host] = map[string]bool{}
	}
	h.held[host][ref] = true
	h.mu.Unlock()
	return []byte("Status: Downloaded newer image for " + ref), nil
}

// forget removes every image a host holds (docker rmi).
func (h *digestHosts) forget(target string) {
	h.mu.Lock()
	delete(h.held, digestHostOf(target))
	h.mu.Unlock()
}

// commands are the commands a host was sent, each joined.
func (h *digestHosts) commands(target string) []string {
	var out []string
	for _, args := range h.daemon(target).Commands() {
		out = append(out, strings.Join(args, " "))
	}
	return out
}

// pulled, built and ran are the pulls, builds and container runs a host was
// asked for (the disk read's throwaway container is not one).
func (h *digestHosts) pulled(target string) []string {
	var out []string
	for _, c := range h.commands(target) {
		if ref, ok := strings.CutPrefix(c, "pull "); ok {
			out = append(out, ref)
		}
	}
	return out
}

func (h *digestHosts) built(target string) []string {
	var out []string
	for _, c := range h.commands(target) {
		if strings.HasPrefix(c, "build ") || strings.HasPrefix(c, "buildx ") {
			out = append(out, c)
		}
	}
	return out
}

func (h *digestHosts) ran(target string) []string {
	var out []string
	for _, args := range h.daemon(target).Commands() {
		if c := strings.Join(args, " "); strings.HasPrefix(c, "run ") && !h.daemon(target).IsDiskRead(args) {
			out = append(out, c)
		}
	}
	return out
}

// digestSources are project "p" and its repository's contract.
type digestSources struct {
	repo, yaml string
}

func (s digestSources) Project(_ context.Context, id ids.ID) (ReleaseProject, error) {
	return ReleaseProject{Application: Application{ID: id, Slug: "p"}, Repository: s.repo}, nil
}
func (s digestSources) Contract(_ context.Context, _ ReleaseProject, revision string) (*appcontract.Contract, string, error) {
	c, err := appcontract.Parse([]byte(s.yaml))
	return c, revision, err
}

// digestWorld is project "p", whose repository holds a multipart app (a
// static site, a built part, an image-only part, a built init step and an
// image-only one), with a container registry attached and two attached
// Docker hosts, "lab" and "lab2".
type digestWorld struct {
	db       *database.DB
	dir      string
	s        *Service
	registry *digestRegistry
	hosts    *digestHosts
	project  ids.ID
	repo     string
	contract []byte
	revision string
	lab      *Target
	lab2     *Target
	sources  digestSources
	projects *project.Projects
	// deps are the dependencies s was built with; another Service on the
	// same records is built from them.
	deps Dependencies
}

// newDigestWorld builds the world; configure adjusts its Service's
// Docker and provider settings before it is built (secrets are written to w.dir first).
func newDigestWorld(t *testing.T, secrets string, configure func(*digestWorld, *testRuntime)) *digestWorld {
	t.Helper()
	return newDigestWorldWith(t, secrets, digestAppYAML, configure)
}

// newDigestWorldWith is newDigestWorld whose repository's contract is
// appYAML (formatted with the registry's host).
func newDigestWorldWith(t *testing.T, secrets, appYAML string, configure func(*digestWorld, *testRuntime)) *digestWorld {
	t.Helper()
	db := testDB(t)
	w := &digestWorld{db: db, dir: t.TempDir(), registry: newDigestRegistry(t, digestRegistryUser, digestRegistryToken)}
	w.hosts = &digestHosts{t: t, platform: "linux x86_64", held: map[string]map[string]bool{},
		daemons: map[string]*dockerfake.Daemon{"": dockerfake.New(), digestHostOf("lab"): dockerfake.New(), digestHostOf("lab2"): dockerfake.New()}}
	w.project = createTestProject(t, db, "p", "P")
	// One front door for every daemon: AppTown's file server, which
	// answers /_apptown/release with the release its last container was told.
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == appcontract.ReleasePath {
			if v := w.hosts.told.Load(); v != nil {
				_, _ = rw.Write([]byte(*v))
			}
			return
		}
		_, _ = rw.Write([]byte("<html><body><script>/* app */</script></body></html>"))
	}))
	t.Cleanup(srv.Close)
	_, portText, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	for _, d := range w.hosts.daemons {
		d.SetPort(port)
	}
	// The attached hosts' SSH key lives in AppTown's secrets store.
	if err := os.WriteFile(filepath.Join(w.dir, "secrets.env"), []byte("LAB_KEY=-----BEGIN OPENSSH PRIVATE KEY-----\\nabc\\n-----END OPENSSH PRIVATE KEY-----\n"+secrets), 0o600); err != nil { // gitleaks:allow (test fixture, not a credential)
		t.Fatal(err)
	}
	w.repo = filepath.Join(w.dir, "workspaces", "project-"+w.project.String())
	w.contract = []byte(strings.ReplaceAll(appYAML, "%[1]s", w.registry.host))
	files := map[string]string{".apptown/app.yaml": string(w.contract), "site/index.html": "<html>p</html>",
		"worker/Dockerfile": "FROM scratch\n", "migrations/Dockerfile": "FROM scratch\n"}
	for name, body := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(w.repo, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(w.repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "release fixture"}} {
		cmd := gittest.Command(args...)
		cmd.Dir = w.repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	revision, err := releaseGit(t.Context(), w.repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	w.revision = strings.TrimSpace(revision)
	w.projects = project.New(db, nil)
	if err := w.projects.SetDefaultBranch(context.Background(), w.project, "main"); err != nil {
		t.Fatal(err)
	}
	secretStore := secret.NewService(db, secret.Config{DataDir: w.dir})
	integrations := integration.NewService(db, integration.Dependencies{Projects: w.projects, Secrets: secretStore})
	catalog := dependency.NewCatalog(db, nil)
	if _, _, err := catalog.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	w.sources = digestSources{repo: w.repo, yaml: string(w.contract)}
	mustExec(t, db, `CREATE TABLE test_release_queue(release_id UUID PRIMARY KEY REFERENCES releases(id))`)
	rt := testRuntime{Scope: docker.Scope{Client: w.hosts}, Probe: &docker.Probe{Settle: time.Millisecond}, DataDir: w.dir, SSHConfigPath: filepath.Join(w.dir, "ssh-config"), Projects: w.projects, Secrets: secretStore, Integrations: integrations, Needs: catalog, ChownImage: "test-helper", BuiltInTarget: "local"}
	if configure != nil {
		configure(w, &rt)
	}
	w.deps = assembled(db, rt, Dependencies{Authority: &requestAuthority{}, Followups: noFollowups{}, Configuration: ConfigurationDependencies{Authority: &requestAuthority{}, Projects: w.sources}, Releases: ReleaseDependencies{Authority: &requestAuthority{}, Still: &requestAuthority{}, Sources: w.sources, Secrets: doReleaseSecrets{}, Queue: requestQueue{}}})
	w.s = New(context.Background(), db, w.deps)
	if err := integrations.Attachments.Attach(t.Context(), w.project, "container-registry", "missing",
		fmt.Sprintf(`{"repo":%q,"username":%q,"token":%q}`, w.registry.host+"/team/p", digestRegistryUser, digestRegistryToken), "test", nil); err != nil {
		t.Fatal(err)
	}
	// The image-only declarations' images, in the project's own registry.
	for ref, content := range map[string]string{"upstream/cache:7": "cache", "upstream/seed:1": "seed"} {
		repo, tag, _ := strings.Cut(ref, ":")
		if _, err := oci.Publish(context.Background(), platformImage(t, digestPlatform, content), w.registry.repository(repo), tag, digestPlatform); err != nil {
			t.Fatal(err)
		}
	}
	w.lab = w.attach(t, "lab")
	w.lab2 = w.attach(t, "lab2")
	return w
}

// attach records an attached Docker host reached over SSH with the key in
// the secrets store.
func (w *digestWorld) attach(t *testing.T, name string) *Target {
	t.Helper()
	target, err := w.s.Targets.Put(context.Background(), &Target{Name: name, Kind: KindDockerHost, Origin: OriginAttached, Status: TargetReady,
		Address: map[string]any{"host": "ssh://root@127.0.0.1", "keyFrom": "LAB_KEY", "hostname": "127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	return target
}

// digestChange is the change whose landing published the bundle: its
// images are tagged change-<change>-<image>.
var digestChange = idstest.N(41)

// publish publishes the app at the repository's head as the deploy step's
// attempt does once the worker built its images, and answers the bundle.
func (w *digestWorld) publish(t *testing.T) *ReleaseBundle {
	t.Helper()
	c, err := appcontract.Parse(w.contract)
	if err != nil {
		t.Fatal(err)
	}
	plan := PlanRelease(c)
	built := map[string]oci.Layout{}
	for _, img := range plan.Images {
		built[img.Name] = platformImage(t, digestPlatform, "built/"+img.Name)
	}
	b, err := w.s.PublishRelease(context.Background(), ReleasePublication{By: &claimedAttempt{db: w.db, project: w.project, run: idstest.N(7), step: idstest.N(70), at: 1},
		ChangeID: digestChange, Tree: w.revision, Contract: w.contract, Plan: plan, Built: built, Platform: digestPlatform,
		Build: BundleBuild{Worker: "https://worker.invalid", Role: "agent"}})
	if err != nil || b == nil || !b.ByDigest() {
		t.Fatalf("the published bundle: %+v %v", b, err)
	}
	return b
}

// promote releases a bundle to a deployment named name on target, as a
// person promoting it does, and answers the release as recorded.
func (w *digestWorld) promote(t *testing.T, bundleID ids.ID, name string, target *Target) (*Release, error) {
	t.Helper()
	if _, err := w.s.Deployments.EnsureStanding(t.Context(), w.project, name, target.ID, Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	accepted, err := w.s.RequestRelease(context.Background(), DeploySelection{ProjectID: w.project, DeploymentName: name, BundleID: bundleID})
	if err != nil {
		t.Fatal(err)
	}
	execErr := w.s.ExecuteRelease(context.Background(), accepted.ID)
	rel, err := w.s.Releases.Last(context.Background(), w.project, name)
	if err != nil || rel.ID != accepted.ID {
		t.Fatal(err)
	}
	return rel, execErr
}

// rebuild is the recovery job for a deployment's current generation.
func (w *digestWorld) rebuild(t *testing.T, dep *Deployment) (map[string]any, error) {
	t.Helper()
	current, err := w.s.Deployments.ByID(context.Background(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	return JobWorker{Service: w.s, Projects: w.projects, DataDir: w.dir}.Rebuild(context.Background(), current.ProjectID.String()+"/"+dep.Name,
		map[string]any{"deployment": current.ID, "generation": current.ObservedGeneration})
}

// expectPulledOnly says a host ran the bundle: it pulled exactly the
// bundle's images by digest, and built nothing.
func expectPulledOnly(t *testing.T, w *digestWorld, target string, b *ReleaseBundle) {
	t.Helper()
	want := map[string]bool{}
	for _, ref := range b.Parts {
		want[ref] = true
	}
	for _, ref := range b.Init {
		want[ref] = true
	}
	got := map[string]bool{}
	for _, ref := range w.hosts.pulled(target) {
		got[ref] = true
	}
	for ref := range want {
		if !got[ref] {
			t.Errorf("%s did not pull %s; it pulled %v", target, ref, w.hosts.pulled(target))
		}
	}
	for ref := range got {
		if !want[ref] {
			t.Errorf("%s pulled %s, not an image of bundle %d", target, ref, b.ID)
		}
	}
	if built := w.hosts.built(target); len(built) != 0 {
		t.Errorf("%s built: %v", target, built)
	}
}
