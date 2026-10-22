package dataprotection

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
)

// sharedRehearsalWorld is one Docker host running a deployment, as a
// one-server install has it: no backup store, no host kept for
// rehearsals, a release whose images are on that host by ID (no
// registry), and a backup set on AppTown's disk.
type sharedRehearsalWorld struct {
	*noLocalWorld
	s    *both
	ops  *operations.Service
	host *rehearsalHost
	set  *Backup
}

const sharedRehearsalApp = "app:\n  web:\n    image: example/game:1\n    ports: [\"8080\"]\n    public: true\n    dataDir: /data\n    health: { readiness: { http: /healthz } }\nrehearsal:\n  checks: [\"GET /api/scores\"]\n"

func newSharedRehearsalWorld(t *testing.T) *sharedRehearsalWorld {
	t.Helper()
	w := &sharedRehearsalWorld{noLocalWorld: noLocalDockerWorld(t, sharedRehearsalApp), host: newRehearsalHost()}
	if err := jobs.Migrate(context.Background(), w.db.Pool()); err != nil {
		t.Fatal(err)
	}
	auth := &grants{who: "alice", deny: map[string]bool{}}
	w.ops = operations.New(w.db, auth, auth, nil)
	projects := project.New(w.db, nil)
	secrets := secret.NewService(w.db, secret.Config{DataDir: w.dir})
	w.s = assembled(w.db, testRuntime{Scope: docker.Scope{Client: w.daemon}, Probe: &docker.Probe{Settle: time.Millisecond}, DataDir: w.dir, SSHConfigPath: filepath.Join(w.dir, "ssh-config"),
		Projects: projects, Secrets: secrets, Integrations: integration.NewService(w.db, integration.Dependencies{Projects: projects, Secrets: secrets}), Needs: dependency.NewCatalog(w.db, nil),
		ChownImage: "test-helper", BuiltInTarget: "local", NoLocalDocker: true},
		deployment.Dependencies{Operations: w.ops, Authority: auth, Followups: noFollowups{}, Configuration: deployment.ConfigurationDependencies{Projects: w.sources},
			Releases: deployment.ReleaseDependencies{Authority: &requestAuthority{}, Sources: w.sources, Secrets: requestSecrets{}, Queue: requestQueue{}}})
	ctx := context.Background()
	volumes := true
	remote, err := w.s.Targets.EditSettings(ctx, w.remote.Name, deployment.TargetSettingsEdit{ExpectedRevision: &w.remote.SettingsRevision, PersistentVolumes: &volumes}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	w.remote = remote
	if _, err := w.s.Deployments.EnsureStanding(ctx, w.project, "main", w.remote.ID, deployment.Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	// The release's image is on the host by ID, as a landing built it
	// there; it was never pushed to a registry.
	image := "sha256:" + strings.Repeat("d", 64)
	w.daemon.mu.Lock()
	w.daemon.images[image] = true
	w.daemon.mu.Unlock()
	w.host.held[image] = true
	bundle, err := w.s.Bundles.Cut(ctx, w.project, w.revision, map[string]string{"web": image}, nil, []byte(sharedRehearsalApp), deployment.BundleOrigin{TargetID: w.remote.ID})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := w.s.RequestRelease(ctx, deployment.DeploySelection{ProjectID: w.project, DeploymentName: "main", BundleID: bundle})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.s.ExecuteRelease(ctx, accepted.ID); err != nil {
		t.Fatal(err)
	}
	if rel, err := w.s.Releases.ByNumber(ctx, w.project, accepted.Number); err != nil || rel.State != deployment.Released {
		t.Fatalf("the release: %+v %v", rel, err)
	}
	w.daemon.SetCommandReply(func(req docker.Command) ([]byte, error, bool) {
		if req.Stdout != nil {
			_, err := req.Stdout.Write([]byte("data from the deployment"))
			return nil, err, true
		}
		return nil, nil, false
	})
	if w.set, err = w.s.TakeBackup(ctx, w.project, "main", "manual", "alice"); err != nil || w.set.Store != StoreAppTownDisk || w.set.Status != BackupOK {
		t.Fatalf("a set on AppTown's disk: %+v %v", w.set, err)
	}
	// The rehearsal's daemon is the deployment's own host's.
	w.s.hostScope = func(_ context.Context, t *deployment.Target) (docker.Scope, error) {
		if t.Name != w.remote.Name {
			return docker.Scope{}, errors.New("rehearsed on " + t.Name)
		}
		return docker.Scope{Client: w.host, Host: attachedHost}, nil
	}
	return w
}

func (w *sharedRehearsalWorld) rehearse(t *testing.T) *operations.Operation {
	t.Helper()
	op, err := w.s.RequestRehearsal(context.Background(), w.project, "main", ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.ops.Run(context.Background(), op.ID); err != nil {
		t.Fatal(err)
	}
	got, err := w.ops.ByID(context.Background(), op.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// With no host kept for rehearsals and no backup store, a deployment is
// rehearsed on its own host from its set on AppTown's disk, its release
// booted from the images that host holds; and the record, the receipt and
// the protection answer say the host was not one kept for rehearsals.
//
// Kills: refusing without a store or a kept host; refusing a release not
// in a registry on its own host; reading the store only; recording a
// shared host as kept.
func TestWithNoHostKeptARehearsalRunsOnTheAppsOwnHostAndSaysSo(t *testing.T) {
	t.Parallel()
	w := newSharedRehearsalWorld(t)
	op := w.rehearse(t)
	if !endedAs(op, OperationVerified) {
		t.Fatalf("rehearsal %s: %s", op.State, op.Detail)
	}
	if !strings.Contains(op.Detail, "on remote, not kept for rehearsals") {
		t.Fatalf("the operation does not say where it ran: %s", op.Detail)
	}
	// The set's copy reached the host from AppTown's disk, as recorded.
	staged := ""
	w.host.mu.Lock()
	for at, data := range w.host.volumes {
		if strings.HasSuffix(at, "/"+w.set.Items[0].File) {
			staged = data
		}
	}
	w.host.mu.Unlock()
	raw, err := os.ReadFile(filepath.Join(w.set.Path, w.set.Items[0].File))
	if err != nil || staged != string(raw) {
		t.Fatalf("staged %q, the set holds %q (%v)", staged, raw, err)
	}
	p, err := w.s.DeploymentProtection(context.Background(), w.project, "main")
	if err != nil || p.Rehearsal == nil || !p.Rehearsal.Passed || p.Rehearsal.Host != "remote" || !p.Rehearsal.Facts.SharedHost {
		t.Fatalf("the last rehearsal: %+v %v", p.Rehearsal, err)
	}
	receipts := operationReceipts(t, w.db, "deployment.rehearsed", w.project)
	if len(receipts) != 1 || !strings.Contains(string(receipts[0].Payload), `"sharedHost": true`) && !strings.Contains(string(receipts[0].Payload), `"sharedHost":true`) {
		t.Fatalf("the receipt: %+v", receipts)
	}
}

// A host kept for rehearsals takes every rehearsal: a release whose images
// are only on the deployment's own host cannot run there and is refused,
// and while the kept host does not answer, nothing runs on the deployment's
// own host instead.
//
// Kills: falling back to the shared host while one is kept; booting a
// release by image ID on a host that does not hold it.
func TestAKeptHostTakesEveryRehearsal(t *testing.T) {
	t.Parallel()
	w := newSharedRehearsalWorld(t)
	ctx := context.Background()
	kept, err := w.s.Targets.Put(ctx, &deployment.Target{Name: "rehearse-1", Kind: deployment.KindDockerHost, Origin: deployment.OriginAttached, Status: deployment.TargetReady,
		Address: map[string]any{"hostname": "rehearse-1.example", "user": "apptown", "keyFrom": "SSH"}})
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	if kept, err = w.s.Targets.EditSettings(ctx, "rehearse-1", deployment.TargetSettingsEdit{ExpectedRevision: &kept.SettingsRevision, Rehearsals: &yes}, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.RequestRehearsal(ctx, w.project, "main", ids.Nil); !errors.Is(err, ErrRehearsalSource) || !strings.Contains(err.Error(), "rehearse-1 cannot run its images") {
		t.Fatalf("a release not in a registry, rehearsed on a kept host: %v", err)
	}
	if _, err := w.s.Targets.RecordCheck(ctx, kept, false, "admin", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.RequestRehearsal(ctx, w.project, "main", ids.Nil); err == nil || !strings.Contains(err.Error(), "is not reachable") {
		t.Fatalf("the kept host down: %v", err)
	}
	if n := len(w.host.Commands()); n != 0 {
		t.Fatalf("the deployment's own host was asked %d things", n)
	}
}

// A set on AppTown's disk that no longer matches its record is not used:
// the rehearsal fails while staging it and starts nothing.
//
// Kills: trusting the disk without the set's digest.
func TestARehearsalHoldsADiskSetToItsRecord(t *testing.T) {
	t.Parallel()
	w := newSharedRehearsalWorld(t)
	file := filepath.Join(w.set.Path, w.set.Items[0].File)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("the fixture must hold data to corrupt")
	}
	raw[0] ^= 1 // preserve size so only the digest can detect the change
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	op := w.rehearse(t)
	if endedAs(op, OperationVerified) || !strings.Contains(op.Detail, "the set records") {
		t.Fatalf("a changed set: %s %s", op.State, op.Detail)
	}
	for _, c := range w.host.Commands() {
		if line := strings.Join(c, " "); strings.Contains(line, "--network-alias") || strings.Contains(line, "example/game") {
			t.Fatalf("the app or a need started from a changed set: %s", line)
		}
	}
}
