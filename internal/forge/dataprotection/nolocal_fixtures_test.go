package dataprotection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/docker/dockerfake"
	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
)

const attachedHost = "ssh://" + "forge-target-remote"

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

type noLocalSources struct {
	repo, yaml string
}

func (s noLocalSources) Project(_ context.Context, id ids.ID) (deployment.ReleaseProject, error) {
	return deployment.ReleaseProject{Application: deployment.Application{ID: id, Slug: "game"}, Repository: s.repo}, nil
}

func (s noLocalSources) Contract(_ context.Context, _ deployment.ReleaseProject, revision string) (*appcontract.Contract, string, error) {
	c, err := appcontract.Parse([]byte(s.yaml))
	return c, revision, err
}

type noLocalWorld struct {
	db       *database.DB
	dir      string
	daemon   *attachedDaemon
	project  ids.ID
	remote   *deployment.Target
	repo     string
	revision string
	sources  noLocalSources
	// service builds a Service on the same records, with or without the mode.
	service func(noLocal bool) *both
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
	w.repo = filepath.Join(w.dir, "workspaces", "project-game")
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
	w.service = func(noLocal bool) *both {
		return assembled(db, testRuntime{Scope: docker.Scope{Client: w.daemon}, Probe: &docker.Probe{Settle: time.Millisecond}, DataDir: w.dir, SSHConfigPath: filepath.Join(w.dir, "ssh-config"), Projects: projects, Secrets: secrets, Integrations: integrations, Needs: dependency.NewCatalog(db, nil), ChownImage: "test-helper", BuiltInTarget: "local", NoLocalDocker: noLocal}, deployment.Dependencies{Followups: noFollowups{}, Configuration: deployment.ConfigurationDependencies{Projects: w.sources}, Releases: deployment.ReleaseDependencies{Authority: &requestAuthority{}, Sources: w.sources, Secrets: requestSecrets{}, Queue: requestQueue{}}})
	}
	w.remote, err = w.service(true).Targets.Put(context.Background(), &deployment.Target{Name: "remote", Kind: deployment.KindDockerHost, Origin: deployment.OriginAttached, Status: deployment.TargetReady,
		Address: map[string]any{"host": "ssh://deploy@127.0.0.1", "keyFrom": "REMOTE_KEY"}, NeedsMode: "emulators", SecretsDelivery: "inject"})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *noLocalWorld) deployment(t *testing.T, s *both, name string, target *deployment.Target) *deployment.Deployment {
	t.Helper()
	dep, err := s.Deployments.EnsureStanding(t.Context(), w.project, name, target.ID, deployment.Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	return dep
}

type requestAuthority struct {
	deny error
	seen []access.Permission
}

func (a *requestAuthority) Require(_ context.Context, p access.Permission) (access.Caller, error) {
	a.seen = append(a.seen, p)
	return access.Caller{Principal: access.Principal{Name: "alice"}, Verified: true}, a.deny
}

type requestSecrets struct{}

func (requestSecrets) ResolveRelease(context.Context, deployment.Application, string, string, *appcontract.Contract) (deploy.ConsumerSecrets, []string, error) {
	return nil, nil, nil
}

type requestQueue struct{}

func (requestQueue) EnqueueReleaseIn(ctx context.Context, tx *database.Tx, id ids.ID) error {
	var recorded int64
	// sql-guarantee: requestQueue is a test-owned transactional port double whose insertion must share the release transaction for queue-veto rollback; production queries do not target test_release_queue.
	return tx.SQL().QueryRowContext(ctx, `INSERT INTO test_release_queue(release_id) VALUES ($1) RETURNING release_id`, id).Scan(&recorded)
}

// The subsystem is built from its declared dependencies alone: every
// field of Dependencies is satisfied here by a value this test defines,
// and nothing from AppTown is imported (the shape checker asserts the
// import graph; this asserts the constructor).

func (noFollowups) EnqueueIn(context.Context, *database.Tx, jobs.Request) error {
	return nil
}
