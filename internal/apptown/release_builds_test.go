package apptown

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
	"github.com/flokkit/apptown/internal/deploy/digitalocean/dofake"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/execution/executionfake"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/oci"
	"github.com/flokkit/apptown/internal/worker/workertest"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// Release builds on the worker, through the deploy step as Work runs it. The worker is a fake
// that leaves an OCI layout for every image a release build declares; the
// registry is go-containerregistry's, in memory, behind a password; the
// Docker hosts are recording fakes that pull an image by digest from that
// registry with the credential in the pull's own configuration, as Docker
// does, and answer its RepoDigests. These are the landing's journeys, across
// Work, the deploy step, the worker's build and the deployment; what a
// published digest bundle does once promoted, recovered or tagged is the
// deployment package's (digest_release_test.go).

const releaseAppYAML = `app:
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

// releaseOneImageYAML is an app App Platform runs: one image, so its spec
// names one version of the project's DOCR repository. App Platform refuses
// an app naming two versions of one repository (restore test RT-L3), which
// the multipart app's distinct images would be.
const releaseOneImageYAML = `app:
  web:
    public: true
    static: site
    ports: ["8080"]
`

// The test registry's credential, and where the project's releases go.
const (
	registryUser, registryToken = "robot", "registry-token-secret"
	releasePlatform             = "linux/amd64"
)

// layoutFiles is an OCI image layout in memory, file by file (oci.Layout).
type layoutFiles map[string][]byte

func (f layoutFiles) Open(_ context.Context, name string) (io.ReadCloser, error) {
	b, ok := f[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

// imageLayout is one image for platform whose one layer is content.
func imageLayout(t *testing.T, platform, content string) layoutFiles {
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

// releaseRegistry is the project's container registry: it answers only its
// credential, and can be made unavailable.
type releaseRegistry struct {
	host        string
	user, token string
	unavailable atomic.Bool
	refusing    atomic.Bool // answers every credential as wrong
	// onRequest, if set, sees each request before it is answered.
	onRequest atomic.Pointer[func(*http.Request)]
}

func newReleaseRegistry(t *testing.T) *releaseRegistry {
	t.Helper()
	return newRegistry(t, registryUser, registryToken)
}

// newRegistry is a registry in memory that answers only user and token.
func newRegistry(t *testing.T, user, token string) *releaseRegistry {
	t.Helper()
	r := &releaseRegistry{user: user, token: token}
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

func (r *releaseRegistry) repository(path string) oci.Repository {
	return oci.Repository{Name: r.host + "/" + path, Credential: oci.Credential{Username: r.user, Password: r.token}}
}

// digestOf is the digest the registry answers for a reference, or "" when it
// has none.
func (r *releaseRegistry) digestOf(t *testing.T, ref string) string {
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

// releaseHosts are the Docker daemons by DOCKER_HOST ("" is AppTown's
// own): recording fakes that pull images by digest from the registry, with
// the credential written in the pull's own configuration.
type releaseHosts struct {
	t        *testing.T
	mu       sync.Mutex
	daemons  map[string]*fakeDocker
	held     map[string]map[string]bool // host -> references by digest it holds
	platform string                     // what `docker info` answers
	onPull   func(host, ref string)
	saved    map[string]string // image ID -> the manifest digest `docker save` packed
	// onCommand, if set, sees each command before it is answered.
	onCommand atomic.Pointer[func(host string, args []string)]
}

func hostOf(target string) string {
	if target == "" {
		return ""
	}
	return "ssh://apptown-target-" + target
}

func (h *releaseHosts) daemon(target string) *fakeDocker {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.daemons[hostOf(target)]
}

func (h *releaseHosts) Run(ctx context.Context, host string, req docker.Command) ([]byte, error) {
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
	if hook := h.onCommand.Load(); hook != nil {
		(*hook)(host, args)
	}
	if len(args) == 2 && args[0] == "save" && req.Stdout != nil {
		return nil, h.save(host, args[1], req.Stdout)
	}
	out, err := d.Run(ctx, "", req) // recorded, as the daemon saw it
	switch {
	case len(args) >= 2 && args[0] == "context" && args[1] == "show":
		return []byte("default\n"), nil
	case len(args) >= 1 && args[0] == "info":
		if len(args) == 3 && strings.Contains(args[2], "MemTotal") {
			return []byte("8589934592\t4\t/var/lib/docker\n"), nil
		}
		return []byte(h.platform + "\n"), nil
	case len(args) == 2 && args[0] == "pull":
		return h.pull(host, config, args[1])
	case len(args) == 5 && args[0] == "image" && args[1] == "inspect" && args[3] == "{{.Id}}" && strings.HasPrefix(args[4], "apptown-preview-build-"):
		// Each image a host built has its own ID, which `docker save`
		// packs and a container started from it runs.
		sum := sha256.Sum256([]byte(host + " " + args[4]))
		return []byte("sha256:" + hex.EncodeToString(sum[:]) + "\n"), nil
	case len(args) == 5 && args[0] == "image" && args[1] == "inspect" && args[3] == "{{.Id}}" && len(args[4]) == len("sha256:")+64 && strings.HasPrefix(args[4], "sha256:"):
		return []byte(args[4] + "\n"), nil // an image's ID names it
	case len(args) == 4 && args[0] == "inspect" && args[2] == docker.RuntimeFormat && err == nil:
		var runtime docker.Runtime
		if json.Unmarshal(out, &runtime) == nil {
			runtime.Image = d.ranImage(args[3], runtime.Image)
			return json.Marshal(runtime)
		}
	case len(args) >= 3 && args[0] == "image" && args[1] == "inspect" && docker.IsDigestReference(args[len(args)-1]):
		ref := args[len(args)-1]
		h.mu.Lock()
		held := h.held[host][ref]
		h.mu.Unlock()
		if !held {
			return []byte("Error response from daemon: No such image: " + ref), errFakeExit
		}
		return []byte(`sha256:built ["` + ref + `"]`), nil
	}
	return out, err
}

// pull answers as Docker does: the registry's own refusal, or the image
// held under its digest.
func (h *releaseHosts) pull(host, config, ref string) ([]byte, error) {
	d, err := name.ParseReference(ref)
	if err != nil {
		return []byte("invalid reference format"), errFakeExit
	}
	_, byDigest := d.(name.Digest)
	var cfg struct {
		Auths map[string]struct{ Auth string }
	}
	raw, _ := os.ReadFile(filepath.Join(config, "config.json"))
	_ = json.Unmarshal(raw, &cfg)
	var opts []remote.Option
	if a := cfg.Auths[d.Context().RegistryStr()].Auth; a != "" {
		plain, _ := base64.StdEncoding.DecodeString(a)
		user, pass, _ := strings.Cut(string(plain), ":")
		opts = append(opts, remote.WithAuth(&authn.Basic{Username: user, Password: pass}))
	}
	if h.onPull != nil {
		h.onPull(host, ref)
	}
	desc, err := remote.Head(d, opts...)
	var te *transport.Error
	switch {
	case errors.As(err, &te) && te.StatusCode == http.StatusNotFound:
		return []byte("Error response from daemon: manifest for " + ref + " not found: manifest unknown: manifest unknown"), errFakeExit
	case errors.As(err, &te) && te.StatusCode == http.StatusUnauthorized:
		return []byte("Error response from daemon: Head \"https://" + d.Context().RegistryStr() + "/v2/\": unauthorized: authentication required"), errFakeExit
	case err != nil:
		return []byte("Error response from daemon: " + err.Error()), errFakeExit
	case byDigest && desc.Digest.String() != d.Identifier():
		return []byte("Error response from daemon: the registry answered another digest"), errFakeExit
	}
	h.mu.Lock()
	if h.held[host] == nil {
		h.held[host] = map[string]bool{}
	}
	h.held[host][ref] = true
	h.mu.Unlock()
	return []byte("Status: Downloaded newer image for " + ref), nil
}

// ranImage is the image ID container was last run from, or was when it
// was run from none of the built images' own IDs.
func (f *fakeDocker) ranImage(container, was string) string {
	for _, c := range slices.Backward(f.joined()) {
		if !strings.HasPrefix(c, "run ") || containerNameOf(c) != container {
			continue
		}
		for _, arg := range strings.Fields(c) {
			if strings.HasPrefix(arg, "sha256:") && len(arg) == len("sha256:")+64 {
				return arg
			}
		}
		return was
	}
	return was
}

// save answers `docker save <id>` as Docker 25 and later do: the image's
// OCI layout in a tar, for the hosts' platform. It records the manifest
// digest it packed for the ID.
func (h *releaseHosts) save(host, id string, to io.Writer) error {
	osType, arch, _ := strings.Cut(h.platform, " ")
	platform, err := oci.Platform(osType, arch)
	if err != nil {
		return err
	}
	files := imageLayout(h.t, platform, "saved "+host+" "+id)
	var index struct{ Manifests []struct{ Digest string } }
	if err := json.Unmarshal(files["index.json"], &index); err != nil {
		return err
	}
	h.mu.Lock()
	if h.saved == nil {
		h.saved = map[string]string{}
	}
	h.saved[id] = index.Manifests[0].Digest
	h.mu.Unlock()
	w := tar.NewWriter(to)
	names := slices.Sorted(maps.Keys(files))
	for _, name := range names {
		if err := w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(files[name]))}); err != nil {
			return err
		}
		if _, err := w.Write(files[name]); err != nil {
			return err
		}
	}
	return w.Close()
}

// savedDigests are the manifest digests the hosts packed, by image ID.
func (h *releaseHosts) savedDigests() map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return maps.Clone(h.saved)
}

// pulled and built are the pulls and builds a host was asked for.
func (h *releaseHosts) pulled(target string) []string {
	var out []string
	for _, c := range h.daemon(target).joined() {
		if ref, ok := strings.CutPrefix(c, "pull "); ok {
			out = append(out, ref)
		}
	}
	return out
}

func (h *releaseHosts) built(target string) []string {
	var out []string
	for _, c := range h.daemon(target).joined() {
		if strings.HasPrefix(c, "build ") || strings.HasPrefix(c, "buildx ") {
			out = append(out, c)
		}
	}
	return out
}

// releaseWorker is AppTown's worker: tasks run on the in-memory fake, and
// a task declaring images leaves an OCI layout for each, for the
// worker's platform, until it is released.
type releaseWorker struct {
	*executionfake.Tasks
	t    *testing.T
	arch string

	mu       sync.Mutex
	sent     []execution.TaskSpec
	layouts  map[string]map[string]layoutFiles // task key -> image -> its layout
	released map[string]bool
	block    bool
	exit     int
	// open, if set, runs before each image file is served; an error is
	// the transfer's.
	open func(ctx context.Context, key, image, file string) error
}

func (w *releaseWorker) Capabilities() execution.Capabilities {
	return execution.Capabilities{Images: true, PrivateDocker: true}
}

func (w *releaseWorker) Ensure(ctx context.Context, spec execution.TaskSpec) (execution.TaskObservation, error) {
	if err := spec.Validate(time.Now()); err != nil {
		return execution.TaskObservation{}, err
	}
	platform, err := oci.Platform("linux", w.arch)
	if err != nil {
		platform = "linux/" + w.arch
	}
	w.mu.Lock()
	w.sent = append(w.sent, spec)
	if w.layouts[spec.Key] == nil {
		images := map[string]layoutFiles{}
		for _, img := range spec.Images {
			images[img.Name] = imageLayout(w.t, platform, spec.Key+"/"+img.Name)
		}
		w.layouts[spec.Key] = images
	}
	env := []string{"FAKE_EXIT=" + strconv.Itoa(w.exit), "FAKE_OUTPUT=building"}
	if w.block {
		env = append(env, "FAKE_BLOCK=1")
	}
	w.mu.Unlock()
	// The fake runs no program: it is given the request's identity, and
	// told how the build ends.
	inner := spec
	inner.Request, inner.Env = spec.Digest(), env
	inner.Images, inner.Private, inner.Source, inner.Files = nil, nil, nil, nil
	return w.Tasks.Ensure(ctx, inner)
}

func (w *releaseWorker) Release(ctx context.Context, ref execution.TaskRef) error {
	w.mu.Lock()
	w.released[ref.Key] = true
	w.mu.Unlock()
	return w.Tasks.Release(ctx, ref)
}

func (w *releaseWorker) ImageResults(ctx context.Context, ref execution.TaskRef) (map[string]execution.ImageCapture, error) {
	w.mu.Lock()
	released, images := w.released[ref.Key], w.layouts[ref.Key]
	w.mu.Unlock()
	if released {
		return nil, execution.ErrImageGone
	}
	if obs, err := w.Tasks.Observe(ctx, ref); err != nil || obs.State != execution.TaskExited {
		return nil, fmt.Errorf("the task has not exited: %+v %v", obs, err)
	}
	out := map[string]execution.ImageCapture{}
	for name := range images {
		out[name] = execution.ImageCapture{State: execution.ResultPresent, Size: 1}
	}
	return out, nil
}

func (w *releaseWorker) ImageFile(ctx context.Context, ref execution.TaskRef, image, file string) (io.ReadCloser, int64, error) {
	w.mu.Lock()
	released, files, open := w.released[ref.Key], w.layouts[ref.Key][image], w.open
	w.mu.Unlock()
	if released {
		return nil, 0, execution.ErrImageGone
	}
	if open != nil {
		if err := open(ctx, ref.Key, image, file); err != nil {
			return nil, 0, err
		}
	}
	b, ok := files[file]
	if !ok {
		return nil, 0, fs.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(b)), int64(len(b)), nil
}

func (w *releaseWorker) AgentImage(context.Context) (execution.AgentImage, error) {
	return execution.AgentImage{Name: "apptown-agent:fixture"}, nil
}

func (w *releaseWorker) Profile(context.Context) (execution.WorkerProfile, error) {
	return execution.WorkerProfile{Name: execution.ProfileWorkerVM, OS: "linux", Architecture: w.arch, Docker: "27.0", DindImage: "docker:27-dind"}, nil
}

func (w *releaseWorker) requests() []execution.TaskSpec {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]execution.TaskSpec(nil), w.sent...)
}

// releaseWorld is an AppTown with a worker and -private-docker whose project
// "p" has a container registry and its main on the attached Docker host
// "lab"; a second attached host, "lab2", runs its staging.
type releaseWorld struct {
	// appYAML is the contract a landing commits ("": releaseAppYAML, the
	// multipart app).
	appYAML string
	// files are more files a landing commits, by path.
	files    map[string]string
	a        *App
	worker   *releaseWorker
	registry *releaseRegistry
	hosts    *releaseHosts
	lab      *deployment.Target
	lab2     *deployment.Target
}

func newReleaseWorld(t *testing.T, configure func(*Config)) *releaseWorld {
	t.Helper()
	w := &releaseWorld{registry: newReleaseRegistry(t)}
	w.hosts = &releaseHosts{t: t, platform: "linux x86_64", held: map[string]map[string]bool{},
		daemons: map[string]*fakeDocker{"": newFakeDocker(), hostOf("lab"): newFakeDocker(), hostOf("lab2"): newFakeDocker()}}
	cfg := Config{Worker: workerClientConfig(t, workertest.NewCA(t), "https://worker.invalid"), PrivateDocker: true,
		DockerClient: w.hosts, DockerProbe: &docker.Probe{Settle: time.Millisecond}}
	if configure != nil {
		configure(&cfg)
	}
	w.a = testApp(t, cfg)
	w.a.SyncStockDependencies(context.Background())
	if id := createReadTestProject(t, w.a, "p"); id != idstest.N(1) {
		t.Fatalf("project fixture ID = %d, want 1", id)
	}
	// One front door for every daemon: AppTown's file server, which
	// answers /_apptown/release with the release its container was told.
	told := &atomic.Pointer[string]{}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == appcontract.ReleasePath {
			if v := told.Load(); v != nil {
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
		d.port, d.told = port, told
	}
	w.worker = &releaseWorker{Tasks: &executionfake.Tasks{}, t: t, arch: "x86_64", layouts: map[string]map[string]layoutFiles{}, released: map[string]bool{}}
	w.a.tasks.Driver, w.a.tasks.Poll = w.worker, 20*time.Millisecond
	if err := w.a.Integrations.Attachments.Attach(t.Context(), idstest.N(1), "container-registry", "missing",
		fmt.Sprintf(`{"repo":%q,"username":%q,"token":%q}`, w.registry.host+"/team/p", registryUser, registryToken), integration.Entered, "test", nil); err != nil {
		t.Fatal(err)
	}
	// The image-only declarations' images, in the project's own registry.
	for ref, content := range map[string]string{"upstream/cache:7": "cache", "upstream/seed:1": "seed"} {
		repo, tag, _ := strings.Cut(ref, ":")
		if _, err := oci.Publish(context.Background(), imageLayout(t, releasePlatform, content), w.registry.repository(repo), tag, releasePlatform); err != nil {
			t.Fatal(err)
		}
	}
	w.a.SeedTargets(t.Context())
	w.lab = attachTarget(t, w.a, "lab", "127.0.0.1")
	w.lab2 = attachTarget(t, w.a, "lab2", "127.0.0.1")
	return w
}

// mainOn makes the project's main a deployment on target, fed by landings.
func (w *releaseWorld) mainOn(t *testing.T, target *deployment.Target) {
	t.Helper()
	if _, err := w.a.Deployment.Deployments.EnsureStanding(t.Context(), idstest.N(1), "main", target.ID, deployment.Attributes{Feed: "landing", Audience: "team", DataClass: "synthetic"}); err != nil {
		t.Fatal(err)
	}
}

// restart is AppTown after a restart (restarted), configured as it was:
// the same worker, -private-docker and Docker hosts. The old AppTown admits
// nothing more; a step it was running is not waited for, as a dead
// process's is not.
func (w *releaseWorld) restart(t *testing.T) {
	t.Helper()
	w.a.Work.BeginStop()
	b := newApp(t, w.a.DB, Config{DataDir: w.a.cfg.dataDir, Worker: workerClientConfig(t, workertest.NewCA(t), "https://worker.invalid"), PrivateDocker: true,
		DockerClient: w.hosts, DockerProbe: &docker.Probe{Settle: time.Millisecond}})
	b.tasks.Driver, b.tasks.Profile, b.tasks.Backend, b.tasks.Poll = w.a.tasks.Driver, w.a.tasks.Profile, w.a.tasks.Backend, w.a.tasks.Poll
	if err := b.RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.ResumePendingAutoSteps(context.Background())
	w.a = b
}

// landingRun admits a landing of the multipart app, its deploy step not
// yet run.
func (w *releaseWorld) landingRun(t *testing.T) (*workflow.Run, ids.ID) {
	t.Helper()
	run, stepID := bootRun(t, w.a, "landing", "deploy")
	ws := requireLocation(t)(workflowSourceForTest(t, w.a, run).Workspace(w.a.cfg.dataDir))
	appYAML := w.appYAML
	if appYAML == "" {
		appYAML = releaseAppYAML
	}
	files := map[string]string{".apptown/app.yaml": strings.ReplaceAll(appYAML, "%[1]s", w.registry.host), "site/index.html": "<html>p</html>",
		"worker/Dockerfile": "FROM scratch\n", "migrations/Dockerfile": "FROM scratch\n"}
	for name, body := range w.files {
		files[name] = body
	}
	for name, body := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(ws, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ws, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "release fixture"}} {
		if out, err := gitexec.Run(t.Context(), ws, args...); err != nil {
			t.Fatalf("git fixture: %v: %s", err, out)
		}
	}
	// A promotion reads the bundle's commit from the project's repository.
	repo := filepath.Join(w.a.cfg.dataDir, "workspaces", "project-p")
	if out, err := gitexec.Run(t.Context(), filepath.Dir(repo), "clone", "-q", ws, repo); err != nil {
		t.Fatalf("git fixture: %v: %s", err, out)
	}
	return run, stepID
}

func (w *releaseWorld) step(t *testing.T, id ids.ID) *workflow.Step {
	t.Helper()
	step, err := workflow.ReadStepIn(context.Background(), w.a.DB, id)
	if err != nil {
		t.Fatal(err)
	}
	return step
}

// promote releases a bundle to a deployment on target, through the release
// owner, and answers the release as recorded.
func (w *releaseWorld) promote(t *testing.T, bundleID ids.ID, name string, target *deployment.Target) (*deployment.Release, error) {
	t.Helper()
	env, err := w.a.Deployment.Deployments.EnsureStanding(t.Context(), idstest.N(1), name, target.ID, deployment.Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.a.Deployment.Bundles.ByID(context.Background(), bundleID)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := acceptBundleForTest(t, w.a, w.a.projectByID(context.Background(), idstest.N(1)), env, b, "boss")
	if err != nil {
		t.Fatal(err)
	}
	execErr := w.a.Deployment.ExecuteRelease(context.Background(), accepted.ID)
	rel, err := w.a.Deployment.Releases.Last(context.Background(), idstest.N(1), name)
	if err != nil || rel.ID != accepted.ID {
		t.Fatal(err)
	}
	return rel, execErr
}

// expectPulledOnly says a host ran the bundle: it pulled exactly the
// bundle's images by digest, and built nothing.
func expectPulledOnly(t *testing.T, w *releaseWorld, target string, b *deployment.ReleaseBundle) {
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

// A multipart app (a static site, a built part, an image-only part, a
// built init step and an image-only one) lands on an attached Docker host:
// the worker builds each distinct image once, in one release-build task
// that carries no environment and no credential; AppTown publishes them
// and copies the image-only ones into the project's registry; the bundle
// committed before the deploy names every image by the digest the registry
// answers; the host pulls exactly those digests, holds them under them
// (RepoDigests) and builds nothing, with the static part's browser values
// delivered; the deployment's generation pins the bundle; and the task is
// settled and released.
func TestALandingBuildsOnTheWorkerPublishesAndPullsByDigest(t *testing.T) {
	t.Parallel()
	w := newReleaseWorld(t, nil)
	w.mainOn(t, w.lab)
	run, stepID := w.landingRun(t)
	w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), stepID)
	step := w.step(t, stepID)
	if step.Status != workflow.StatusPass {
		t.Fatalf("deploy step: %s %s\nlab: %s", step.Status, step.Evidence, strings.Join(w.hosts.daemon("lab").joined(), "\n"))
	}

	sent := w.worker.requests()
	if len(sent) != 1 {
		t.Fatalf("%d task requests, want the one release build", len(sent))
	}
	spec := sent[0]
	var images []string
	for _, img := range spec.Images {
		images = append(images, img.Name)
	}
	if strings.Join(images, ",") != "part-web,part-worker,init-migrate" {
		t.Errorf("the build declares %v, want each built image once", images)
	}
	raw, _ := json.Marshal(spec)
	if len(spec.Env) != 0 || len(spec.Inputs) != 0 || spec.Private == nil || !spec.Private.Daemon || len(spec.Private.Services) != 0 || strings.Contains(string(raw), registryToken) {
		t.Errorf("the release build carries more than its source and build files: %s", raw)
	}
	for _, f := range spec.Files {
		if bytes.Contains(f.Content, []byte(registryToken)) || bytes.Contains(f.Content, []byte(base64.StdEncoding.EncodeToString([]byte(registryUser+":"+registryToken)))) {
			t.Errorf("the release build's file %s carries the registry's credential", f.Path)
		}
	}
	script := strings.Join(spec.Argv, " ")
	for _, flag := range []string{"--platform", releasePlatform, "--provenance=false", "--sbom=false", "type=oci,dest=/apptown/state/out/part-web,tar=false", "/work/site", "/work/worker", "/work/migrations"} {
		if !strings.Contains(script, flag) {
			t.Errorf("the build script lacks %q:\n%s", flag, script)
		}
	}

	b, err := w.a.Deployment.Bundles.OfRun(context.Background(), idstest.N(1), run.ID)
	if err != nil || b == nil || !b.ByDigest() {
		t.Fatalf("the landing's bundle: %+v %v", b, err)
	}
	if b.BuiltFrom.Registry != w.registry.host+"/team/p" || b.BuiltFrom.Platform != releasePlatform || b.BuiltFrom.Build.Role != "agent" || b.BuiltFrom.StepID != stepID || b.BuiltFrom.Attempt != 1 {
		t.Errorf("the bundle's origin: %+v", b.BuiltFrom)
	}
	tagged := map[string]string{"web": "part-web", "worker": "part-worker", "cache": "part-cache"}
	for part, image := range tagged {
		answer := w.registry.digestOf(t, fmt.Sprintf("%s/team/p:change-%d-%s", w.registry.host, run.ChangeID, image))
		if answer == "" || b.Parts[part] != w.registry.host+"/team/p@"+answer {
			t.Errorf("the bundle's %s is %s; the registry answers %q", part, b.Parts[part], answer)
		}
	}
	if b.Init["migrate"] != w.registry.host+"/team/p@"+w.registry.digestOf(t, fmt.Sprintf("%s/team/p:change-%d-init-migrate", w.registry.host, run.ChangeID)) {
		t.Errorf("the bundle's migrate step is %s", b.Init["migrate"])
	}
	for ref, upstream := range map[string]string{b.Parts["cache"]: "upstream/cache:7", b.Init["seed"]: "upstream/seed:1"} {
		if digest := w.registry.digestOf(t, w.registry.host+"/"+upstream); !strings.HasSuffix(ref, "@"+digest) {
			t.Errorf("a copy changed its digest: %s, upstream %s", ref, digest)
		}
	}
	expectPulledOnly(t, w, "lab", b)
	if browser := w.hosts.daemon("lab").browser[mainNames(t, w.a).App()]; browser == "" {
		t.Errorf("the static part was not given its browser values")
	}
	if configs := w.hosts.daemon("lab").joined(); len(configs) == 0 {
		t.Fatal("nothing ran on the attached host")
	}

	env, err := w.a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main")
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := w.a.Deployment.Outcomes.InputsAtGeneration(context.Background(), env.ID, env.ObservedGeneration)
	if err != nil || inputs.BundleID != b.ID {
		t.Errorf("the generation's inputs %+v %v, want bundle %d pinned", inputs, err, b.ID)
	}
	var evidence struct{ Bundle ids.ID }
	if err := json.Unmarshal(step.Evidence, &evidence); err != nil || evidence.Bundle != b.ID {
		t.Errorf("the step's evidence names bundle %d, want %d: %s", evidence.Bundle, b.ID, step.Evidence)
	}
	attempt, err := tasks.New(w.a.DB).Get(context.Background(), spec.Key)
	if err != nil || attempt.Observed != tasks.Settled || !attempt.Released || attempt.Invocation != tasks.ReleaseBuildInvocation {
		t.Errorf("the release build's task: %+v %v", attempt, err)
	}
}

// What would stop a release is refused before anything is built or
// published, naming the next action: no registry, no -private-docker, main
// not on a Docker host, and a platform the target does not run or that is
// unknown.
func TestAReleaseBuildIsRefusedBeforeAnythingIsBuilt(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		prepare func(t *testing.T, w *releaseWorld)
		config  func(*Config)
		want    string
	}{
		{name: "no registry", prepare: func(t *testing.T, w *releaseWorld) {
			if err := w.a.Integrations.Attachments.Detach(context.Background(), idstest.N(1), "container-registry", "test"); err != nil {
				t.Fatal(err)
			}
		}, want: "this project has no container registry for its releases, and no container-registry connector is offered"},
		{name: "two registry connectors", prepare: func(t *testing.T, w *releaseWorld) {
			w.offerRegistry(t, "registry-a", "REGISTRY_PASSWORD")
			w.offerRegistry(t, "registry-b", "REGISTRY_PASSWORD")
		}, want: "several container-registry connectors are offered (registry-a, registry-b); attach the one p publishes to"},
		{name: "registry credential not held", prepare: func(t *testing.T, w *releaseWorld) {
			w.offerRegistry(t, "registry-a", "NOT_IN_THE_STORE")
		}, want: "names its credential NOT_IN_THE_STORE, which the secrets store does not hold"},
		{name: "no private docker", config: func(c *Config) { c.PrivateDocker = false }, want: "start AppTown with -private-docker"},
		{name: "main on portainer", prepare: func(t *testing.T, w *releaseWorld) {
			lan, err := w.a.Deployment.Targets.Put(context.Background(), &deployment.Target{Name: "lan", Kind: deployment.KindPortainer, Origin: deployment.OriginAttached, Status: deployment.TargetReady})
			if err != nil {
				t.Fatal(err)
			}
			w.mainOn(t, lan)
		}, want: "a landing deploys to a Docker host"},
		{name: "another platform", prepare: func(_ *testing.T, w *releaseWorld) { w.worker.arch = "aarch64" }, want: "the worker builds for linux/arm64 and lab runs linux/amd64"},
		{name: "unknown worker platform", prepare: func(_ *testing.T, w *releaseWorld) { w.worker.arch = "riscv64" }, want: "the worker's platform is unknown"},
		{name: "unknown target platform", prepare: func(_ *testing.T, w *releaseWorld) { w.hosts.platform = "linux " }, want: "the target's platform is unknown"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newReleaseWorld(t, c.config)
			if c.prepare != nil {
				c.prepare(t, w)
			}
			if _, err := w.a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main"); err != nil {
				w.mainOn(t, w.lab)
			}
			_, stepID := w.landingRun(t)
			w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), stepID)
			if step := w.step(t, stepID); step.Status != workflow.StatusError || !strings.Contains(string(step.Evidence), c.want) {
				t.Fatalf("deploy step: %s %s, want an error naming %q", step.Status, step.Evidence, c.want)
			}
			if sent := w.worker.requests(); len(sent) != 0 {
				t.Errorf("a refused release asked the worker for %d tasks", len(sent))
			}
			if b, err := w.a.Deployment.Bundles.Latest(context.Background(), idstest.N(1)); err != nil || b != nil {
				t.Errorf("a refused release committed %+v %v", b, err)
			}
			if pulled, built := w.hosts.pulled("lab"), w.hosts.built("lab"); len(pulled)+len(built) != 0 {
				t.Errorf("a refused release reached the host: %v %v", pulled, built)
			}
		})
	}
}

// offerRegistry replaces the project's container registry with none and
// offers the world's registry as the connector name, whose credential is
// the secret tokenFrom names: the store holds REGISTRY_PASSWORD.
func (w *releaseWorld) offerRegistry(t *testing.T, name, tokenFrom string) {
	t.Helper()
	if err := w.a.Integrations.Attachments.Detach(context.Background(), idstest.N(1), integration.CapContainerRegistry, "test"); err != nil && !errors.Is(err, integration.ErrNotAttached) {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(w.a.cfg.dataDir, "secrets.env"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\nREGISTRY_PASSWORD=" + registryToken + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`{"url":%q,"group":"team","provider":"registry","username":%q,"tokenFrom":[%q]}`, w.registry.host, registryUser, tokenFrom)
	if _, err := w.a.Integrations.Bindings.Upsert(t.Context(), name, integration.CapContainerRegistry, "service", config, false); err != nil {
		t.Fatal(err)
	}
}

// A project with no container registry, on an AppTown that offers one
// container-registry connector, is given it at its first landing: the
// attachment is recorded by the integrations owner like any other, names
// <url>/<group>/<slug>, and holds no credential. The connector's
// credential is read from the secrets store, and the release is published
// with it.
func TestAFirstLandingAttachesTheOneOfferedRegistry(t *testing.T) {
	t.Parallel()
	w := newReleaseWorld(t, nil)
	w.offerRegistry(t, "registry-a", "REGISTRY_PASSWORD")
	w.mainOn(t, w.lab)
	run, stepID := w.landingRun(t)
	w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), stepID)
	if step := w.step(t, stepID); step.Status != workflow.StatusPass {
		t.Fatalf("deploy step: %s %s", step.Status, step.Evidence)
	}
	rows, err := integration.AttachedRows(context.Background(), w.a.DB, idstest.N(1))
	if err != nil {
		t.Fatal(err)
	}
	var attached *integration.Attachment
	for i := range rows {
		if rows[i].Capability == integration.CapContainerRegistry {
			attached = &rows[i]
		}
	}
	if attached == nil || attached.Binding != "registry-a" || attached.Provenance != string(integration.Provisioned) {
		t.Fatalf("the project's container registry: %+v", attached)
	}
	var resource map[string]any
	if err := json.Unmarshal([]byte(attached.ResourceJSON), &resource); err != nil || len(resource) != 1 || resource["repo"] != w.registry.host+"/team/p" {
		t.Errorf("the attachment holds %s, want only the repository %s/team/p", attached.ResourceJSON, w.registry.host)
	}
	b, err := w.a.Deployment.Bundles.OfRun(context.Background(), idstest.N(1), run.ID)
	if err != nil || b == nil || b.BuiltFrom.Registry != w.registry.host+"/team/p" {
		t.Fatalf("the landing's bundle: %+v %v", b, err)
	}
	if w.registry.digestOf(t, fmt.Sprintf("%s/team/p:change-%d-part-web", w.registry.host, run.ChangeID)) == "" {
		t.Error("the release was not published to the attached registry")
	}
}

// A project that has a container registry keeps it, whatever AppTown
// offers: the landing publishes to the attachment it has.
func TestALandingKeepsAnAttachedRegistry(t *testing.T) {
	t.Parallel()
	w := newReleaseWorld(t, nil)
	before, _, err := w.a.Integrations.Attachments.Attached(context.Background(), idstest.N(1), integration.CapContainerRegistry)
	if err != nil {
		t.Fatal(err)
	}
	config := `{"url":"other.invalid","group":"elsewhere","tokenFrom":["REGISTRY_PASSWORD"]}`
	if _, err := w.a.Integrations.Bindings.Upsert(t.Context(), "registry-a", integration.CapContainerRegistry, "service", config, false); err != nil {
		t.Fatal(err)
	}
	w.mainOn(t, w.lab)
	_, stepID := w.landingRun(t)
	w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), stepID)
	if step := w.step(t, stepID); step.Status != workflow.StatusPass {
		t.Fatalf("deploy step: %s %s", step.Status, step.Evidence)
	}
	binding, resource, err := w.a.Integrations.Attachments.Attached(context.Background(), idstest.N(1), integration.CapContainerRegistry)
	if err != nil || binding != before || !strings.Contains(resource, registryToken) || !strings.Contains(resource, w.registry.host+"/team/p") {
		t.Errorf("the manual attachment changed: %s %s %v", binding, resource, err)
	}
}

// The built-in host, outside the no-local-Docker mode, pulls a landing's
// bundle by digest too: an AppTown with a worker builds nothing on any host.
func TestTheBuiltInHostPullsALandingsBundleToo(t *testing.T) {
	t.Parallel()
	w := newReleaseWorld(t, nil)
	builtIn, err := w.a.Deployment.Targets.ByName(t.Context(), deployment.BuiltInTargetName)
	if err != nil {
		t.Fatal(err)
	}
	w.mainOn(t, builtIn)
	run, stepID := w.landingRun(t)
	w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), stepID)
	if step := w.step(t, stepID); step.Status != workflow.StatusPass {
		t.Fatalf("deploy step: %s %s", step.Status, step.Evidence)
	}
	b, err := w.a.Deployment.Bundles.OfRun(context.Background(), idstest.N(1), run.ID)
	if err != nil || b == nil || !b.ByDigest() {
		t.Fatalf("the bundle: %+v %v", b, err)
	}
	expectPulledOnly(t, w, "", b)
	if calls := w.hosts.daemon("lab").joined(); len(calls) != 0 {
		t.Errorf("a landing on the built-in host reached lab: %v", calls)
	}
}

// awaitDeployStep waits for a deploy step run in the background to settle.
func awaitDeployStep(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("the deploy step did not return")
	}
}

// Interrupting the step during a layer's transfer, or just before the
// commit, commits no bundle and deploys nothing; no image is written after
// the interruption is seen, and the build's task is settled and released.
// (A step superseded between images is the deployment owner's test:
// TestAPublicationStopsWhenItsAttemptIsSuperseded.)
func TestAnInterruptedPublicationCommitsNoBundleAndDeploysNothing(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, when string }{
		{"interrupted during a layer", "layer"},
		{"interrupted before the commit", "commit"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newReleaseWorld(t, nil)
			w.mainOn(t, w.lab)
			run, stepID := w.landingRun(t)
			var once sync.Once
			end := func() {
				once.Do(func() {
					if _, err := w.a.Work.Abandon(asPerson("bls"), workflow.StopWork{ChangeID: run.ChangeID, Reason: "interrupting the release"}); err != nil {
						t.Error(err)
					}
				})
			}
			w.worker.open = func(ctx context.Context, _, image, file string) error {
				switch {
				case c.when == "layer" && image == "part-worker" && strings.HasPrefix(file, "blobs/"):
					end()
					<-ctx.Done() // the transfer stalls until the step's watch ends it
					return ctx.Err()
				}
				return nil
			}
			if c.when == "commit" {
				// The registry reads back the last image (a copy) as written:
				// the step is interrupted then, and the commit after it is
				// refused by the fence. The watch is slowed so that it does
				// not end the read-back first.
				seed := w.registry.digestOf(t, w.registry.host+"/upstream/seed:1")
				w.a.tasks.Poll = time.Hour
				hook := func(req *http.Request) {
					if req.Method == http.MethodHead && strings.HasSuffix(req.URL.Path, "/team/p/manifests/"+seed) {
						end()
					}
				}
				w.registry.onRequest.Store(&hook)
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), stepID)
			}()
			awaitDeployStep(t, done)
			w.registry.onRequest.Store(nil)
			if b, err := w.a.Deployment.Bundles.Latest(context.Background(), idstest.N(1)); err != nil || b != nil {
				t.Errorf("an interrupted publication committed %+v %v", b, err)
			}
			if pulled, runs := w.hosts.pulled("lab"), containersRun(w.hosts.daemon("lab")); len(pulled)+len(runs) != 0 {
				t.Errorf("an interrupted publication deployed: %v %v", pulled, runs)
			}
			// Whatever an image was tagged.
			repo, err := name.NewRepository(w.registry.host + "/team/p")
			if err != nil {
				t.Fatal(err)
			}
			tags, err := remote.List(repo, remote.WithAuth(&authn.Basic{Username: registryUser, Password: registryToken}))
			if err != nil {
				t.Fatal(err)
			}
			tag := func(image string) string {
				for _, tg := range tags {
					if tg == fmt.Sprintf("change-%d-%s", run.ChangeID, image) {
						return tg
					}
				}
				return ""
			}
			switch c.when {
			case "layer":
				// Parts publish in the contract's order (cache, web, worker),
				// then init steps.
				for _, image := range []string{"part-worker", "init-migrate", "init-seed"} {
					if got := tag(image); got != "" {
						t.Errorf("%s was written after the interruption: %s", image, got)
					}
				}
			case "commit":
				if tag("init-seed") == "" {
					t.Error("the interruption was not just before the commit: the last image was not written")
				}
			}
			key := fmt.Sprintf("step-%d-attempt-1-release-build", stepID)
			if attempt, err := tasks.New(w.a.DB).Get(context.Background(), key); err != nil || attempt.Observed != tasks.Settled || !attempt.Released {
				t.Errorf("the release build's task: %+v %v", attempt, err)
			}
		})
	}
}

// Interrupting the step after its bundle is committed leaves the bundle
// valid: it is promoted. The deploy under way follows its own attempt.
func TestABundleCommittedBeforeAnInterruptionStaysPromotable(t *testing.T) {
	t.Parallel()
	w := newReleaseWorld(t, nil)
	w.mainOn(t, w.lab)
	run, stepID := w.landingRun(t)
	var once sync.Once
	w.hosts.onPull = func(host, _ string) {
		if host == hostOf("lab") {
			once.Do(func() {
				if _, err := w.a.Work.Abandon(asPerson("bls"), workflow.StopWork{ChangeID: run.ChangeID, Reason: "interrupting after the commit"}); err != nil {
					t.Error(err)
				}
			})
		}
	}
	w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), stepID)
	w.hosts.onPull = nil
	b, err := w.a.Deployment.Bundles.OfRun(context.Background(), idstest.N(1), run.ID)
	if err != nil || b == nil || !b.ByDigest() {
		t.Fatalf("the committed bundle: %+v %v", b, err)
	}
	env, err := w.a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main")
	if err != nil {
		t.Fatal(err)
	}
	if busy, err := w.a.Deployment.Outcomes.HasOpenAttempt(context.Background(), env.ID); err != nil || busy {
		t.Errorf("the deploy under way left its attempt open: %v %v", busy, err)
	}
	rel, err := w.promote(t, b.ID, "staging", w.lab2)
	if err != nil || rel.State != deployment.Released {
		t.Fatalf("promoting the bundle: %+v %v", rel, err)
	}
	expectPulledOnly(t, w, "lab2", b)
}

// An AppTown restart while the release builds, or once it has exited and its
// images are being published, ends the deploy step in error, and nothing
// continues it: its task is stopped, settled and released, and no new
// attempt starts. Run again, the step builds once more and commits one new
// bundle.
func TestARestartDuringTheBuildEndsTheStepAndARetryCommitsOneBundle(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"started", "exited"} {
		t.Run(stage, func(t *testing.T) {
			w := newReleaseWorld(t, nil)
			w.mainOn(t, w.lab)
			run, stepID := w.landingRun(t)
			w.a.tasks.AbandonAt = func(at string) bool { return at == stage }
			w.worker.block = stage == "started"
			w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), stepID)
			if step := w.step(t, stepID); step.Status != workflow.StatusRunning {
				t.Fatalf("the step AppTown died in: %s", step.Status)
			}
			key := fmt.Sprintf("step-%d-attempt-1-release-build", stepID)
			w.restart(t)
			step := w.step(t, stepID)
			if step.Status != workflow.StatusError || !strings.Contains(string(step.Evidence), workflow.EndedByRestart) || step.Attempt != 1 {
				t.Fatalf("the step after a restart: %s attempt %d %s", step.Status, step.Attempt, step.Evidence)
			}
			attempt, err := tasks.New(w.a.DB).Get(context.Background(), key)
			if err != nil || attempt.Observed != tasks.Settled || !attempt.Released || w.worker.Present(key) {
				t.Errorf("the interrupted build's task: %+v %v (still on the worker: %v)", attempt, err, w.worker.Present(key))
			}
			if sent := w.worker.requests(); len(sent) != 1 {
				t.Errorf("the restart asked the worker for %d builds, want none beyond the first", len(sent)-1)
			}
			if bundle, err := w.a.Deployment.Bundles.Latest(context.Background(), idstest.N(1)); err != nil || bundle != nil {
				t.Errorf("an interrupted build committed %+v %v", bundle, err)
			}

			w.worker.mu.Lock()
			w.worker.block = false
			w.worker.mu.Unlock()
			if _, err := w.a.Work.RetryStep(asPerson("bls"), workflow.RetryCommand{StepID: stepID}); err != nil {
				t.Fatal(err)
			}
			awaitStep(t, w.a, stepID, workflow.StatusPass)
			bundle, err := w.a.Deployment.Bundles.OfRun(context.Background(), idstest.N(1), run.ID)
			if err != nil || bundle == nil || bundle.BuiltFrom.Attempt != 2 {
				t.Fatalf("the retry's bundle: %+v %v", bundle, err)
			}
			if first, err := w.a.Deployment.Bundles.Latest(context.Background(), idstest.N(1)); err != nil || first.ID != bundle.ID {
				t.Errorf("the newest bundle is %+v, not the retry's %d (%v)", first, bundle.ID, err)
			}
		})
	}
}

// A restart after the Docker deploy settled, before its step did, ends the
// step in error, applies nothing again, and leaves the bundle promotable.
func TestARestartAfterTheDeploySettledAppliesNothingAgain(t *testing.T) {
	t.Parallel()
	w := newReleaseWorld(t, nil)
	w.mainOn(t, w.lab)
	mainApp := mainNames(t, w.a).App()
	run, stepID := w.landingRun(t)
	// The landing's evidence is read from the host once its deployment has
	// settled: AppTown dies there, before its step settles.
	died, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	hook := func(host string, args []string) {
		if host == hostOf("lab") && strings.Join(args, " ") == "inspect -f {{.Config.Image}} "+mainApp {
			once.Do(func() { close(died); <-resume })
		}
	}
	w.hosts.onCommand.Store(&hook)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), stepID)
	}()
	select {
	case <-died:
	case <-done:
		t.Fatalf("the deploy step ended before its deployment settled: %s", w.step(t, stepID).Evidence)
	}
	w.hosts.onCommand.Store(nil)
	env, err := w.a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main")
	if err != nil || env.ObservedGeneration != 1 {
		t.Fatalf("main had not settled: %+v %v", env, err)
	}
	runsBefore, sentBefore := len(containersRun(w.hosts.daemon("lab"))), len(w.worker.requests())
	w.restart(t)
	step := w.step(t, stepID)
	if step.Status != workflow.StatusError || !strings.Contains(string(step.Evidence), workflow.EndedByRestart) {
		t.Fatalf("the step after a restart: %s %s", step.Status, step.Evidence)
	}
	// Whatever the restart dispatched has finished, so anything it applied
	// again would show below.
	drained(t, w.a)
	if runs, sent := len(containersRun(w.hosts.daemon("lab"))), len(w.worker.requests()); runs != runsBefore || sent != sentBefore {
		t.Errorf("the restart applied again: %d containers run (was %d), %d builds (was %d)", runs, runsBefore, sent, sentBefore)
	}
	if after, err := w.a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main"); err != nil || after.ObservedGeneration != env.ObservedGeneration {
		t.Errorf("main after the restart: %+v %v", after, err)
	}
	// The dead AppTown's step goroutine ends; its outcome is no longer the
	// step's.
	close(resume)
	awaitDeployStep(t, done)
	if step := w.step(t, stepID); step.Status != workflow.StatusError {
		t.Errorf("the dead forge's late outcome replaced the restart's: %s %s", step.Status, step.Evidence)
	}
	bundle, err := w.a.Deployment.Bundles.OfRun(context.Background(), idstest.N(1), run.ID)
	if err != nil || bundle == nil {
		t.Fatal(bundle, err)
	}
	if rel, err := w.promote(t, bundle.ID, "staging", w.lab2); err != nil || rel.State != deployment.Released {
		t.Fatalf("promoting the earlier bundle: %+v %v", rel, err)
	}
}

// In the no-local-Docker mode a landing's digest bundle lands on an
// attached host and is promoted to another: its images are in its
// registry, bound to no target. The built-in host stays refused.
func TestInTheNoLocalDockerModeADigestBundleGoesToAnyAttachedHost(t *testing.T) {
	t.Parallel()
	w := newReleaseWorld(t, func(c *Config) { c.NoLocalDocker = true })
	w.mainOn(t, w.lab)
	run, stepID := w.landingRun(t)
	w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), stepID)
	if step := w.step(t, stepID); step.Status != workflow.StatusPass {
		t.Fatalf("deploy step: %s %s", step.Status, step.Evidence)
	}
	b, err := w.a.Deployment.Bundles.OfRun(context.Background(), idstest.N(1), run.ID)
	if err != nil || b == nil || !b.BuiltFrom.TargetID.IsZero() {
		t.Fatalf("the bundle: %+v %v", b, err)
	}
	if rel, err := w.promote(t, b.ID, "staging", w.lab2); err != nil || rel.State != deployment.Released {
		t.Fatalf("promoting to another attached host: %+v %v", rel, err)
	}
	expectPulledOnly(t, w, "lab2", b)
}

// A release build that fails fails the step with the end of its output;
// nothing is published or deployed, and its task is settled and released.
func TestAFailedReleaseBuildFailsTheStepAndPublishesNothing(t *testing.T) {
	t.Parallel()
	w := newReleaseWorld(t, nil)
	w.mainOn(t, w.lab)
	run, stepID := w.landingRun(t)
	w.worker.exit = 2
	w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), stepID)
	step := w.step(t, stepID)
	if step.Status != workflow.StatusFail || !strings.Contains(string(step.Evidence), "the release build failed (exit 2)") || !strings.Contains(string(step.Evidence), "building") {
		t.Fatalf("deploy step: %s %s", step.Status, step.Evidence)
	}
	if b, err := w.a.Deployment.Bundles.OfRun(context.Background(), idstest.N(1), run.ID); err != nil || b != nil {
		t.Errorf("a failed build committed %+v %v", b, err)
	}
	if digest := w.registry.digestOf(t, fmt.Sprintf("%s/team/p:change-%d-part-web", w.registry.host, run.ChangeID)); digest != "" {
		t.Errorf("a failed build published %s", digest)
	}
	if pulled, runs := w.hosts.pulled("lab"), containersRun(w.hosts.daemon("lab")); len(pulled)+len(runs) != 0 {
		t.Errorf("a failed build deployed: %v %v", pulled, runs)
	}
	key := fmt.Sprintf("step-%d-attempt-1-release-build", stepID)
	if attempt, err := tasks.New(w.a.DB).Get(context.Background(), key); err != nil || attempt.Observed != tasks.Settled || !attempt.Released || attempt.ExitCode == nil || *attempt.ExitCode != 2 {
		t.Errorf("the failed build's task: %+v %v", attempt, err)
	}
}

// Deleting a project removes its release images from every Docker host it
// had a deployment on. A host pulled them by digest and holds them under
// no build-image name, so the cleanup names each by its digest reference:
// never forced, and one a container still uses is kept without failing
// the cleanup.
func TestDeletingAProjectRemovesItsReleaseImagesFromEachHost(t *testing.T) {
	t.Parallel()
	w := newReleaseWorld(t, nil)
	testAdministrator(t, w.a, "boss")
	// A change shipped before the bundle is cut, so each release of the
	// bundle carries it and the change's page lists both releases.
	change, err := workflow.NewCreator(w.a.DB, func(context.Context, workflow.CreatedChange) error { return nil }).Create(context.Background(), workflow.ChangeDraft{ProjectID: idstest.N(1), Type: "feature", Title: "Shipped"}, "boss")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.a.DB.InTx(context.Background(), func(tx *database.Tx) error {
		_, err := workflow.TransitionChangeIn(context.Background(), tx, idstest.N(1), change.ID, workflow.ChangeMove{Action: workflow.ShipChange}, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	w.mainOn(t, w.lab)
	run, stepID := w.landingRun(t)
	w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), stepID)
	b, err := w.a.Deployment.Bundles.OfRun(context.Background(), idstest.N(1), run.ID)
	if err != nil || b == nil || !b.ByDigest() {
		t.Fatalf("the landing: %s %v", w.step(t, stepID).Evidence, err)
	}
	if rel, err := w.promote(t, b.ID, "staging", w.lab2); err != nil || rel.State != deployment.Released {
		t.Fatalf("promotion: %+v %v", rel, err)
	}
	if _, err := w.a.Work.Abandon(asPerson("boss"), workflow.StopWork{RunID: run.ID, Reason: "the project is being deleted"}); err != nil {
		t.Fatal(err)
	}
	// The fixture's repository is a clone of the run's workspace; that
	// origin is no GitLab copy for the deletion to archive.
	if out, err := gitexec.Run(t.Context(), filepath.Join(w.a.cfg.dataDir, "workspaces", "project-p"), "remote", "remove", "origin"); err != nil {
		t.Fatalf("git fixture: %v: %s", err, out)
	}
	inUse := b.Parts["web"]
	lab2 := w.hosts.daemon("lab2")
	lab2.mu.Lock()
	lab2.reply = func(args []string) (string, error, bool) {
		if strings.Join(args, " ") == "rmi "+inUse {
			return "Error response from daemon: conflict: unable to delete " + inUse + " (cannot be forced) - image is being used by running container 0123", errFakeExit, true
		}
		return "", nil, false
	}
	lab2.mu.Unlock()
	if st := deleteProject(t, w.a, "p"); st.State != project.Deleted {
		t.Fatalf("delete: %+v", st)
	}
	if op := purgeProject(t, w.a, "p"); op.State != operations.Succeeded {
		t.Fatalf("purge: %s %q", op.State, op.Detail)
	}
	runCleanup(t, w.a, idstest.N(1), 0)
	// The world's worker is not reachable over HTTP, so the previews it
	// would remove keep the round pending; no image's removal is a failure.
	if d, err := w.a.Projects.Deletion(context.Background(), idstest.N(1)); err != nil || strings.Contains(d.Detail, "rmi") {
		t.Fatalf("cleanup: %s %v", d.Detail, err)
	}
	want := map[string]bool{}
	for _, m := range []map[string]string{b.Parts, b.Init} {
		for _, ref := range m {
			want[ref] = true
		}
	}
	for _, host := range []string{"lab", "lab2"} {
		removed := map[string]bool{}
		for _, call := range w.hosts.daemon(host).joined() {
			if ref, ok := strings.CutPrefix(call, "rmi "); ok {
				if strings.Contains(ref, "-f") || strings.Contains(ref, "--force") {
					t.Errorf("%s: an image was forced: %s", host, call)
				}
				removed[ref] = true
			}
		}
		for ref := range want {
			if !removed[ref] {
				t.Errorf("%s: the release image %s was not removed; rmi calls %v", host, ref, removed)
			}
		}
	}
}

// A release serves only on the deployment it was recorded on. Deleting a
// deployment and making it again under the same name is another
// deployment: when it releases the same bundle, the release of the deleted
// one stays taken down, in the release list and on the change, and the new
// release is the one serving.
func TestARecreatedDeploymentDoesNotReviveATakenDownRelease(t *testing.T) {
	t.Parallel()
	w := newReleaseWorld(t, nil)
	testAdministrator(t, w.a, "boss")
	// A change shipped before the bundle is cut, so each release of the
	// bundle carries it and the change's page lists both releases.
	change, err := workflow.NewCreator(w.a.DB, func(context.Context, workflow.CreatedChange) error { return nil }).Create(context.Background(), workflow.ChangeDraft{ProjectID: idstest.N(1), Type: "feature", Title: "Shipped"}, "boss")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.a.DB.InTx(context.Background(), func(tx *database.Tx) error {
		_, err := workflow.TransitionChangeIn(context.Background(), tx, idstest.N(1), change.ID, workflow.ChangeMove{Action: workflow.ShipChange}, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	w.mainOn(t, w.lab)
	run, stepID := w.landingRun(t)
	w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), stepID)
	if step := w.step(t, stepID); step.Status != workflow.StatusPass {
		t.Fatalf("deploy step: %s %s", step.Status, step.Evidence)
	}
	b, err := w.a.Deployment.Bundles.OfRun(context.Background(), idstest.N(1), run.ID)
	if err != nil || b == nil {
		t.Fatal(b, err)
	}
	first, err := w.promote(t, b.ID, "review", w.lab2)
	if err != nil || first.State != deployment.Released {
		t.Fatalf("first promotion: %+v %v", first, err)
	}
	if out := authzCall(w.a.Handler(), "boss", "DELETE", "/api/v1/projects/p/deployments/review", ""); out.Code != 200 {
		t.Fatalf("delete: %d %s", out.Code, out.Body.String())
	}
	second, err := w.promote(t, b.ID, "review", w.lab2)
	if err != nil || second.State != deployment.Released || second.DeploymentID == first.DeploymentID {
		t.Fatalf("second promotion: %+v %v", second, err)
	}
	out := authzCall(w.a.Handler(), "boss", "GET", "/api/v1/projects/p/releases", "")
	var listed []struct {
		ID      ids.ID
		Serving bool
	}
	if err := json.Unmarshal(out.Body.Bytes(), &listed); err != nil || out.Code != 200 {
		t.Fatalf("releases: %d %s", out.Code, out.Body.String())
	}
	serving := map[ids.ID]bool{}
	for _, r := range listed {
		serving[r.ID] = r.Serving
	}
	if serving[first.ID] || !serving[second.ID] {
		t.Fatalf("taken-down release %d serving %v, its replacement %d serving %v", first.ID, serving[first.ID], second.ID, serving[second.ID])
	}
	out = authzCall(w.a.Handler(), "boss", "GET", fmt.Sprintf("/api/v1/changes/%d", change.ID), "")
	var view struct {
		Evidence struct {
			Releases []struct {
				Number     int64
				Deployment string
				Serving    bool
			}
		}
	}
	if err := json.Unmarshal(out.Body.Bytes(), &view); err != nil || out.Code != 200 {
		t.Fatalf("change: %d %s", out.Code, out.Body.String())
	}
	onChange := map[int64]bool{}
	for _, r := range view.Evidence.Releases {
		onChange[r.Number] = r.Serving
	}
	if len(onChange) != 2 || onChange[first.Number] || !onChange[second.Number] {
		t.Fatalf("the change's releases: %s", out.Body.String())
	}
}

// What a person reads of a landing's release on App Platform: the release list
// and the release's page (the HTTP views). How App Platform, Portainer and
// DOCR receive a digest bundle is the deployment package's (digest_targets_test.go).

const doToken = "dop_v1_test"

// promotionTargets is the release world with App Platform and Portainer
// attached: DOCR answers only the DigitalOcean token, and Portainer's
// endpoint answers platform (its docker/version body).
type promotionTargets struct {
	*releaseWorld
	do       *dofake.API
	docr     *releaseRegistry
	platform atomic.Pointer[string]
	mu       sync.Mutex
	stacks   []string // the compose files Portainer was given
}

func newPromotionTargets(t *testing.T, configure func(*Config)) *promotionTargets {
	t.Helper()
	p := &promotionTargets{do: &dofake.API{}, docr: newRegistry(t, "apptown", doToken)}
	p.do.ServeFrontDoor(false)
	amd64 := `{"Os":"linux","Arch":"amd64"}`
	p.platform.Store(&amd64)
	api := httptest.NewServer(p.do.Handler())
	t.Cleanup(api.Close)
	portainer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/endpoints":
			fmt.Fprint(w, `[{"Id":3,"Name":"local"}]`)
		case "/api/endpoints/3/docker/version":
			fmt.Fprint(w, *p.platform.Load())
		case "/api/stacks":
			fmt.Fprint(w, `[]`)
		case "/api/stacks/create/standalone/string":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			compose, _ := body["stackFileContent"].(string)
			p.mu.Lock()
			p.stacks = append(p.stacks, compose)
			p.mu.Unlock()
			fmt.Fprint(w, `{"Id":1}`)
		default:
			http.Error(w, "not faked", http.StatusNotFound)
		}
	}))
	t.Cleanup(portainer.Close)
	p.releaseWorld = newReleaseWorld(t, func(c *Config) {
		c.DigitalOcean = digitalocean.Options{APIBase: api.URL, RegistryHost: p.docr.host, PollInterval: 10 * time.Millisecond, DBPoll: 10 * time.Millisecond,
			ProbeFrontDoor: func(context.Context, string, time.Time, func(string, ...any)) error { return nil }}
		if configure != nil {
			configure(c)
		}
	})
	a := p.a
	secrets := fmt.Sprintf("DIGITALOCEAN_ACCESS_TOKEN=%s\nPORTAINER_URL=%s\nPORTAINER_API_KEY=key\nAPPTOWN_APPS_DOMAIN=apps.example\n", doToken, portainer.URL)
	f, err := os.OpenFile(filepath.Join(a.cfg.dataDir, "secrets.env"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(secrets); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Integrations.Bindings.Save(context.Background(), "do-syd", digitalocean.CapCloudAccount, "digitalocean", mustJSON(map[string]any{"tokenFrom": "DIGITALOCEAN_ACCESS_TOKEN", "region": "syd", "registry": "forge-test"}), "fixture"); err != nil {
		t.Fatal(err)
	}
	a.SeedTargets(t.Context())
	if err := a.Projects.SetDefaultBranch(context.Background(), idstest.N(1), "main"); err != nil {
		t.Fatal(err)
	}
	return p
}

// land runs a landing of the multipart app on lab and answers its bundle.
func (p *promotionTargets) land(t *testing.T) *deployment.ReleaseBundle {
	t.Helper()
	p.mainOn(t, p.lab)
	run, stepID := p.landingRun(t)
	p.a.Work.ExecuteStep(p.a.asEngine(context.Background()), stepID)
	if step := p.step(t, stepID); step.Status != workflow.StatusPass {
		t.Fatalf("deploy step: %s %s", step.Status, step.Evidence)
	}
	b, err := p.a.Deployment.Bundles.OfRun(context.Background(), idstest.N(1), run.ID)
	if err != nil || b == nil || !b.ByDigest() {
		t.Fatalf("the landing's bundle: %+v %v", b, err)
	}
	return b
}

func (p *promotionTargets) target(t *testing.T, name string) *deployment.Target {
	t.Helper()
	target, err := p.a.Deployment.Targets.ByName(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

// A promotion to App Platform has no landing run, and its deployment records
// no commit or container. Once the deployment is observed running, the
// release list says the release serves, as its own page says it is live;
// before that first look it is "not observed yet", not "not serving".
//
// Kills: the list matching the deployment's landing run or commit instead of
// the owner's live rule (complaint #19); a provider deployment read as
// "records no container", unknown forever.
func TestAnAppPlatformPromotionServesInTheListAsItsPageSays(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, nil)
	p.appYAML = releaseOneImageYAML
	b := p.land(t)
	rel, err := p.promote(t, b.ID, "cloud", p.target(t, "do-syd"))
	if err != nil || rel.State != deployment.Released {
		t.Fatalf("the release: %+v %v", rel, err)
	}
	listed := func() (serving bool, observed, reason string) {
		t.Helper()
		w := authzCall(p.a.Handler(), "boss", "GET", "/api/v1/projects/p/releases", "")
		var list []struct {
			ID               ids.ID
			Serving          bool
			Observed, Reason string
		}
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 {
			t.Fatalf("releases: %d %s", w.Code, w.Body.String())
		}
		for _, r := range list {
			if r.ID == rel.ID {
				return r.Serving, r.Observed, r.Reason
			}
		}
		t.Fatalf("release %d is not listed: %s", rel.ID, w.Body.String())
		return
	}
	if serving, observed, reason := listed(); serving || observed != "unknown" || !strings.Contains(reason, "not been observed yet") {
		t.Fatalf("before the first observation: serving %v, %q %q", serving, observed, reason)
	}
	if err := p.a.Deployment.ObserveRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	w := authzCall(p.a.Handler(), "boss", "GET", fmt.Sprintf("/api/v1/projects/p/releases/%d", rel.Number), "")
	var detail struct{ IsLive bool }
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || w.Code != 200 || !detail.IsLive {
		t.Fatalf("the release's page: %d %s", w.Code, w.Body.String())
	}
	if serving, observed, reason := listed(); !serving {
		t.Fatalf("observed running, its page live, and the list says not serving: %q %q", observed, reason)
	}
}

// A release's page says whether its bundle is by digest and names each
// image, so a person can see before promoting it whether it can go to App
// Platform: a landing's bundle by digest names every part and init image
// as <repository>@sha256:…, its registry and platform; a bundle of image
// IDs says it is not by digest.
//
// Kills: a release detail without its bundle (finding 4: the form could
// not be told from the API); a bundle reported by digest when it holds
// image IDs.
func TestAReleaseDetailShowsItsBundlesFormAndDigests(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, nil)
	b := p.land(t)
	// A Docker host runs the multipart bundle (App Platform refuses its
	// distinct images at admission).
	rel, err := p.promote(t, b.ID, "staging", p.lab2)
	if err != nil {
		t.Fatal(err)
	}
	testAdministrator(t, p.a, "boss")
	var out struct {
		Bundle *struct {
			ID                 ids.ID
			ByDigest           bool
			Registry, Platform string
			Parts, Init        map[string]string
		}
	}
	w := authzCall(p.a.Handler(), "boss", "GET", fmt.Sprintf("/api/v1/projects/p/releases/%d", rel.Number), "")
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.Bundle == nil {
		t.Fatalf("release detail: %d %s %v", w.Code, w.Body.String(), err)
	}
	got := out.Bundle
	if got.ID != b.ID || !got.ByDigest || got.Registry != b.BuiltFrom.Registry || got.Platform != b.BuiltFrom.Platform || len(got.Parts) != len(b.Parts) || len(got.Init) != len(b.Init) {
		t.Fatalf("the bundle: %+v, want %+v", got, b)
	}
	for name, ref := range b.Parts {
		if got.Parts[name] != ref || !strings.Contains(ref, "@sha256:") {
			t.Errorf("part %s: %q, want %q", name, got.Parts[name], ref)
		}
	}
	for name, ref := range b.Init {
		if got.Init[name] != ref {
			t.Errorf("init %s: %q, want %q", name, got.Init[name], ref)
		}
	}
	// A bundle of image IDs, cut as an AppTown without a worker cuts one.
	contract, err := os.ReadFile(filepath.Join(p.a.projectRepoPath("p"), ".apptown", "app.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	idBundle, err := p.a.Deployment.Bundles.Cut(t.Context(), idstest.N(1), b.ContractRevision, map[string]string{"web": "sha256:web"}, map[string]string{}, contract, deployment.BundleOrigin{})
	if err != nil {
		t.Fatal(err)
	}
	rel, _ = p.promote(t, idBundle, "lab-copy", p.target(t, "lab"))
	w = authzCall(p.a.Handler(), "boss", "GET", fmt.Sprintf("/api/v1/projects/p/releases/%d", rel.Number), "")
	out.Bundle = nil
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.Bundle == nil || out.Bundle.ByDigest || out.Bundle.Registry != "" || out.Bundle.Parts["web"] != "sha256:web" {
		t.Fatalf("an image-ID release's detail: %d %s %v", w.Code, w.Body.String(), err)
	}
}

// The live endpoint names the image being transferred before the transfer
// reads its bytes, and retains progress after either a successful or failed
// deploy. Nothing from the registry credential may reach that public log.
func TestDeploymentProgressIsLiveBeforeTransferAndReadableAfterSettlement(t *testing.T) {
	t.Parallel()
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprintf("buildFailed=%v", failed), func(t *testing.T) {
			w := newReleaseWorld(t, nil)
			w.mainOn(t, w.lab)
			_, stepID := w.landingRun(t)
			h := w.a.Handler()
			read := func() (bool, string) {
				t.Helper()
				out := authzCall(h, "boss", "GET", fmt.Sprintf("/api/v1/steps/%d/live", stepID), "")
				var live struct {
					Running bool     `json:"running"`
					Lines   []string `json:"lines"`
				}
				if err := json.Unmarshal(out.Body.Bytes(), &live); err != nil || out.Code != 200 {
					t.Fatalf("live endpoint: %d %s (%v)", out.Code, out.Body.String(), err)
				}
				text := strings.Join(live.Lines, "\n")
				if strings.Contains(text, registryToken) {
					t.Error("deployment progress exposes the registry credential")
				}
				return live.Running, text
			}
			var checked sync.Once
			var transferred atomic.Bool
			w.worker.open = func(_ context.Context, _, image, _ string) error {
				checked.Do(func() {
					transferred.Store(true)
					running, text := read()
					if !running || !strings.Contains(text, "Publishing release image "+image) || strings.Contains(text, "Deployment completed") {
						t.Errorf("the live log does not locate the transfer before its bytes are read: running=%v\n%s", running, text)
					}
				})
				return nil
			}
			var pulled atomic.Bool
			beforeCommand := func(host string, args []string) {
				if host == hostOf("lab") && len(args) == 2 && args[0] == "pull" {
					pulled.Store(true)
					running, text := read()
					if !running || !strings.Contains(text, "Preparing recorded image") || strings.Contains(text, "Checking application readiness") {
						t.Errorf("the live log does not locate the host pull before it runs: running=%v\n%s", running, text)
					}
				}
			}
			w.hosts.onCommand.Store(&beforeCommand)
			if failed {
				w.worker.exit = 2
			}
			w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), stepID)
			want := workflow.StatusPass
			if failed {
				want = workflow.StatusFail
			}
			if step := w.step(t, stepID); step.Status != want {
				t.Fatalf("deploy: %s %s", step.Status, step.Evidence)
			}
			running, text := read()
			if running || !strings.Contains(text, "Building ") {
				t.Errorf("no historical build phase after settlement: running=%v\n%s", running, text)
			}
			if failed {
				if strings.Contains(text, "Publishing release image") || strings.Contains(text, "Deployment completed") {
					t.Errorf("failed build reports phases it did not reach:\n%s", text)
				}
			} else if !transferred.Load() || !pulled.Load() || !strings.Contains(text, "Checking application readiness") || !strings.Contains(text, "Deployment completed") {
				t.Errorf("successful deploy lost its boot/check phases:\n%s", text)
			}
		})
	}
}

// The landing's preflight (deployment.Preflight) through the deploy step as
// Work runs it, on an AppTown that builds its releases on the worker.

// preflightSpec requires WP_HOME of the worker part, and generates
// AUTH_KEY.
const preflightSpec = `[project]
name = "p"
revision = "1.0"

[profiles.default]
WP_HOME  = { description = "the site's address" }
AUTH_KEY = { description = "signs cookies", generate = true }

[scopes.worker]
secrets = ["WP_HOME", "AUTH_KEY"]
`

// bindWPHome binds deployment's WP_HOME to value in the built-in store.
func bindWPHome(t *testing.T, a *App, deployment, value string) {
	t.Helper()
	scope := secret.Scope("p", deployment, "")
	if _, err := a.Secrets.Vault.Put(t.Context(), scope, "WP_HOME", value, "test"); err != nil {
		t.Fatal(err)
	}
	if err := a.Secrets.Bindings.Put(t.Context(), idstest.N(1), secret.Binding{Deployment: deployment, Name: "WP_HOME", Store: secret.StoreInternal, Ref: scope, Version: "1", UpdatedBy: "test"}); err != nil {
		t.Fatal(err)
	}
}

// A main deployment missing WP_HOME, with only preview bound, is refused
// before its release is built: no release-build task is reserved or sent,
// no image published, no bundle committed, nothing reaches the host, and
// no secret is generated. The refusal names main and WP_HOME, not the
// value preview holds, and says where to set it. The committed contract
// is judged, not a newer one in the working tree.
func TestAMissingMainSettingRefusesTheLandingBeforeAnythingIsBuilt(t *testing.T) {
	t.Parallel()
	w := newReleaseWorld(t, nil)
	w.files = map[string]string{secret.SpecFile: preflightSpec}
	w.mainOn(t, w.lab)
	bindWPHome(t, w.a, "preview", "https://preview.example")
	run, stepID := w.landingRun(t)
	// An uncommitted edit that would drop the requirement is not what lands.
	for _, dir := range []string{requireLocation(t)(workflowSourceForTest(t, w.a, run).Workspace(w.a.cfg.dataDir)), filepath.Join(w.a.cfg.dataDir, "workspaces", "project-p")} {
		if err := os.WriteFile(filepath.Join(dir, secret.SpecFile), []byte("[project]\nname = \"p\"\nrevision = \"1.0\"\n\n[profiles.default]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), stepID)
	step := w.step(t, stepID)
	evidence := string(step.Evidence)
	if step.Status != workflow.StatusError {
		t.Fatalf("deploy step: %s %s, want refused", step.Status, evidence)
	}
	for _, want := range []string{`"category":"missing-settings"`, "the main deployment has no value for WP_HOME", "Settings → Secrets", `"missing":["WP_HOME"]`, `"consumers":["worker"]`} {
		if !strings.Contains(evidence, want) {
			t.Errorf("the refusal lacks %q: %s", want, evidence)
		}
	}
	if strings.Contains(evidence, "preview.example") {
		t.Errorf("the refusal carries a value: %s", evidence)
	}
	if sent := w.worker.requests(); len(sent) != 0 {
		t.Errorf("a refused release asked the worker for %d tasks", len(sent))
	}
	if reserved, err := tasks.New(w.a.DB).Invocations(context.Background(), stepID, 1); err != nil || len(reserved) != 0 {
		t.Errorf("a refused release reserved tasks: %+v %v", reserved, err)
	}
	if digest := w.registry.digestOf(t, fmt.Sprintf("%s/team/p:change-%d-part-web", w.registry.host, run.ChangeID)); digest != "" {
		t.Errorf("a refused release published %s", digest)
	}
	if b, err := w.a.Deployment.Bundles.OfRun(context.Background(), idstest.N(1), run.ID); err != nil || b != nil {
		t.Errorf("a refused release committed %+v %v", b, err)
	}
	if pulled, runs := w.hosts.pulled("lab"), containersRun(w.hosts.daemon("lab")); len(pulled)+len(runs) != 0 {
		t.Errorf("a refused release reached the host: %v %v", pulled, runs)
	}
	if bound, err := w.a.Secrets.Bindings.For(t.Context(), idstest.N(1), "main"); err != nil || len(bound) != 0 {
		t.Errorf("a refused release bound main's secrets: %v %v", bound, err)
	}
}

// With main's WP_HOME bound the same landing builds, publishes and deploys
// as before, and boot delivers the generated secret it minted.
func TestABoundMainSettingLetsTheLandingBuildAndDeploy(t *testing.T) {
	t.Parallel()
	w := newReleaseWorld(t, nil)
	w.files = map[string]string{secret.SpecFile: preflightSpec}
	w.mainOn(t, w.lab)
	bindWPHome(t, w.a, "preview", "https://preview.example")
	bindWPHome(t, w.a, "main", "https://main.example")
	_, stepID := w.landingRun(t)
	w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), stepID)
	if step := w.step(t, stepID); step.Status != workflow.StatusPass {
		t.Fatalf("deploy step: %s %s", step.Status, step.Evidence)
	}
	if len(w.worker.requests()) != 1 {
		t.Errorf("the release was not built once: %d tasks", len(w.worker.requests()))
	}
	if bound, err := w.a.Secrets.Bindings.For(t.Context(), idstest.N(1), "main"); err != nil || !bound["AUTH_KEY"].Generated {
		t.Errorf("boot did not generate AUTH_KEY: %v %v", bound, err)
	}
}

// The preflight is not a reservation: a setting removed while the release
// is built still stops the deploy at boot, before anything runs.
func TestASettingRemovedDuringTheBuildStillRefusesTheDeploy(t *testing.T) {
	t.Parallel()
	w := newReleaseWorld(t, nil)
	w.files = map[string]string{secret.SpecFile: preflightSpec}
	w.mainOn(t, w.lab)
	bindWPHome(t, w.a, "main", "https://main.example")
	removed := false
	w.worker.open = func(ctx context.Context, _, _, _ string) error {
		if !removed {
			removed = true
			if err := w.a.Secrets.Bindings.Unbind(ctx, idstest.N(1), "main", "WP_HOME", "test"); err != nil {
				return err
			}
		}
		return nil
	}
	_, stepID := w.landingRun(t)
	w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), stepID)
	step := w.step(t, stepID)
	if !removed || step.Status == workflow.StatusPass || !strings.Contains(string(step.Evidence), "WP_HOME") {
		t.Fatalf("deploy step after WP_HOME was removed during the build: %s %s", step.Status, step.Evidence)
	}
	if runs := containersRun(w.hosts.daemon("lab")); len(runs) != 0 {
		t.Errorf("the deploy ran containers without WP_HOME: %v", runs)
	}
}

// A manual deploy of a landed bundle to a deployment that lacks the
// setting is refused by the same check, before any secret is generated
// for it or anything reaches its host.
func TestAManualDeployMissingASettingIsRefusedBeforeAnyEffect(t *testing.T) {
	t.Parallel()
	w := newReleaseWorld(t, nil)
	w.files = map[string]string{secret.SpecFile: preflightSpec}
	w.mainOn(t, w.lab)
	bindWPHome(t, w.a, "main", "https://main.example")
	run, stepID := w.landingRun(t)
	w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), stepID)
	if step := w.step(t, stepID); step.Status != workflow.StatusPass {
		t.Fatalf("deploy step: %s %s", step.Status, step.Evidence)
	}
	b, err := w.a.Deployment.Bundles.OfRun(context.Background(), idstest.N(1), run.ID)
	if err != nil || b == nil {
		t.Fatalf("the landing's bundle: %+v %v", b, err)
	}
	_, err = w.promote(t, b.ID, "staging", w.lab2)
	var refused *deployment.PreflightError
	if !errors.As(err, &refused) || refused.Category != deployment.PreflightMissingSettings || strings.Join(refused.Missing, ",") != "WP_HOME" || refused.Check.Deployment != "staging" {
		t.Fatalf("a manual deploy without WP_HOME: %v", err)
	}
	if bound, err := w.a.Secrets.Bindings.For(t.Context(), idstest.N(1), "staging"); err != nil || len(bound) != 0 {
		t.Errorf("a refused deploy bound staging's secrets: %v %v", bound, err)
	}
	if pulled, runs := w.hosts.pulled("lab2"), containersRun(w.hosts.daemon("lab2")); len(pulled)+len(runs) != 0 {
		t.Errorf("a refused deploy reached its host: %v %v", pulled, runs)
	}
}

// Releases built on AppTown's Docker hosts, with no worker: a worker
// changes where a release is built, never what it produces. With a
// container registry the landing publishes what its host built, as the
// worker's build is published; without one it records the host's image
// IDs, as before.

// localReleaseWorld is the release world with no worker, landing an app
// whose every image is built (image-only declarations are copied by the
// same publication as on the worker, covered there).
func localReleaseWorld(t *testing.T) *releaseWorld {
	t.Helper()
	w := newReleaseWorld(t, func(c *Config) { c.Worker, c.PrivateDocker = nil, false })
	w.appYAML = `app:
  web:
    public: true
    static: site
    ports: ["8080"]
  worker:
    build: worker
init:
  - name: migrate
    build: migrations
`
	return w
}

// A landing built on its host is published to the project's registry: the
// bundle names, by the digest the registry answers, exactly the image the
// host saved for each built part and init step, for the host's platform,
// built on that host; the step's evidence names the bundle, and another host takes the release
// by pulling those digests and building nothing.
func TestALandingBuiltOnItsHostIsPublishedAsOnTheWorker(t *testing.T) {
	t.Parallel()
	w := localReleaseWorld(t)
	w.mainOn(t, w.lab)
	run, stepID := w.landingRun(t)
	w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), stepID)
	step := w.step(t, stepID)
	if step.Status != workflow.StatusPass {
		t.Fatalf("deploy step: %s %s", step.Status, step.Evidence)
	}
	if sent := w.worker.requests(); len(sent) != 0 {
		t.Fatalf("an AppTown with no worker sent %d tasks", len(sent))
	}
	if len(w.hosts.built("lab")) == 0 {
		t.Fatal("the landing did not build on its host")
	}
	b, err := w.a.Deployment.Bundles.OfRun(context.Background(), idstest.N(1), run.ID)
	if err != nil || b == nil || !b.ByDigest() {
		t.Fatalf("the landing's bundle: %+v %v", b, err)
	}
	if b.BuiltFrom.Registry != w.registry.host+"/team/p" || b.BuiltFrom.Platform != releasePlatform || b.BuiltFrom.Build.Worker != "lab" || b.BuiltFrom.Build.Role != "docker-host" || b.BuiltFrom.StepID != stepID || !b.BuiltFrom.TargetID.IsZero() {
		t.Errorf("the bundle's origin: %+v", b.BuiltFrom)
	}
	// Compare each bundle entry with the image its own container ran.
	// Comparing sets alone would accept swapped parts and init steps.
	dep, err := w.a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main")
	if err != nil {
		t.Fatal(err)
	}
	names, err := dep.HostNames()
	if err != nil {
		t.Fatal(err)
	}
	exported := w.hosts.savedDigests()
	for container, recorded := range map[string]string{
		names.App(): b.Parts["web"], names.Service("worker"): b.Parts["worker"], names.Init("migrate"): b.Init["migrate"],
	} {
		id := w.hosts.daemon("lab").ranImage(container, "")
		if digest := exported[id]; digest == "" || recorded != w.registry.host+"/team/p@"+digest {
			t.Errorf("%s ran image %s, exported as %s, but the bundle names %s", container, id, digest, recorded)
		}
	}
	saved := map[string]bool{}
	for _, digest := range w.hosts.savedDigests() {
		saved[w.registry.host+"/team/p@"+digest] = true
	}
	built := map[string]bool{b.Parts["web"]: true, b.Parts["worker"]: true, b.Init["migrate"]: true}
	if len(saved) != 3 || len(built) != 3 {
		t.Fatalf("the host saved %v; the bundle's built images are %v", saved, built)
	}
	for ref := range built {
		if !saved[ref] {
			t.Errorf("the bundle names %s, which is not an image the host saved: %v", ref, saved)
		}
	}
	if answer := w.registry.digestOf(t, fmt.Sprintf("%s/team/p:change-%d-part-web", w.registry.host, run.ChangeID)); b.Parts["web"] != w.registry.host+"/team/p@"+answer {
		t.Errorf("the bundle's web is %s; the registry answers %q", b.Parts["web"], answer)
	}
	var evidence struct{ Bundle ids.ID }
	if err := json.Unmarshal(step.Evidence, &evidence); err != nil || evidence.Bundle != b.ID {
		t.Errorf("the step's evidence names bundle %d, want %d: %s", evidence.Bundle, b.ID, step.Evidence)
	}
	if rel, err := w.promote(t, b.ID, "staging", w.lab2); err != nil || rel.State != deployment.Released {
		t.Fatalf("promoting the bundle to lab2: %+v %v", rel, err)
	}
	expectPulledOnly(t, w, "lab2", b)
}

// With no container registry a landing built on its host records the
// host's image IDs, as before, and saves nothing.
func TestALandingWithNoRegistryKeepsItsHostsImages(t *testing.T) {
	t.Parallel()
	w := localReleaseWorld(t)
	if err := w.a.Integrations.Attachments.Detach(t.Context(), idstest.N(1), "container-registry", "test"); err != nil {
		t.Fatal(err)
	}
	w.mainOn(t, w.lab)
	run, stepID := w.landingRun(t)
	w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), stepID)
	if step := w.step(t, stepID); step.Status != workflow.StatusPass {
		t.Fatalf("deploy step: %s %s", step.Status, step.Evidence)
	}
	b, err := w.a.Deployment.Bundles.OfRun(context.Background(), idstest.N(1), run.ID)
	if err != nil || b == nil || b.ByDigest() || b.BuiltFrom.TargetID != w.lab.ID {
		t.Fatalf("the landing's bundle: %+v %v", b, err)
	}
	if saved := w.hosts.savedDigests(); len(saved) != 0 {
		t.Errorf("the host saved %v with no registry to publish to", saved)
	}
}

// A registry that refuses the publication ends the step with an error that
// says the new version is running and was not published; no bundle is
// recorded, of either form.
func TestAFailedLocalPublicationSaysTheVersionIsRunning(t *testing.T) {
	t.Parallel()
	w := localReleaseWorld(t)
	w.mainOn(t, w.lab)
	run, stepID := w.landingRun(t)
	w.registry.refusing.Store(true)
	w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), stepID)
	step := w.step(t, stepID)
	var evidence struct{ Reason, URL string }
	_ = json.Unmarshal(step.Evidence, &evidence)
	if step.Status != workflow.StatusError || evidence.URL == "" || !strings.Contains(evidence.Reason, "is running at "+evidence.URL) || !strings.Contains(evidence.Reason, "was not published") {
		t.Fatalf("deploy step: %s %s", step.Status, step.Evidence)
	}
	if b, err := w.a.Deployment.Bundles.OfRun(context.Background(), idstest.N(1), run.ID); err != nil || b != nil {
		t.Errorf("a failed publication recorded bundle %+v %v", b, err)
	}
}

// The release plan says a landing's bundle is published once the landing
// publishes it, and the commit it would ship is the bundle's.
func TestTheReleasePlanShipsTheLandingsPublishedBundle(t *testing.T) {
	t.Parallel()
	w := newReleaseWorld(t, nil)
	w.mainOn(t, w.lab)
	run, stepID := w.landingRun(t)
	w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), stepID)
	b, err := w.a.Deployment.Bundles.OfRun(context.Background(), idstest.N(1), run.ID)
	if err != nil || b == nil || !b.ByDigest() {
		t.Fatalf("the landing's bundle: %+v %v", b, err)
	}
	out := authzCall(w.a.Handler(), "boss", "GET", "/api/v1/projects/p/release/plan", "")
	var plan struct {
		Published *bool
		Head      struct{ SHA string }
	}
	if err := json.Unmarshal(out.Body.Bytes(), &plan); err != nil || out.Code != 200 || plan.Published == nil || !*plan.Published || plan.Head.SHA != b.ContractRevision {
		t.Fatalf("the plan: %d %s %v", out.Code, out.Body.String(), err)
	}
}
