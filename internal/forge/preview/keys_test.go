package preview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/docker/dockerfake"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/openrouter/openrouterfake"
)

const llmProvisioningSentinel = "sk-or-v1-PROVISIONING-SENTINEL-d41c"

const llmAppFixture = `app:
  web:
    image: reg.example/app:1
    ports: ["8080"]
    public: true
needs:
  llm: {limit: 3, limitReset: monthly}
`

// llmWorld is a world whose AppTown holds an OpenRouter provisioning key, with
// an AI models connector on a fake OpenRouter; attached says whether the
// project attached AI models.
func llmWorld(t *testing.T, attached bool) (*world, *openrouterfake.Server) {
	t.Helper()
	w := newWorld(t, "", "OPENROUTER_PROVISIONING_KEY="+llmProvisioningSentinel+"\n")
	f := openrouterfake.New(llmProvisioningSentinel)
	t.Cleanup(f.Close)
	integrations := w.env.Integrations
	if _, err := integrations.Bindings.Save(context.Background(), "openrouter", integration.CapLLM, "openrouter", `{"url":"`+f.Base+`","provisioningKeyFrom":["OPENROUTER_PROVISIONING_KEY"],"maxLimit":5,"previewLimit":1}`, "admin"); err != nil {
		t.Fatal(err)
	}
	if attached {
		if err := integrations.Attachments.Attach(t.Context(), w.projectID, integration.CapLLM, "openrouter", "{}", integration.Entered, "owner", nil); err != nil {
			t.Fatal(err)
		}
	}
	return w, f
}

// llmBootWorld is the preview owner on llmWorld's Docker.
func llmBootWorld(t *testing.T, attached bool) (*Service, *world, ids.ID, *openrouterfake.Server) {
	t.Helper()
	w, f := llmWorld(t, attached)
	return w.service(Dependencies{}), w, w.projectID, f
}

func llmWorkspace(t *testing.T, ws, yaml string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(ws, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".apptown", "app.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
}

func llmContract(t *testing.T, yaml string) (*appcontract.Contract, string) {
	t.Helper()
	ws := t.TempDir()
	llmWorkspace(t, ws, yaml)
	c, err := docker.ReadWorkspaceContract(ws)
	if err != nil {
		t.Fatal(err)
	}
	return c, ws
}

func runCalls(client *dockerfake.Daemon) string {
	var out []string
	for _, args := range client.Matching("run ") {
		out = append(out, strings.Join(args, " "))
	}
	return strings.Join(out, "\n")
}

// A preview's key: preparing makes none; a boot makes one with the preview
// limit; booting again keeps it. While the preview boots its key stays;
// once no container of it stands, the key is revoked with its value; a
// revoke that fails becomes a retained row.
func TestAPreviewsKeyLivesWhileItBootsOrStands(t *testing.T) {
	t.Parallel()
	s, lw, projectID, f := llmBootWorld(t, true)
	ctx := context.Background()
	c, ws := llmContract(t, llmAppFixture)
	name := docker.PreviewChangeName(idstest.N(7))
	prepared, err := s.preparePreview(ctx, projectID, "p", name, ws, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Calls()) != 0 {
		t.Fatal("preparing a preview made a key")
	}
	if prepared, err = s.withPreviewKeys(ctx, projectID, name, prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := s.withPreviewKeys(ctx, projectID, name, prepared); err != nil {
		t.Fatal(err)
	}
	held, err := s.previewKeys(projectID, name).Keys.Held(ctx)
	if err != nil || len(held) != 1 || f.CallsOf("POST") != 1 {
		t.Fatalf("one key, kept: %+v %v, %d POSTs", held, err, f.CallsOf("POST"))
	}
	key := f.Keys()[held[0].Hash]
	if *key.Limit != 1 || key.Reset != nil || prepared.Secrets["web"]["LLM_API_KEY"] != key.Value {
		t.Fatalf("preview key: limit %v, given %q", *key.Limit, prepared.Secrets["web"]["LLM_API_KEY"])
	}

	// Booting (held): kept. Neither booting nor standing: revoked.
	unlock := s.holdPreview(name)
	if err := s.ReleaseEndedPreviewKeys(ctx); err != nil || len(f.Keys()) != 1 {
		t.Fatalf("a booting preview keeps its key: %v", err)
	}
	unlock()
	if err := s.ReleaseEndedPreviewKeys(ctx); err != nil || len(f.Keys()) != 0 {
		t.Fatalf("an ended preview's key is revoked: %v, %d keys", err, len(f.Keys()))
	}
	if held, err := s.previewKeys(projectID, name).Keys.Held(ctx); err != nil || len(held) != 0 {
		t.Fatalf("the preview holds no key any more: %+v %v", held, err)
	}
	if _, _, err := lw.secrets.Vault.Get(ctx, "llm:preview:"+name, integration.LLMKeyName); !errors.Is(err, secret.ErrNotStored) {
		t.Fatalf("its stored value goes too: %v", err)
	}

	other := docker.PreviewRunName(idstest.N(9))
	if _, err := s.withPreviewKeys(ctx, projectID, other, prepared); err != nil {
		t.Fatal(err)
	}
	f.FailNext("DELETE", 503)
	if err := s.ReleaseEndedPreviewKeys(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := s.retained.ForProject(ctx, projectID)
	if err != nil || len(rows) != 1 || rows[0].Need != "AI models key" || rows[0].Deployment != other || rows[0].Handle["hash"] == "" {
		t.Fatalf("a failed revoke is retained: %+v %v", rows, err)
	}
	if len(f.Keys()) != 1 {
		t.Fatal("the fake revoked a key it refused to")
	}
	if held, _ := s.previewKeys(projectID, other).Keys.Held(ctx); len(held) != 0 {
		t.Fatal("the retained key stays off the preview's list")
	}
}

// Previews follow the attachment rule too.
func TestAPreviewWithoutTheAttachmentGetsNoKey(t *testing.T) {
	t.Parallel()
	s, _, projectID, f := llmBootWorld(t, false)
	c, ws := llmContract(t, llmAppFixture)
	prepared, err := s.preparePreview(t.Context(), projectID, "p", "forge-preview-change-1", ws, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.withPreviewKeys(context.Background(), projectID, docker.PreviewChangeName(idstest.N(1)), prepared); !errors.Is(err, integration.ErrLLMNotAttached) || len(f.Calls()) != 0 {
		t.Fatalf("want not attached and no calls: %v, %d", err, len(f.Calls()))
	}
}

// A change's preview boots with its own preview key, kept while AppTown
// keeps the preview, revoked once the change's preview is discarded.
func TestAChangePreviewBootsWithItsOwnKey(t *testing.T) {
	t.Parallel()
	s, lw, projectID, f := llmBootWorld(t, true)
	client := lw.client
	ws := filepath.Join(s.dataDir, "workspaces", "change-00000000-0007-7000-8000-000000000000")
	llmWorkspace(t, ws, llmAppFixture)
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(7), ProjectID: projectID}, false); err != nil {
		t.Fatal(err)
	}
	name := docker.PreviewChangeName(idstest.N(7))
	held, err := s.previewKeys(projectID, name).Keys.Held(context.Background())
	if err != nil || len(held) != 1 {
		t.Fatalf("the preview owns a key: %+v %v", held, err)
	}
	key := f.Keys()[held[0].Hash]
	if !strings.Contains(runCalls(client), "LLM_API_KEY="+key.Value) || *key.Limit != 1 {
		t.Fatalf("the preview is given its key:\n%s", runCalls(client))
	}
	if err := s.ReleaseEndedPreviewKeys(context.Background()); err != nil || len(f.Keys()) != 1 {
		t.Fatalf("a running preview keeps its key: %v", err)
	}
	if err := s.DiscardChangePreview(context.Background(), idstest.N(7)); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseEndedPreviewKeys(context.Background()); err != nil || len(f.Keys()) != 0 {
		t.Fatalf("a discarded preview's key is revoked: %v, %d", err, len(f.Keys()))
	}
}

// A preview the age reaper removed has ended, though AppTown's record of
// what it served was not touched: its key is revoked.
func TestAReapedPreviewsKeyIsRevoked(t *testing.T) {
	t.Parallel()
	s, lw, pid, f := llmBootWorld(t, true)
	client := lw.client
	llmWorkspace(t, filepath.Join(s.dataDir, "workspaces", "change-00000000-0007-7000-8000-000000000000"), llmAppFixture)
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(7), ProjectID: pid}, false); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseEndedPreviewKeys(context.Background()); err != nil || len(f.Keys()) != 1 {
		t.Fatalf("a standing preview keeps its key: %v", err)
	}
	// What the reaper does (maintenance/containers.go): remove the
	// container, then forget the preview row.
	name := docker.PreviewChangeName(idstest.N(7))
	(docker.Scope{Client: client}).Remove(context.Background(), name)
	if err := s.deployments.ForgetPreviewContainer(t.Context(), name); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseEndedPreviewKeys(context.Background()); err != nil || len(f.Keys()) != 0 {
		t.Fatalf("a reaped preview still has %d keys: %v", len(f.Keys()), err)
	}
}

// A worker preview removed with its project has ended: its key is revoked.
// A worker that cannot be asked keeps every key.
func TestAWorkerPreviewRemovedWithItsProjectLosesItsKey(t *testing.T) {
	t.Parallel()
	lw, f := llmWorld(t, true)
	s, w, _, dir, pid := onWorker(t, lw)
	llmWorkspace(t, filepath.Join(dir, "workspaces", "change-00000000-0007-7000-8000-000000000000"), llmAppFixture)
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(7), ProjectID: pid}, false); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseEndedPreviewKeys(context.Background()); err != nil || len(f.Keys()) != 1 {
		t.Fatalf("a preview standing on the worker keeps its key: %v", err)
	}
	name := docker.PreviewChangeName(idstest.N(7))
	if err := s.RemovePreviewForGood(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	if w.holds(name) {
		t.Fatal("fixture: the worker still holds the preview")
	}
	w.mu.Lock()
	w.err = errors.New("worker away")
	w.mu.Unlock()
	if err := s.ReleaseEndedPreviewKeys(context.Background()); err == nil || len(f.Keys()) != 1 {
		t.Fatalf("a worker that cannot be asked keeps the key: %v, %d", err, len(f.Keys()))
	}
	w.mu.Lock()
	w.err = nil
	w.mu.Unlock()
	if err := s.ReleaseEndedPreviewKeys(context.Background()); err != nil || len(f.Keys()) != 0 {
		t.Fatalf("a removed worker preview still has %d keys: %v", len(f.Keys()), err)
	}
}

// A worker boot that finishes while the sweep looks keeps its key: the
// sweep never decides from a list taken before it held the preview's name.
func TestAWorkerBootFinishingDuringTheSweepKeepsItsKey(t *testing.T) {
	t.Parallel()
	lw, f := llmWorld(t, true)
	s, w, _, dir, pid := onWorker(t, lw)
	c, _ := llmContract(t, llmAppFixture)
	name := docker.PreviewChangeName(idstest.N(7))
	prepared, err := s.preparePreview(t.Context(), pid, "p", name, dir, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The boot holds its name and has its key; the worker has not made
	// the preview yet. It finishes right after the first list is answered.
	unlock := s.holdPreview(name)
	if _, err := s.withPreviewKeys(context.Background(), pid, name, prepared); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.afterList = func() { w.put(name); unlock() }
	w.mu.Unlock()
	if err := s.ReleaseEndedPreviewKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	pending := w.afterList != nil // the sweep never listed while the boot held the name
	w.afterList = nil
	w.mu.Unlock()
	if pending {
		w.put(name)
		unlock()
	}
	if err := s.ReleaseEndedPreviewKeys(context.Background()); err != nil || len(f.Keys()) != 1 {
		t.Fatalf("a preview that booted keeps its key: %v, %d keys", err, len(f.Keys()))
	}
}

// A preview opened again while its old key awaits the sweep ends up with
// a live key: its boot waits for the sweep
// that holds its name, and makes a key if the sweep revoked the old one.
func TestAPreviewOpenedAgainDuringTheSweepHasALiveKey(t *testing.T) {
	t.Parallel()
	lw, f := llmWorld(t, true)
	s, w, _, dir, pid := onWorker(t, lw)
	c, _ := llmContract(t, llmAppFixture)
	name := docker.PreviewChangeName(idstest.N(7))
	prepared, err := s.preparePreview(t.Context(), pid, "p", name, dir, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.withPreviewKeys(context.Background(), pid, name, prepared); err != nil {
		t.Fatal(err)
	}
	// The preview ended; its key awaits the sweep. As the sweep lists, the
	// preview is opened again: that boot holds the name, reuses (or makes)
	// the key and finishes.
	booted := make(chan error, 1)
	w.mu.Lock()
	w.afterList = func() {
		go func() {
			unlock := s.holdPreview(name)
			defer unlock()
			_, err := s.withPreviewKeys(context.Background(), pid, name, prepared)
			w.put(name)
			booted <- err
		}()
		select { // give a boot that need not wait the time to finish
		case err := <-booted:
			booted <- err
		case <-time.After(300 * time.Millisecond):
		}
	}
	w.mu.Unlock()
	if err := s.ReleaseEndedPreviewKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-booted; err != nil {
		t.Fatal(err)
	}
	held, err := s.previewKeys(pid, name).Keys.Held(context.Background())
	if err != nil || len(held) != 1 {
		t.Fatalf("the reopened preview holds a key: %+v %v", held, err)
	}
	if _, live := f.Keys()[held[0].Hash]; !live || !w.holds(name) {
		t.Fatalf("the reopened preview's key is live: %v", live)
	}
}

// A preview key's revocation that the sweep's caller stopped is not a key
// that could not be revoked: nothing is retained for a person, and the next
// sweep finishes the release.
//
// Kills: a stopped revocation recorded as a retained key.
func TestAStoppedRevocationRetainsNothing(t *testing.T) {
	t.Parallel()
	s, _, projectID, f := llmBootWorld(t, true)
	ctx := context.Background()
	c, ws := llmContract(t, llmAppFixture)
	name := docker.PreviewChangeName(idstest.N(7))
	prepared, err := s.preparePreview(ctx, projectID, "p", name, ws, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.withPreviewKeys(ctx, projectID, name, prepared); err != nil {
		t.Fatal(err)
	}
	stopped, stop := context.WithCancel(ctx)
	f.Before = func(method string) {
		if method == "DELETE" {
			stop() // the sweep's caller stops while the key is revoked
		}
	}
	_ = s.ReleaseEndedPreviewKeys(stopped)
	f.Before = nil
	if rows, err := s.retained.ForProject(ctx, projectID); err != nil || len(rows) != 0 {
		t.Fatalf("a stopped revocation was retained: %+v %v", rows, err)
	}
	if err := s.ReleaseEndedPreviewKeys(ctx); err != nil {
		t.Fatal(err)
	}
	if held, err := s.previewKeys(projectID, name).Keys.Held(ctx); err != nil || len(held) != 0 {
		t.Fatalf("the next sweep did not finish the release: %+v %v", held, err)
	}
	if rows, err := s.retained.ForProject(ctx, projectID); err != nil || len(rows) != 0 {
		t.Fatalf("the finished release retained: %+v %v", rows, err)
	}
}

// A preview's key whose revocation failed is retained and forgotten here in
// one transaction: when the retained row cannot be written, the preview
// keeps the key on its own list, so the key is never off both lists, and
// nothing is retained; the next sweep revokes it.
//
// Kills: forgetting the key outside the retained row's transaction.
func TestAKeyThatCannotBeRetainedStaysThePreviews(t *testing.T) {
	t.Parallel()
	s, w, projectID, f := llmBootWorld(t, true)
	ctx := context.Background()
	c, ws := llmContract(t, llmAppFixture)
	name := docker.PreviewChangeName(idstest.N(7))
	prepared, err := s.preparePreview(ctx, projectID, "p", name, ws, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.withPreviewKeys(ctx, projectID, name, prepared); err != nil {
		t.Fatal(err)
	}
	f.FailNext("DELETE", 503)
	dbtest.FailNextStatement(t, w.db, "-- name: InsertRetainedResource")
	if err := s.ReleaseEndedPreviewKeys(ctx); err == nil {
		t.Fatal("a key that could be neither revoked nor retained was released")
	}
	if held, err := s.previewKeys(projectID, name).Keys.Held(ctx); err != nil || len(held) != 1 {
		t.Fatalf("the preview no longer holds the key: %+v %v", held, err)
	}
	if rows, err := s.retained.ForProject(ctx, projectID); err != nil || len(rows) != 0 {
		t.Fatalf("a retained row was written: %+v %v", rows, err)
	}
	if err := s.ReleaseEndedPreviewKeys(ctx); err != nil || len(f.Keys()) != 0 {
		t.Fatalf("the next sweep revokes it: %v, %d keys", err, len(f.Keys()))
	}
}
