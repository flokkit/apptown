package dataprotection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/deploy/s3bucket/s3fake"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/stock"
)

// offhostWorld is a production deployment "main" on AppTown's built-in
// Docker host, holding an app volume and Postgres, with a backup store it
// can attach (design/subsystems/deployment.md, "Backups and restore").
type offhostWorld struct {
	t      *testing.T
	s      *both
	client *dockerfake.Daemon
	store  *s3fake.Store
	dir    string
	pid    ids.ID
	slug   string

	mu       sync.Mutex
	restored []string // "<file>: <bytes>" each restore read on its stdin
	stops    int
}

func newOffhostWorld(t *testing.T, slug string, port int) *offhostWorld {
	t.Helper()
	db := testDB(t)
	w := &offhostWorld{t: t, pid: createTestProject(t, db, slug, strings.ToUpper(slug)), dir: t.TempDir(), slug: slug, client: dockerfake.New()}
	if err := os.WriteFile(filepath.Join(w.dir, "secrets.env"), []byte("STORE_KEY=STOREKEY\nSTORE_SECRET=storesecret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := dependency.NewCatalog(db, nil)
	if _, _, err := catalog.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	auth := &grants{who: "alice", deny: map[string]bool{}}
	w.s = assembled(db, testRuntime{Scope: docker.Scope{Client: w.client}, DataDir: w.dir, Secrets: secret.NewService(db, secret.Config{DataDir: w.dir}), Needs: catalog, ChownImage: "test-helper", BuiltInTarget: "local"}, deployment.Dependencies{Authority: auth, Releases: deployment.ReleaseDependencies{Sources: &releasedContracts{}}})
	target, err := w.s.Targets.Put(context.Background(), &deployment.Target{Name: "local", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Deployments.EnsureStanding(t.Context(), w.pid, "main", target.ID, deployment.Attributes{DataClass: "production"}.WithDefaults()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Deployments.ReserveHostPort(t.Context(), w.pid, "main", docker.Current.Container(slug, "main"), port); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(w.dir, "workspaces", "project-"+slug, ".apptown")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := "app:\n  web:\n    image: registry.example/" + slug + ":1\n    ports: [\"8080\"]\n    public: true\n    dataDir: /data\nneeds:\n  postgres: {}\n"
	if err := os.WriteFile(filepath.Join(repo, "app.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	releaseMain(t, w.s, slug, yaml)
	w.store = s3fake.New(t, "STOREKEY")
	w.store.AddBucket("forge-copies")
	setBackupDockerCommand(w.client, func(req docker.Command) ([]byte, error) {
		switch {
		case req.Stdout != nil:
			_, err := io.WriteString(req.Stdout, "copy of "+filepath.Base(req.Stdout.(*os.File).Name()))
			return nil, err
		case req.Stdin != nil:
			raw, err := io.ReadAll(req.Stdin)
			w.mu.Lock()
			w.restored = append(w.restored, filepath.Base(req.Stdin.(*os.File).Name())+": "+string(raw))
			w.mu.Unlock()
			return nil, err
		case req.Args[0] == "stop":
			w.mu.Lock()
			w.stops++
			w.mu.Unlock()
		}
		return nil, nil
	})
	return w
}

func (w *offhostWorld) attach() {
	w.t.Helper()
	if _, err := w.s.AttachStore(context.Background(), AttachStore{Endpoint: w.store.API.URL, Region: "syd1", Bucket: "forge-copies", AccessKeyFrom: "STORE_KEY", SecretKeyFrom: "STORE_SECRET"}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *offhostWorld) take(trigger string) *Backup {
	w.t.Helper()
	b, err := w.s.TakeBackup(context.Background(), w.pid, "main", trigger, "alice")
	if err != nil {
		w.t.Fatal(err)
	}
	return b
}

func (w *offhostWorld) sets() []Backup {
	w.t.Helper()
	sets, err := w.s.backups.ForDeployment(context.Background(), w.pid, "main", 100)
	if err != nil {
		w.t.Fatal(err)
	}
	return sets
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// stagingEntries are what is left staged on AppTown's disk.
func (w *offhostWorld) stagingEntries() []string {
	entries, _ := os.ReadDir(filepath.Join(w.dir, "backups", ".staging"))
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// Without a store a Docker host's backups stay on AppTown's disk, and
// its protection says losing that disk loses them; a rehearsal is refused
// at its door with the sentence the card shows. With a store attached,
// every backup goes to the store: each copy held to its digest there, the
// database's copy made by its kind's portable export, nothing left on the
// AppTown's disk, and the deployment off-host.
//
// Kills: writing to the disk with a store attached; recording a format
// without running the kind's export; recording a copy the store did not
// take as it was made; leaving staging behind; a "never rehearsed" with no
// store; a card and a door that disagree.
func TestWithAStoreEveryDockerBackupGoesToTheStore(t *testing.T) {
	t.Parallel()
	w := newOffhostWorld(t, "oh", 40100)
	ctx := context.Background()
	disk := w.take("manual")
	if disk.Store != StoreAppTownDisk || !strings.HasPrefix(disk.Path, filepath.Join(w.dir, "backups", "oh", "main")) {
		t.Fatalf("without a store: %+v", disk)
	}
	p, err := w.s.DeploymentProtection(ctx, w.pid, "main")
	if err != nil || p.OffHost || p.Note != OnAppTownDiskNote || p.Operations == nil || p.Operations.Rehearsal.Reason != "on-apptown-disk" || !strings.Contains(p.RehearsalNote, "backup store") {
		t.Fatalf("protection without a store: %+v %+v %v", p, p.Operations, err)
	}
	if len(p.Items) != 2 || p.Items[1].Copy != CopyFresh {
		t.Fatalf("items without a store: %+v", p.Items)
	}
	if _, err := w.s.RequestRehearsal(ctx, w.pid, "main", ids.Nil); !errors.Is(err, ErrOnAppTownDisk) || err.Error() != p.Operations.Rehearsal.Why {
		t.Fatalf("the rehearsal door without a store: %v (the card says %q)", err, p.Operations.Rehearsal.Why)
	}
	// Each door refuses a Docker host's deployment with the reason the card
	// shows, the platform's own: a copy is its backups, and a check or a
	// restore test runs only on App Platform, gate or no gate (OHB-01).
	doors := map[string]struct {
		card Availability
		ask  func() error
	}{
		"copy":  {p.Operations.Copy, func() error { _, err := w.s.RequestCopy(ctx, w.pid, "main"); return err }},
		"check": {p.Operations.Check, func() error { _, err := w.s.RequestDrill(ctx, w.pid, "main"); return err }},
		"restore test": {p.Operations.Restore, func() error {
			_, err := w.s.RequestRestoredCopy(ctx, w.pid, "main", RestoredCopyRequest{Test: true})
			return err
		}},
	}
	for gate, image := range map[string]string{"without a gate": "", "with a gate": "gate@sha256:" + strings.Repeat("c", 64)} {
		w.s.restored.GateImage, w.s.restored.AppTown = image, "https://forge.example"
		for door, d := range doors {
			err := d.ask()
			if err == nil || d.card.Available || err.Error() != d.card.Why || fault.CodeOf(err) != d.card.Reason || strings.Contains(err.Error(), "gate") {
				t.Errorf("%s, %s: the door says %v (%s); the card says %+v", door, gate, err, fault.CodeOf(err), d.card)
			}
		}
		if p.Operations.Check.Reason != "not-on-this-platform" || p.Operations.Restore.Reason != "not-on-this-platform" || p.Operations.Copy.Reason != "backups-page" ||
			!strings.Contains(p.Operations.Restore.Why, "rehearsal") || !strings.Contains(p.Operations.Check.Why, "rehearsal") {
			t.Fatalf("the card's reasons: %+v", p.Operations)
		}
	}
	w.s.restored.GateImage = ""

	w.attach()
	w.client.ResetCommands()
	b := w.take("manual")
	if b.Store != StoreBackupStore || b.StoreBucket != "forge-copies" || !strings.HasPrefix(b.Path, "oh/main/") || b.Status != BackupOK {
		t.Fatalf("with a store: %+v", b)
	}
	pg := b.GoodCopy("need", "postgres")
	if pg == nil || pg.Format != "pg-custom" || pg.AsUser != "app" || pg.Client == "" || pg.Restore == "" {
		t.Fatalf("the database's copy: %+v", pg)
	}
	exported := false
	for _, c := range commandsOf(w.client) {
		exported = exported || strings.HasSuffix(c, "apptown-svc-env-oh-main-postgres sh -c pg_dump -Fc --no-owner")
	}
	if !exported {
		t.Fatalf("the copy was not made by the kind's export:\n%s", strings.Join(commandsOf(w.client), "\n"))
	}
	for _, it := range b.Items {
		o, ok := w.store.Get("forge-copies", b.Path+"/"+it.File)
		if !ok || sum(o.Body) != it.SHA256 || int64(len(o.Body)) != it.Bytes || o.Public {
			t.Fatalf("%s in the store: %v %+v", it.File, ok, it)
		}
	}
	manifest, ok := w.store.Get("forge-copies", b.Path+"/manifest.json")
	if !ok {
		t.Fatal("the set's manifest is not beside its copies")
	}
	// The manifest names AppTown that took the set.
	var named Backup
	if err := json.Unmarshal(manifest.Body, &named); err != nil || named.ID != b.ID || named.AppTown.IsZero() {
		t.Fatalf("the set's manifest: %s %v", manifest.Body, err)
	}
	if left := w.stagingEntries(); len(left) != 0 {
		t.Fatalf("staging left on AppTown's disk: %v", left)
	}
	if got, _ := os.ReadDir(filepath.Join(w.dir, "backups", "oh", "main")); len(got) != 1 {
		t.Fatalf("AppTown's disk holds %d sets; only the one taken before the store", len(got))
	}
	if p, err = w.s.DeploymentProtection(ctx, w.pid, "main"); err != nil || !p.OffHost || p.Note != "" || p.Items[1].LastCopy.Backup != b.ID || p.Items[1].CopyNote != "" {
		t.Fatalf("protection with a store: %+v %v", p, err)
	}

	// A copy the store does not hold as it was made is not a copy.
	w.store.Mangle = func(key string, body []byte) []byte {
		if strings.HasSuffix(key, "-postgres.dump") {
			return append(body, '!')
		}
		return body
	}
	bad := w.take("manual")
	if bad.Status != BackupPartial || bad.GoodCopy("need", "postgres") != nil || !strings.Contains(bad.Items[1].Error, "store holds") {
		t.Fatalf("a copy the store changed: %+v", bad)
	}
}

// An in-place restore of a set in the store reads every copy back, held
// to its digest, before anything changes; a copy the store no longer holds
// as recorded refuses the restore with no safety set taken and nothing
// stopped.
//
// Kills: refusing store sets in place; restoring before reading back;
// taking the safety set (or stopping the app) before the copies are in
// hand; restoring a copy whose digest does not match.
func TestADockerSetInTheStoreRestoresInPlace(t *testing.T) {
	t.Parallel()
	w := newOffhostWorld(t, "ohr", 40101)
	w.attach()
	b := w.take("manual")
	res, err := w.s.restoreBackup(context.Background(), nil, w.pid, "main", b.ID, "alice")
	if err != nil || res.Status != RestoreNotVerified || len(res.Items) != 2 {
		t.Fatalf("restore: %+v %v", res, err)
	}
	if got := strings.Join(w.restored, "\n"); !strings.Contains(got, "back-apptown-need-env-ohr-main-postgres.dump: copy of apptown-need-env-ohr-main-postgres.dump") || !strings.Contains(got, "back-apptown-data-apptown-env-ohr-main.tar.gz: copy of apptown-data-apptown-env-ohr-main.tar.gz") {
		t.Fatalf("what was restored:\n%s", got)
	}
	before, stops := len(w.sets()), w.stops
	o, _ := w.store.Get("forge-copies", b.Path+"/apptown-need-env-ohr-main-postgres.dump")
	o.Body = []byte("changed")
	w.store.Put("forge-copies", b.Path+"/apptown-need-env-ohr-main-postgres.dump", o)
	if _, err := w.s.restoreBackup(context.Background(), nil, w.pid, "main", b.ID, "alice"); err == nil || !strings.Contains(err.Error(), "nothing was restored") {
		t.Fatalf("a tampered copy: %v", err)
	}
	if len(w.sets()) != before || w.stops != stops {
		t.Fatalf("a refused restore took a safety set or stopped the app: %d sets (was %d), %d stops (was %d)", len(w.sets()), before, w.stops, stops)
	}
}

// A production deployment keeps its newest 14 sets wherever they are:
// sets on AppTown's disk and in the store are counted once, together,
// and each pruned set is removed where it is.
//
// Kills: counting each place apart (disk sets kept forever once new sets
// go to the store); pruning a store set before an older disk set.
func TestRetentionCountsSetsAcrossBothPlaces(t *testing.T) {
	t.Parallel()
	w := newOffhostWorld(t, "ohk", 40102)
	var disk []*Backup
	for range 9 {
		disk = append(disk, w.take("scheduled"))
	}
	w.attach()
	held := func() (all, inStore int) {
		for _, b := range w.sets() {
			if b.Status.Holds() {
				all++
				if b.Store == StoreBackupStore {
					inStore++
				}
			}
		}
		return all, inStore
	}
	for range 7 {
		w.take("scheduled")
	}
	// 9 + 7: the two oldest go, both on the disk.
	if all, inStore := held(); all != 14 || inStore != 7 {
		t.Fatalf("held %d (%d in the store); want the newest 14, all 7 store sets among them", all, inStore)
	}
	for i, b := range disk {
		_, err := os.Stat(b.Path)
		if gone := os.IsNotExist(err); gone != (i < 2) {
			t.Fatalf("disk set %d: gone %v", i, gone)
		}
	}
	// 9 more: every disk set has gone, and the two oldest store sets.
	var keys int
	for range 9 {
		w.take("scheduled")
	}
	if all, inStore := held(); all != 14 || inStore != 14 {
		t.Fatalf("held %d (%d in the store); want the newest 14, all in the store", all, inStore)
	}
	for _, k := range w.store.Keys("forge-copies") {
		if strings.HasSuffix(k, "/manifest.json") {
			keys++
		}
	}
	if keys != 14 {
		t.Fatalf("the store holds %d sets; pruned sets are removed from it", keys)
	}
}

// The daily sweep moves sets still on AppTown's disk into the attached
// store: each file held to its recorded digest, the row changed only after
// every file and the manifest are in, the folder removed after, a final
// set keeping its expiry. A set whose file does not match stays, saying
// why; one whose upload failed is moved the next day; nothing moves while
// a restore holds the files.
//
// Kills: moving under a restore; changing the row before the files are
// in; moving a set that does not match its record; losing a final set's
// expiry; not retrying a failed move.
func TestDiskSetsMoveToTheStore(t *testing.T) {
	t.Parallel()
	w := newOffhostWorld(t, "ohm", 40103)
	ctx := context.Background()
	first, mismatched := w.take("scheduled"), w.take("scheduled")
	dk := docker.Scope{Client: w.client}
	plan, err := w.s.heldPlan(ctx, w.pid, "ohm", "main")
	if err != nil {
		t.Fatal(err)
	}
	final, err := w.s.takeSet(ctx, dk, w.pid, "ohm", "main", plan, TriggerTakeDown, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mismatched.Path, "apptown-need-env-ohm-main-postgres.dump"), []byte("changed on the disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	w.attach()
	// A store attached is not a copy moved: the deployment is off-host
	// only once its newest backup is in the store.
	if p, err := w.s.DeploymentProtection(ctx, w.pid, "main"); err != nil || p.OffHost || !strings.Contains(p.Note, "still on this host") {
		t.Fatalf("before the move: %+v %v", p, err)
	}

	files, err := LockBackupFiles(filepath.Join(w.dir, "backups"), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.s.MoveSetsToStore(ctx); err == nil {
		t.Fatal("sets moved while a restore held them")
	}
	files.Close()

	w.store.Fail("PUT", "forge-copies", first.Path[len(filepath.Join(w.dir, "backups"))+1:]+"/manifest.json", 500)
	moved, left, err := w.s.MoveSetsToStore(ctx)
	if err != nil || moved != 1 || len(left) != 2 {
		t.Fatalf("moved %d, left %v, %v", moved, left, err)
	}
	got, _ := w.s.backups.ByID(ctx, w.pid, "main", final.ID)
	if got.Store != StoreBackupStore || got.Path != "ohm/main/"+filepath.Base(final.Path) || got.Trigger != TriggerTakeDown || got.TakenAt != final.TakenAt {
		t.Fatalf("the final set moved: %+v (was %+v)", got, final)
	}
	if _, err := os.Stat(final.Path); !os.IsNotExist(err) {
		t.Fatalf("a moved set's folder is still on the disk: %v", err)
	}
	for _, id := range []ids.ID{first.ID, mismatched.ID} {
		b, _ := w.s.backups.ByID(ctx, w.pid, "main", id)
		if b.Store != StoreAppTownDisk || !strings.Contains(b.Error, "not moved to the backup store") {
			t.Fatalf("set %d should stay on the disk saying why: %+v", id, b)
		}
		if _, err := os.Stat(b.Path); err != nil {
			t.Fatalf("a set that stayed lost its folder: %v", err)
		}
	}
	w.store.ClearFailures()
	if moved, left, err := w.s.MoveSetsToStore(ctx); err != nil || moved != 1 || len(left) != 1 || !strings.Contains(left[0], "does not match its record") {
		t.Fatalf("the next day: moved %d, left %v, %v", moved, left, err)
	}
	if b, _ := w.s.backups.ByID(ctx, w.pid, "main", first.ID); b.Store != StoreBackupStore {
		t.Fatalf("a failed move is retried: %+v", b)
	}
	// A moved set restores from the store.
	if res, err := w.s.restoreBackup(ctx, nil, w.pid, "main", first.ID, "alice"); err != nil || res.Status != RestoreNotVerified {
		t.Fatalf("restoring a moved set: %+v %v", res, err)
	}
}

// The sweep removes what a stopped take left staged more than a day ago,
// and nothing newer. A folder's age is its modification time, the system
// clock's: with AppTown's clock two days ahead, the new one stays.
//
// Kills: staging judged by AppTown's clock (the new folder reads two days
// old and is removed).
func TestStagingAStoppedTakeLeftIsCleared(t *testing.T) {
	t.Parallel()
	w := newOffhostWorld(t, "ohs", 40104)
	w.s.clock = func() time.Time { return time.Now().Add(48 * time.Hour) }
	staging := filepath.Join(w.dir, "backups", ".staging")
	for _, name := range []string{"take-old", "take-new", "42"} {
		if err := os.MkdirAll(filepath.Join(staging, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-25 * time.Hour)
	for _, name := range []string{"take-old", "42"} {
		if err := os.Chtimes(filepath.Join(staging, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := w.s.ClearStaging(); err != nil || n != 1 {
		t.Fatalf("cleared %d: %v", n, err)
	}
	if got := strings.Join(w.stagingEntries(), ","); got != "42,take-new" {
		t.Fatalf("left staged: %s", got)
	}
}

// What a Docker host's deployment's card says comes from what is recorded:
// with a store attached and no retained backup (none yet, or a first one
// that failed) it is kept nowhere, never "on this host"; a backup on the
// disk is on this host, one in the store in the store. A release contract
// that cannot be read is unknown, never a deployment holding no data (OHB2-01,
// OHB2-02).
//
// Kills: inferring "on this host" from not being off-host; hiding an
// unreadable contract as a stateless deployment.
func TestADockerDeploymentSaysWhereItsBackupsAreFromTheRecord(t *testing.T) {
	t.Parallel()
	w := newOffhostWorld(t, "ohw", 40105)
	ctx := context.Background()
	read := func() *Protection {
		t.Helper()
		p, err := w.s.DeploymentProtection(ctx, w.pid, "main")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if p := read(); p.KeptOn != "" || p.Unknown != "" {
		t.Fatalf("before any backup: %+v", p)
	}
	w.attach()
	w.store.Fail("PUT", "forge-copies", "*", 500)
	if b := w.take("scheduled"); b.Status != BackupFailed {
		t.Fatalf("the first backup with the store failing: %+v", b)
	}
	if p := read(); p.KeptOn != "" || p.OffHost {
		t.Fatalf("after a failed first backup: kept on %q, off-host %v", p.KeptOn, p.OffHost)
	}
	w.store.ClearFailures()
	w.take("scheduled")
	if p := read(); p.KeptOn != "store" || !p.OffHost {
		t.Fatalf("after a backup in the store: %+v", p)
	}

	h := newOffhostWorld(t, "ohd", 40106)
	h.take("scheduled")
	if p, err := h.s.DeploymentProtection(ctx, h.pid, "main"); err != nil || p.KeptOn != "host" {
		t.Fatalf("a backup on the disk: %+v %v", p, err)
	}

	releaseMain(t, h.s, "ohd", "app: [not a contract")
	if p, err := h.s.DeploymentProtection(ctx, h.pid, "main"); err != nil || p.Unknown == "" || len(p.Coverage.Assessed) != 0 ||
		!slices.Equal(p.Coverage.NotAssessed, []string{"the deployment's own data: what it holds is unknown", OutsideNotAssessed}) {
		t.Fatalf("an unreadable contract: %+v %v", p, err)
	}
	releaseMain(t, h.s, "ohd", "app:\n  web:\n    image: registry.example/ohd:1\n    ports: [\"8080\"]\n    public: true\n")
	// Holding no data AppTown knows of still says what is not assessed.
	if p, err := h.s.DeploymentProtection(ctx, h.pid, "main"); err != nil || p.Unknown != "" || len(p.Items) != 0 ||
		len(p.Coverage.Assessed) != 0 || !slices.Equal(p.Coverage.NotAssessed, []string{OutsideNotAssessed}) {
		t.Fatalf("a stateless contract: %+v %v", p, err)
	}
}
