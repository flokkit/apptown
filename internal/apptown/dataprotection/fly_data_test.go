package dataprotection

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/fly"
	"github.com/flokkit/apptown/internal/deploy/fly/flyfake"
	"github.com/flokkit/apptown/internal/deploy/s3bucket/s3fake"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/oci"
	"github.com/flokkit/apptown/stock"
)

type flyDataWorld struct {
	s                *both
	api              *flyfake.API
	store            *s3fake.Store
	project          ids.ID
	dep              *deployment.Deployment
	driver           *fly.Driver
	failUpload       bool
	copied, restored []string
}

func newFlyDataWorld(t *testing.T) *flyDataWorld {
	t.Helper()
	ctx := context.Background()
	db := testDB(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("FLY_API_TOKEN=org-token\nSTORE_KEY=STOREKEY\nSTORE_SECRET=storesecret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	vault := secret.NewService(db, secret.Config{DataDir: dir})
	catalog := dependency.NewCatalog(db, nil)
	if _, _, err := catalog.SyncStock(ctx, stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	integrations := integration.NewService(db, integration.Dependencies{Kinds: catalog, Secrets: vault})
	if _, err := integrations.Bindings.Save(ctx, "fly-syd", "cloud-account", "fly", `{"tokenFrom":"FLY_API_TOKEN","org":"acme","region":"syd"}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	api := flyfake.New("acme")
	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)
	auth := &grants{who: "alice", deny: map[string]bool{}}
	w := &flyDataWorld{api: api, project: createTestProject(t, db, "fly-data", "Fly data"), store: s3fake.New(t, "STOREKEY")}
	w.store.AddBucket("copies")
	w.s = assembled(db, testRuntime{Installation: "0193a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b", DataDir: dir, Secrets: vault, Needs: catalog, Integrations: integrations, NoLocalDocker: true, ChownImage: "reg/helper@sha256:" + strings.Repeat("e", 64), Fly: fly.Options{APIBase: server.URL, CopyImage: func(_ context.Context, from oci.Source, _ oci.Repository, _ string) (string, error) {
		if _, digest, ok := strings.Cut(from.Reference, "@"); ok {
			return digest, nil
		}
		return "sha256:" + strings.Repeat("d", 64), nil
	}}}, deployment.Dependencies{Authority: auth, Releases: deployment.ReleaseDependencies{Sources: &releasedContracts{}}})
	target, err := w.s.Targets.Put(ctx, &deployment.Target{Name: "fly-syd", Kind: "fly-machines", Provider: "fly-syd", Address: map[string]any{"org": "acme", "region": "syd"}, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	w.dep, err = w.s.Deployments.EnsureStanding(ctx, w.project, "prod", target.ID, deployment.Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	w.driver, err = w.s.providers.FlyData(ctx, target, w.dep)
	if err != nil {
		t.Fatal(err)
	}
	yaml := "app:\n  web:\n    image: reg/app:1\n    ports: ['8080']\n    public: true\n    dataDir: /data\nneeds: {postgres: {}}\n"
	c, err := appcontract.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	sources := w.s.sources.(*releasedContracts)
	sources.by = map[string]string{"rev": yaml}
	at, _, err := w.s.Outcomes.Admit(ctx, deployment.Start{DeploymentID: w.dep.ID, TargetID: target.ID, Key: "fly-release", Inputs: deployment.Inputs{Revision: "rev"}})
	if err != nil {
		t.Fatal(err)
	}
	art, err := w.driver.Render(ctx, w.dep.Placement(), deploy.Source{Contract: c, Revision: "rev", Images: map[string]string{"web": "reg/app@sha256:" + strings.Repeat("a", 64)}}, nil, flySecrets{}, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	ob, err := w.driver.Reconcile(ctx, w.dep.Placement(), at.Generation, art, "release", func(string, ...any) {}, func(map[string]any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.s.Outcomes.Complete(ctx, deployment.Completion{Attempt: at, Observed: ob}); err != nil {
		t.Fatal(err)
	}
	w.dep, err = w.s.Deployments.ByID(ctx, w.dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	api.OnCreate = func(_ string, m *flyfake.Machine) {
		if m.Metadata()["apptown_role"] != "copy" {
			return
		}
		env, _ := m.Config["env"].(map[string]any)
		objectURL, _ := env["APPTOWN_COPY_URL"].(string)
		if strings.Contains(fmt.Sprint(m.Config), "storesecret") {
			t.Error("store key entered a job")
		}
		script := m.Config["init"].(map[string]any)["cmd"].([]any)[0].(string)
		if strings.Contains(script, "--upload-file") {
			if w.failUpload {
				flyfake.Exit(m, 1, false)
				return
			}
			body := "directory contents"
			if strings.Contains(script, "pg_dump") {
				body = "database export"
				w.copied = append(w.copied, "postgres")
			} else {
				w.copied = append(w.copied, "volume")
			}
			req, _ := http.NewRequest("PUT", objectURL, strings.NewReader(body))

			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Error(err)
				flyfake.Exit(m, 1, false)
				return
			}
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
			if res.StatusCode != 200 {
				t.Errorf("upload: %d", res.StatusCode)
			}
		} else {
			if env["APPTOWN_COPY_SHA256"] == "" {
				t.Error("restore has no recorded digest")
			}
			if strings.Contains(script, "pg_restore") {
				if !strings.Contains(script, "pg_restore -l") && !strings.Contains(script, "pg_restore --list") {
					t.Error("restore did not run catalogue verify")
				}
				w.restored = append(w.restored, "postgres")
			} else {
				w.restored = append(w.restored, "volume")
			}
		}
	}
	return w
}

func (w *flyDataWorld) attach(t *testing.T) {
	t.Helper()
	if _, err := w.s.AttachStore(context.Background(), AttachStore{Endpoint: w.store.API.URL, Region: "test", Bucket: "copies", AccessKeyFrom: "STORE_KEY", SecretKeyFrom: "STORE_SECRET"}); err != nil {
		t.Fatal(err)
	}
}

func TestFlySetRecordsStoreBytesAndFailedUploadReturnsThePart(t *testing.T) {
	w := newFlyDataWorld(t)
	if _, err := w.s.TakeBackup(t.Context(), w.project, "prod", "alice"); err == nil || !strings.Contains(err.Error(), errNoStore.Error()) {
		t.Fatalf("backup without store: %v", err)
	}
	w.attach(t)
	b, err := w.s.TakeBackup(t.Context(), w.project, "prod", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != BackupOK || b.Store != StoreBackupStore || len(b.Items) != 2 {
		t.Fatalf("set: %+v", b)
	}
	for _, it := range b.Items {
		body := "directory contents"
		if it.Kind == "need" {
			body = "database export"
			if it.Copy != CopyDeclared {
				t.Fatal("database not declared export")
			}
		} else if it.Copy != copyFiles {
			t.Fatal("directory was not copied while stopped")
		}
		sum := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
		if it.Bytes != int64(len(body)) || it.SHA256 != sum {
			t.Fatalf("store bytes not recorded: %+v", it)
		}
	}
	w.failUpload = true
	b, err = w.s.TakeBackup(t.Context(), w.project, "prod", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != BackupFailed {
		t.Fatalf("upload failure passed: %+v", b)
	}
	ms, err := w.driver.DataMachines(t.Context(), w.dep.Placement())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Role == "part" && m.State != "started" {
			t.Fatal("failed upload left part down")
		}
		if m.Role == "need" && m.State != "started" {
			t.Fatal("declared export stopped need")
		}
	}
}

func TestFlyFinalSetDeletesAppAndVolumesAndNoStoreNeedsKeepChoice(t *testing.T) {
	for _, store := range []bool{false, true} {
		t.Run(fmt.Sprint(store), func(t *testing.T) {
			w := newFlyDataWorld(t)
			if store {
				w.attach(t)
			}
			owned, err := w.s.Deployments.Owned(t.Context(), w.dep.ID)
			if err != nil {
				t.Fatal(err)
			}
			target, err := w.s.Targets.ByID(t.Context(), w.dep.TargetID)
			if err != nil {
				t.Fatal(err)
			}
			final, err := w.s.finalBackup(t.Context(), w.dep, target, owned, "alice")
			if !store {
				if _, ok := err.(*finalCopyUnavailable); !ok {
					t.Fatalf("missing store did not require keep-data choice: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if final.set == nil || final.set.Status != BackupOK {
				t.Fatalf("final: %+v", final)
			}
			if _, err := w.driver.Teardown(t.Context(), w.dep.Placement(), w.dep.ObservedGeneration, deploy.TakeDown{Owned: owned, BackedUp: true}); err != nil {
				t.Fatal(err)
			}
			if len(w.api.Apps()) != 0 {
				t.Fatal("final take-down left app or volumes")
			}
		})
	}
}

type flySecrets struct{}

func (flySecrets) EnvFor(string) map[string]string { return nil }

func TestFlyRestoreTakesSafetySetStopsWritersAndRestoresDatabaseAndNewVolume(t *testing.T) {
	w := newFlyDataWorld(t)
	w.attach(t)
	b, err := w.s.TakeBackup(t.Context(), w.project, "prod", "alice")
	if err != nil {
		t.Fatal(err)
	}
	var old string
	for _, it := range b.Items {
		if it.Kind == "volume" {
			old = it.Volume
		}
	}
	result, err := w.s.restoreBackup(t.Context(), nil, w.project, "prod", b.ID, "alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.SafetyBackup.IsZero() || result.Started == nil || !*result.Started {
		t.Fatalf("restore: %+v", result)
	}
	if len(result.Items) != 2 {
		t.Fatalf("items: %+v", result.Items)
	}
	for _, it := range result.Items {
		if it.Kind == "need" && it.Outcome != restoreVerified {
			t.Fatalf("database not verified: %+v", it)
		}
		if it.Kind == "volume" && it.Outcome != restoreCopied {
			t.Fatalf("files not copied: %+v", it)
		}
	}
	ms, err := w.driver.DataMachines(t.Context(), w.dep.Placement())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Role == "part" {
			if m.State != "started" || len(m.Volumes) != 1 || m.Volumes[0].ID == old {
				t.Fatalf("restore did not switch to the filled volume: %+v", m)
			}
		}
	}
}

func TestFlyKeepChoiceUsesTheRemovalDoorAndCannotDeleteDataAfterStoreAppears(t *testing.T) {
	for _, store := range []bool{false, true} {
		t.Run(fmt.Sprint(store), func(t *testing.T) {
			w := newFlyDataWorld(t)
			if _, err := w.s.TakeDown(t.Context(), RemoveDeployment{ProjectID: w.project, Name: "prod"}); !errors.Is(err, errNoFinalCopy) {
				t.Fatalf("missing store was not refused: %v", err)
			}
			if store {
				w.attach(t)
			}
			out, err := w.s.TakeDown(t.Context(), RemoveDeployment{ProjectID: w.project, Name: "prod", WithoutFinalCopy: "prod"})
			if store {
				if err == nil || !strings.Contains(err.Error(), "confirm the take-down again") {
					t.Fatalf("stale keep choice changed to deleting data: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if out.Removed == nil || len(out.Removed.Retained) == 0 {
				t.Fatalf("no retained resource recorded: %+v", out)
			}
			for _, name := range w.api.Apps() {
				app := w.api.App(name)
				if len(app.Volumes) != 2 {
					t.Fatal("kept data was removed")
				}
				for _, m := range app.Machines {
					if m.State != "stopped" {
						t.Fatal("retained data is still served")
					}
				}
			}
		})
	}
}

func TestFlyReleaseAndDailyBackupUseSetsAndMissingStoreNeedsAcceptance(t *testing.T) {
	w := newFlyDataWorld(t)
	target, err := w.s.Targets.ByID(t.Context(), w.dep.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	if why := w.s.CannotCopy(t.Context(), w.dep, target); !strings.Contains(why, errNoStore.Error()) {
		t.Fatalf("release did not need uncopied-data acceptance without store: %s", why)
	}
	w.attach(t)
	if why := w.s.CannotCopy(t.Context(), w.dep, target); why != "" {
		t.Fatal(why)
	}
	witness, err := w.s.BeforeRelease(t.Context(), w.project, "prod", false)
	if err != nil {
		t.Fatal(err)
	}
	if !witness.Complete() || witness.copy == nil {
		t.Fatal("no pre-release set")
	}
	setDataFeature(t, w.s.db, "backups", true)
	asks := &flyBackupAsks{}
	if _, err := (JobWorker{Service: w.s.Service, Backups: asks}).Sweep(t.Context(), "daily", nil); err != nil {
		t.Fatal(err)
	}
	if len(asks.keys) != 1 || asks.keys[0] != w.project.String()+"/prod" {
		t.Fatalf("daily sweep did not ask for Fly set: %v", asks.keys)
	}
}

type flyBackupAsks struct{ keys []string }

func (a *flyBackupAsks) RequestBackup(_ context.Context, key, _ string) error {
	a.keys = append(a.keys, key)
	return nil
}

func TestFlyProtectionReadsTheStoreRequirementAndKeepDataMeaning(t *testing.T) {
	w := newFlyDataWorld(t)
	collectProtectionForTest(t, w.s, w.project, "prod")
	p, err := w.s.DeploymentProtection(t.Context(), w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 2 || p.Operations == nil || p.Operations.FinalCopy.Available || !strings.Contains(p.TakeDown, "every Machine stopped") || !strings.Contains(p.Note, errNoStore.Error()) {
		t.Fatalf("protection without store: %+v", p)
	}
	w.attach(t)
	if _, err := w.s.TakeBackup(t.Context(), w.project, "prod", "alice"); err != nil {
		t.Fatal(err)
	}
	collectProtectionForTest(t, w.s, w.project, "prod")
	p, err = w.s.DeploymentProtection(t.Context(), w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if !p.OffHost || !p.Operations.FinalCopy.Available || !strings.Contains(p.TakeDown, "then deletes the Fly app and its volumes") {
		t.Fatalf("protection with store: %+v", p)
	}
}

// A Fly deployment's page names where it runs from its target's own
// description, not a Docker host's or App Platform's words: the doors that
// do not apply there say why in terms of this deployment.
func TestFlyProtectionNamesFlyNotADockerHost(t *testing.T) {
	w := newFlyDataWorld(t)
	collectProtectionForTest(t, w.s, w.project, "prod")
	p, err := w.s.DeploymentProtection(t.Context(), w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if p.Operations == nil || p.Operations.Copy.Reason != "backups-page" || p.Operations.Copy.Why != "A deployment on fly-syd (Fly.io Machines) is backed up daily and from the Backups page." {
		t.Fatalf("copy: %+v", p.Operations)
	}
	for what, said := range map[string]string{"copy": p.Operations.Copy.Why, "check": p.Operations.Check.Why, "restore": p.Operations.Restore.Why, "restore test": p.RestoreTestNote} {
		if said == "" || strings.Contains(said, "Docker host") {
			t.Errorf("%s says %q on a Fly deployment", what, said)
		}
	}
}

func TestFlyCanKeepDataWhenItsNeedCannotBeCopiedEvenWithAStore(t *testing.T) {
	w := newFlyDataWorld(t)
	w.attach(t)
	for _, name := range w.api.Apps() {
		for _, m := range w.api.App(name).Machines {
			if m.Metadata()["apptown_role"] == "need" {
				w.api.Set(name, m.ID, "stopped", "ok", "")
			}
		}
	}
	collectProtectionForTest(t, w.s, w.project, "prod")
	p, err := w.s.DeploymentProtection(t.Context(), w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if p.Operations.FinalCopy.Available || !strings.Contains(p.TakeDown, "keeps the Fly app") {
		t.Fatalf("an unavailable export was offered as a final copy: %+v", p)
	}
	if _, err := w.s.TakeDown(t.Context(), RemoveDeployment{ProjectID: w.project, Name: "prod"}); !errors.Is(err, errNoFinalCopy) {
		t.Fatalf("take-down did not require a keep-data choice: %v", err)
	}
	out, err := w.s.TakeDown(t.Context(), RemoveDeployment{ProjectID: w.project, Name: "prod", WithoutFinalCopy: "prod"})
	if err != nil || out.Removed == nil || len(out.Removed.Retained) != 1 {
		t.Fatalf("keep-data take-down: %+v %v", out, err)
	}
}

// A final backup that was tried and failed leaves the person a way out:
// the choice to go without one, which keeps the app and its volumes. A
// later whole set makes a final copy possible again, so the choice is
// then refused as before.
func TestFlyKeepChoiceStandsAfterTheFinalBackupFailed(t *testing.T) {
	w := newFlyDataWorld(t)
	w.attach(t)
	w.failUpload = true
	_, err := w.s.TakeDown(t.Context(), RemoveDeployment{ProjectID: w.project, Name: "prod"})
	if !errors.Is(err, errFinalBackupIncomplete) || !strings.Contains(err.Error(), "without a final copy") {
		t.Fatalf("failed final backup did not refuse with the way out: %v", err)
	}
	w.failUpload = false
	if _, err := w.s.TakeBackup(t.Context(), w.project, "prod", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.TakeDown(t.Context(), RemoveDeployment{ProjectID: w.project, Name: "prod", WithoutFinalCopy: "prod"}); err == nil || !strings.Contains(err.Error(), "confirm the take-down again") {
		t.Fatalf("a whole set newer than the failed final one did not make a copy possible: %v", err)
	}
	w.failUpload = true
	if _, err := w.s.TakeDown(t.Context(), RemoveDeployment{ProjectID: w.project, Name: "prod"}); !errors.Is(err, errFinalBackupIncomplete) {
		t.Fatalf("failed final backup: %v", err)
	}
	out, err := w.s.TakeDown(t.Context(), RemoveDeployment{ProjectID: w.project, Name: "prod", WithoutFinalCopy: "prod"})
	if err != nil {
		t.Fatalf("the choice to go without a final copy was refused after the final backup failed: %v", err)
	}
	if out.Removed == nil || len(out.Removed.Retained) == 0 {
		t.Fatalf("no retained resource recorded: %+v", out)
	}
	for _, name := range w.api.Apps() {
		if len(w.api.App(name).Volumes) != 2 {
			t.Fatal("kept data was removed")
		}
	}
}

// A Fly deployment that was never released holds nothing at Fly: it is
// taken down with no final copy and no choice to go without one.
func TestFlyNeverReleasedDeploymentNeedsNoFinalCopy(t *testing.T) {
	w := newFlyDataWorld(t)
	w.attach(t)
	if _, err := w.s.Deployments.EnsureStanding(t.Context(), w.project, "staging", w.dep.TargetID, deployment.Attributes{}.WithDefaults()); err != nil {
		t.Fatal(err)
	}
	out, err := w.s.TakeDown(t.Context(), RemoveDeployment{ProjectID: w.project, Name: "staging"})
	if err != nil {
		t.Fatalf("a never-released deployment needed a final copy: %v", err)
	}
	if out.Removed == nil || out.Removed.FinalBackup != nil || len(out.Removed.Retained) != 0 {
		t.Fatalf("take-down: %+v", out.Removed)
	}
	if len(w.api.Apps()) != 1 {
		t.Fatal("the other deployment's app was touched")
	}
}
