package deployment

import (
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
)

func TestStorageBindingPreservesEvidenceAndRefusesConflicts(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()
	pid := createTestProject(t, db, "storage-binding", "Storage binding")
	s := New(ctx, db, Dependencies{})
	target, err := s.Targets.Put(ctx, &Target{Name: "local", Kind: KindDockerHost, Origin: OriginBuiltIn, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := s.Deployments.EnsureStanding(ctx, pid, "main", target.ID, Attributes{DataClass: "production"}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	owner := s.Deployments.StorageOf(dep)
	b := deploy.StorageBinding{DataPath: "/data", Need: "postgres", Volume: "recorded-volume", DeclaredImage: "postgres:17", ImageID: "sha256:" + strings.Repeat("a", 64), VolumeCreated: time.Date(2026, 10, 25, 1, 2, 3, 123456789, time.UTC), VolumeDriver: "local", Origin: "observed"}
	if _, found, err := owner.Get(ctx, b.Need, b.Volume); err != nil || found {
		t.Fatalf("absent: found=%t err=%v", found, err)
	}
	dbtest.FailNextStatement(t, db, "INSERT INTO audit_log")
	if err := owner.Establish(ctx, b); err == nil {
		t.Fatal("audit failure established storage evidence")
	}
	if _, found, err := owner.Get(ctx, b.Need, b.Volume); err != nil || found {
		t.Fatalf("audit rollback: found=%t err=%v", found, err)
	}
	if err := owner.Establish(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := owner.Establish(ctx, b); err != nil {
		t.Fatalf("identical establishment: %v", err)
	}
	got, found, err := owner.Get(ctx, b.Need, b.Volume)
	if err != nil || !found || !sameStorage(got, b) {
		t.Fatalf("round trip: %+v found=%t err=%v", got, found, err)
	}
	changed := b
	changed.VolumeCreated = changed.VolumeCreated.Add(time.Nanosecond)
	if err := owner.Establish(ctx, changed); err == nil {
		t.Fatal("same-name replacement overwrote evidence")
	}
	changed = b
	changed.ImageID = "sha256:" + strings.Repeat("b", 64)
	if err := owner.Establish(ctx, changed); err == nil {
		t.Fatal("different engine overwrote evidence")
	}
	if err := owner.Retire(ctx, b.Need, b.Volume); err != nil {
		t.Fatal(err)
	}
	if _, found, err := owner.Get(ctx, b.Need, b.Volume); err != nil || found {
		t.Fatalf("retired: found=%t err=%v", found, err)
	}
	if err := owner.Establish(ctx, changed); err != nil {
		t.Fatalf("new binding after retirement: %v", err)
	}
}

func TestStorageConcurrentEstablishmentAndRestoreTransition(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()
	pid := createTestProject(t, db, "storage-race", "Storage race")
	s := New(ctx, db, Dependencies{})
	target, err := s.Targets.Put(ctx, &Target{Name: "local", Kind: KindDockerHost, Origin: OriginBuiltIn, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := s.Deployments.EnsureStanding(ctx, pid, "main", target.ID, Attributes{DataClass: "production"}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	r := storageRecorder{db: db, deployment: dep.ID}
	b := deploy.StorageBinding{DataPath: "/data", Need: "postgres", Volume: "database", DeclaredImage: "postgres:17", ImageID: "sha256:" + strings.Repeat("a", 64), VolumeCreated: time.Date(2026, 10, 25, 0, 0, 0, 1, time.UTC), VolumeDriver: "local", Origin: "observed"}
	var wg sync.WaitGroup
	outcomes := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			candidate := b
			candidate.VolumeCreated = candidate.VolumeCreated.Add(time.Duration(i))
			outcomes <- r.Establish(ctx, candidate)
		}(i)
	}
	wg.Wait()
	close(outcomes)
	successes := 0
	for err := range outcomes {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("conflicting establishments succeeded %d times", successes)
	}
	source, found, err := r.Get(ctx, b.Need, b.Volume)
	if err != nil || !found {
		t.Fatalf("source: found=%t err=%v", found, err)
	}
	restored := source
	restored.VolumeCreated = source.VolumeCreated.Add(time.Hour)
	restore := storageRestoreRecorder{storageRecorder: r, needs: map[string]deploy.StorageCopy{"postgres": {}}}
	if err := restore.Restore(ctx, &source, restored); err == nil {
		t.Fatal("ordinary deployment was allowed to transition for restore")
	}
	if err := s.Deployments.HoldForFill(ctx, []ids.ID{dep.ID}); err != nil {
		t.Fatal(err)
	}
	wrong := source
	wrong.ImageID = "sha256:" + strings.Repeat("b", 64)
	if err := restore.Restore(ctx, &wrong, restored); err == nil {
		t.Fatal("stale source evidence transitioned")
	}
	dbtest.FailNextStatement(t, db, "INSERT INTO audit_log")
	if err := restore.Restore(ctx, &source, restored); err == nil {
		t.Fatal("audit failure committed restore transition")
	}
	if got, found, err := r.Get(ctx, b.Need, b.Volume); err != nil || !found || !sameStorage(got, source) {
		t.Fatalf("restore rollback lost source: %+v found=%t err=%v", got, found, err)
	}
	if err := restore.Restore(ctx, &source, restored); err != nil {
		t.Fatal(err)
	}
	got, _, err := r.Get(ctx, b.Need, b.Volume)
	if err != nil || !sameStorage(got, restored) || got.Origin != "restored" {
		t.Fatalf("target evidence: %+v err=%v", got, err)
	}
	// Removal must transfer unowned observed evidence too, in its transaction.
	left, err := database.Transact(ctx, db, func(tx *database.Tx) ([]deploy.Left, error) { return retainStorageIn(ctx, tx, dep, nil) })
	if err != nil || len(left) != 1 || left[0].Handle["storageBinding"] == nil {
		t.Fatalf("retained evidence: %+v err=%v", left, err)
	}
	retained, ok := left[0].Handle["storageBinding"].(deploy.StorageBinding)
	if !ok || retained.DataPath != restored.DataPath {
		t.Fatalf("retained mount destination lost: %+v", left)
	}
}

func TestPhysicalRestoreRequiresCopyTimeSourceEngine(t *testing.T) {
	image := "sha256:" + strings.Repeat("a", 64)
	source := deploy.StorageBinding{DataPath: "/data", Need: "redis", Volume: "redis-data", DeclaredImage: "redis:7-alpine", ImageID: image}
	copy := deploy.StorageCopy{Volume: source.Volume, Physical: true, EngineImageID: image}
	for _, tc := range []struct {
		name, change string
		wantError    bool
	}{
		{name: "same physical engine"},
		{name: "legacy archive", change: "legacy", wantError: true},
		{name: "no source", change: "source", wantError: true},
		{name: "different writer", change: "writer", wantError: true},
		{name: "different volume", change: "volume", wantError: true},
		{name: "different need", change: "need", wantError: true},
		{name: "different declaration", change: "declaration", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := copy
			old := source
			src := &old
			declared := source.DeclaredImage
			switch tc.change {
			case "legacy":
				item.EngineImageID = ""
			case "source":
				src = nil
			case "writer":
				item.EngineImageID = "sha256:" + strings.Repeat("b", 64)
			case "volume":
				item.Volume = "other-data"
			case "need":
				old.Need = "other"
			case "declaration":
				declared = "redis:8-alpine"
			}
			r := storageRestoreRecorder{needs: map[string]deploy.StorageCopy{"redis": item}}
			got, exact, err := r.Image("redis", declared, src)
			if (err != nil) != tc.wantError {
				t.Fatalf("engine=%q err=%v", got, err)
			}
			if !tc.wantError && (got != image || !exact) {
				t.Fatalf("physical restore selected %q", got)
			}
			if tc.change == "legacy" && !strings.Contains(err.Error(), "preserve the archive") {
				t.Fatalf("legacy diagnosis: %v", err)
			}
		})
	}
}
