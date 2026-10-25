package preview

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/stock"
)

func TestALandedChangePreviewsItsLandedCommitAndAnOpenOneItsFolder(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "game", Name: "Game"})
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	projects := project.New(db, nil)
	secrets := secret.NewService(db, secret.Config{DataDir: dir})
	integrations := integration.NewService(db, integration.Dependencies{Projects: projects, Secrets: secrets})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Forwarded-Proto") != "https" {
			t.Error("probe lost ingress policy")
		}
		_, _ = w.Write([]byte("<html><script>game()</script></html>"))
	}))
	defer server.Close()
	parsed, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(parsed.Port())
	var mu sync.Mutex
	var calls []string
	started := false
	scope := docker.Scope{Client: dockerClientFunc(func(_ context.Context, _ string, c docker.Command) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		args := c.Args
		if len(args) >= 2 && args[0] == "--config" {
			args = args[2:] // the boot's own client configuration
		}
		line := strings.Join(args, " ")
		calls = append(calls, line)
		if out, ok := dockerfake.RoomyDisk(args); ok {
			return out, nil
		}
		switch args[0] {
		case "run":
			started = true
			return []byte("container"), nil
		case "rm":
			started = false
		case "info":
			return []byte("8589934592\t8\t/var/lib/docker"), nil
		case "port":
			if started {
				return []byte(fmt.Sprintf("127.0.0.1:%d", port)), nil
			}
			return nil, errors.New("no container")
		case "inspect":
			if strings.Contains(line, "State.Running") {
				return []byte(strconv.FormatBool(started)), nil
			}
		}
		return nil, nil
	})}
	targets := deployment.NewTargets(db, nil)
	s := New(db, Dependencies{Targets: targets, Deployments: deployment.NewDeployments(db), Projects: projects, Integrations: integrations, DataDir: dir,
		Hosts: deployment.NewHosts(targets, secrets, deployment.HostSettings{Scope: scope, DataDir: dir, BuiltInTarget: "local"}),
		Environment: deployment.NewEnvironment(targets, deployment.EnvironmentDependencies{ToolVersions: stock.Versions(), Secrets: secrets, Integrations: integrations, Needs: dependency.NewCatalog(db, nil),
			Probe: &docker.Probe{Settle: time.Millisecond}, Routes: deployment.Routing{Scheme: "https", PreviewScheme: "https", PreviewDomain: "preview.example"}, Projects: projects})})
	if _, err := s.targets.Put(context.Background(), &deployment.Target{Name: "local", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady}); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "workspaces", "project-game")
	write := func(dir, image string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, ".apptown"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".apptown", "app.yaml"), []byte("app:\n  web:\n    image: "+image+"\n    public: true\n    ports: [\"8080\"]\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := gittest.Command(args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
		return strings.TrimSpace(string(out))
	}
	write(repo, "old:v1")
	git("init", "-q")
	git("add", ".")
	git("-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "old game")
	revision := git("rev-parse", "HEAD")
	write(repo, "new:v2")
	git("add", ".")
	git("-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "later game")
	selected := Change{ID: idstest.N(7), ProjectID: pid, SourceRevision: revision}
	out, err := s.RehearseChange(context.Background(), selected, false)
	if err != nil || out.Revision != revision || out.URL != "https://change-00000000-0007-7000-8000-000000000000.preview.example" {
		t.Fatalf("historical preview: %+v %v", out, err)
	}
	if row, err := s.deployments.ByName(context.Background(), pid, deployment.PreviewName(idstest.N(7))); err != nil || row.URL != out.URL {
		t.Fatalf("preview receipt: %+v %v", row, err)
	}
	// The landed change's folder still exists (the workspace sweep takes it
	// a day later) and holds something else: the landed commit is still
	// what is previewed.
	worktree := filepath.Join(dir, "workspaces", "change-00000000-0007-7000-8000-000000000000")
	write(worktree, "dirty:v3")
	out, err = s.RehearseChange(context.Background(), selected, true)
	if err != nil || out.Revision != revision {
		t.Fatalf("landed preview beside its folder: %+v %v", out, err)
	}
	mu.Lock()
	transcript := strings.Join(calls, "\n")
	mu.Unlock()
	if !strings.Contains(transcript, "pull old:v1") || strings.Contains(transcript, "pull dirty:v3") || strings.Contains(transcript, "pull new:v2") {
		t.Fatalf("a landed change booted something but its landed commit:\n%s", transcript)
	}
	// A change that has not landed previews its folder, edits and all.
	out, err = s.RehearseChange(context.Background(), Change{ID: idstest.N(7), ProjectID: pid}, true)
	if err != nil || out.Revision != "" {
		t.Fatalf("dirty preview: %+v %v", out, err)
	}
	mu.Lock()
	transcript = strings.Join(calls, "\n")
	mu.Unlock()
	if !strings.Contains(transcript, "pull dirty:v3") {
		t.Fatalf("an unlanded change did not boot its folder:\n%s", transcript)
	}
	if strings.Contains(transcript, "apptown-env-") {
		t.Fatal("preview touched standing data")
	}
}

type dockerClientFunc func(context.Context, string, docker.Command) ([]byte, error)

func (f dockerClientFunc) Run(ctx context.Context, host string, c docker.Command) ([]byte, error) {
	return f(ctx, host, c)
}
