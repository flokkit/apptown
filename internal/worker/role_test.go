package worker_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/execution/executionfake"
	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

// A worker's role is fixed on its first start with an empty state
// directory, before anything else, and a later start with another role is
// refused.
func TestAWorkersRoleIsFixedOnAnEmptyStateDirectoryAndNeverChanged(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	role, err := worker.FixRole(state, worker.RoleCheck, nil)
	if err != nil || role.Role != worker.RoleCheck || role.Since.IsZero() {
		t.Fatalf("a new check worker: %+v %v", role, err)
	}
	if entries, _ := os.ReadDir(state); len(entries) != 1 || entries[0].Name() != "role" {
		t.Fatalf("the role is not the first and only thing in the state directory: %v", entries)
	}
	again, err := worker.FixRole(state, worker.RoleCheck, nil)
	if err != nil || !again.Since.Equal(role.Since) {
		t.Fatalf("a restart: %+v %v", again, err)
	}
	if _, err := worker.FixRole(state, worker.RoleAgent, nil); err == nil || !strings.Contains(err.Error(), "never changed") {
		t.Fatalf("a check worker started as an agent worker: %v", err)
	}
	if _, err := worker.FixRole(t.TempDir(), "chat", nil); err == nil {
		t.Fatal("an unknown role was fixed")
	}
}

// A state directory from before roles belongs to a worker that may have run
// privileged chats: it is an agent worker, and can never be a check worker.
func TestAFormerChatWorkerCanNeverBecomeACheckWorker(t *testing.T) {
	state := t.TempDir()
	if err := os.MkdirAll(filepath.Join(state, "sessions", "c1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.FixRole(state, worker.RoleCheck, nil); err == nil || !strings.Contains(err.Error(), "can never become a check worker") {
		t.Fatalf("a former chat worker as a check worker: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "role")); !os.IsNotExist(err) {
		t.Fatalf("the refused start wrote a role: %v", err)
	}
	if role, err := worker.FixRole(state, worker.RoleAgent, nil); err != nil || role.Role != worker.RoleAgent {
		t.Fatalf("a former chat worker as an agent worker: %+v %v", role, err)
	}
	if _, err := worker.FixRole(state, worker.RoleCheck, nil); err == nil {
		t.Fatal("a former chat worker became a check worker on its next start")
	}
}

// A check worker serves its role, checks and their images, and refuses
// chats, tasks, previews and everything else with its reason.
func TestACheckWorkerRefusesEverythingButChecks(t *testing.T) {
	ca := workertest.NewCA(t)
	since := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	srv := workertest.StartServer(t, ca, &worker.Server{Installation: workertest.Installation, State: t.TempDir(),
		Role: worker.Role{Role: worker.RoleCheck, Since: since}, Tasks: &executionfake.Tasks{}})
	client := workertest.Client(t, ca, workertest.Installation)
	call := func(method, path string) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader("{}"))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	code, body := call(http.MethodGet, "/v1/role")
	var role worker.Role
	if err := json.Unmarshal([]byte(body), &role); code != http.StatusOK || err != nil || role.Role != worker.RoleCheck || !role.Since.Equal(since) {
		t.Fatalf("GET /v1/role: %d %s", code, body)
	}
	for _, r := range [][2]string{
		{http.MethodPut, "/v1/sessions/c1"}, {http.MethodPost, "/v1/sessions/c1/start"}, {http.MethodPost, "/v1/sessions/c1/rebuild"},
		{http.MethodPost, "/v1/tasks/agent-1"}, {http.MethodPut, "/v1/workspaces/agent-1?ref=refs/heads/x&commit=" + strings.Repeat("a", 40)},
		{http.MethodPut, "/v1/previews/p1"}, {http.MethodGet, "/v1/agent-image"},
	} {
		if code, body := call(r[0], r[1]); code != http.StatusForbidden || !strings.Contains(body, "runs checks and nothing else") {
			t.Errorf("%s %s on a check worker: %d %s", r[0], r[1], code, body)
		}
	}
	if code, body := call(http.MethodPost, "/v1/checks"); code == http.StatusForbidden {
		t.Errorf("a check worker refused a check: %s", body)
	}
	if err := worker.RequireCheckWorker(context.Background(), client, srv.URL, time.Now()); err != nil {
		t.Errorf("a check worker provisioned before AppTown first used it: %v", err)
	}
	if err := worker.RequireCheckWorker(context.Background(), client, srv.URL, since.Add(-time.Minute)); err == nil || !strings.Contains(err.Error(), "after this AppTown first used that address") {
		t.Errorf("a check worker provisioned after AppTown first used its address: %v", err)
	}
}

// A check worker never builds a release: a task declaring images is
// refused with its reason, nothing is recorded or created, and it serves no
// image file.
func TestACheckWorkerRefusesAReleaseBuild(t *testing.T) {
	ca := workertest.NewCA(t)
	state := t.TempDir()
	tasks := &executionfake.Tasks{}
	srv := workertest.StartServer(t, ca, &worker.Server{Installation: workertest.Installation, State: state,
		Role: worker.Role{Role: worker.RoleCheck, Since: time.Now()}, Tasks: tasks, DindImage: "docker:27-dind"})
	client := workertest.Client(t, ca, workertest.Installation)
	build, _ := json.Marshal(worker.TaskRequest{Key: "build-1", Image: "docker:27-cli", Argv: []string{"docker", "buildx", "build", "."},
		Admission: execution.Admission{Profile: "worker-vm", Network: execution.NetworkDefault, Limits: execution.Limits{CPUs: 1, MemoryBytes: 1 << 30, PIDs: 512}, Expires: time.Now().Add(time.Hour)},
		Workspace: "build-1", Private: &execution.PrivateDocker{Daemon: true},
		Images: []execution.ImageResult{{Name: "web", Path: "/apptown/state/out/web", Max: 1 << 30}}})
	for _, r := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/tasks/build-1", string(build)},
		{http.MethodGet, "/v1/tasks/build-1/images", ""},
		{http.MethodGet, "/v1/tasks/build-1/images/web/index.json", ""},
	} {
		req, _ := http.NewRequest(r.method, srv.URL+r.path, strings.NewReader(r.body))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "runs checks and nothing else") {
			t.Errorf("%s %s on a check worker: %d %s", r.method, r.path, resp.StatusCode, body)
		}
	}
	if tasks.Present("build-1") {
		t.Error("a check worker created a release build")
	}
	if _, err := os.Stat(filepath.Join(state, "records", "build-1.json")); !os.IsNotExist(err) {
		t.Errorf("a check worker recorded a release build: %v", err)
	}
}

// AppTown's first use of an address is recorded once and kept.
func TestTheFirstUseOfAWorkerIsKept(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "workers-used")
	first, err := worker.FirstUse(dir, "https://w1:8443", nil)
	if err != nil || first.IsZero() {
		t.Fatalf("%v %v", first, err)
	}
	later, err := worker.FirstUse(dir, "https://w1:8443", nil)
	if err != nil || !later.Equal(first) {
		t.Fatalf("the first use moved: %v, was %v (%v)", later, first, err)
	}
	if other, err := worker.FirstUse(dir, "https://w2:8443", nil); err != nil || other.Before(first) {
		t.Fatalf("another address: %v %v", other, err)
	}
}

// An agent worker writes its privileged-agents mark durably and keeps the
// first; a check worker's guard refuses, and a mark that cannot be written
// is an error.
func TestThePrivilegedAgentsMark(t *testing.T) {
	state := t.TempDir()
	mark := worker.PrivilegedMark(state, worker.Role{Role: worker.RoleAgent}, nil)
	if err := mark(); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(state, "privileged-agents"))
	if err != nil {
		t.Fatal(err)
	}
	if err := mark(); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(filepath.Join(state, "privileged-agents")); string(again) != string(first) {
		t.Fatalf("the mark was replaced: %s, was %s", again, first)
	}
	if err := worker.PrivilegedMark(t.TempDir(), worker.Role{Role: worker.RoleCheck}, nil)(); err == nil {
		t.Fatal("a check worker's guard allowed a privileged container")
	}
	blocked := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := worker.PrivilegedMark(blocked, worker.Role{Role: worker.RoleAgent}, nil)(); err == nil {
		t.Fatal("an unwritable mark reported success")
	}
}
