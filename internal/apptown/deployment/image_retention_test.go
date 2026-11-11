package deployment

import (
	"context"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/deploy"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/stamp"
)

func TestImageKeepSetsProtectRollbackBackupsAndRequests(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 11, 3, 30, 0, 0, time.UTC)
	dep := idstest.N(1)
	// Release 4 is outside every window: only its kept backup protects it.
	snapshot := imageRetentionSnapshot{running: map[ids.ID]bool{idstest.N(10): true}, backups: map[ids.ID]bool{idstest.N(4): true}}
	// Releases are newest first, as the owner query returns them.
	for n := int64(15); n >= 1; n-- {
		r := &Release{ID: idstest.N(uint64(n)), BundleID: idstest.N(uint64(n)), DeploymentID: dep, State: Released, ReleasedAt: stamp.Of(now.AddDate(0, 0, -20))}
		if n == 15 {
			r.State = Releasing
		}
		if n == 14 {
			r.State = releaseWaiting
		}
		if n == 9 {
			r.ReleasedAt = stamp.Of(now.AddDate(0, 0, -2))
		}
		snapshot.releases = append(snapshot.releases, r)
		snapshot.bundles = append(snapshot.bundles, &ReleaseBundle{ID: r.BundleID, ProjectID: idstest.N(100), CreatedAt: now.AddDate(0, 0, -120).Format(time.RFC3339Nano)})
	}
	spoke, hub := retentionKeep(now, snapshot, integration.HubPolicy{KeepDays: 90, KeepReleases: 2})
	for _, n := range []int64{15, 14, 13, 12, 11, 10, 9, 4} {
		if !spoke[dep][idstest.N(uint64(n))] || !hub[idstest.N(uint64(n))] {
			t.Errorf("required release %d was not kept", n)
		}
	}
	for _, n := range []int64{8, 7, 6, 5, 3, 2, 1} {
		if spoke[dep][idstest.N(uint64(n))] || hub[idstest.N(uint64(n))] {
			t.Errorf("expired release %d kept", n)
		}
	}
	snapshot.bundles[8].CreatedAt = now.AddDate(0, 0, -80).Format(time.RFC3339Nano)
	_, hub = retentionKeep(now, snapshot, integration.HubPolicy{KeepDays: 90, KeepReleases: 10})
	if !hub[idstest.N(7)] || !hub[idstest.N(6)] || hub[idstest.N(5)] {
		t.Fatalf("hub age/count union: %v", hub)
	}
}

func TestSharedKeptPartOrInitImageIsNeverExpired(t *testing.T) {
	t.Parallel()
	kept := &ReleaseBundle{Parts: map[string]string{"web": "shared"}, Init: map[string]string{"seed": "init"}}
	old := &ReleaseBundle{Parts: map[string]string{"worker": "shared", "old": "expired"}, Init: map[string]string{"seed": "init"}}
	recorded, protect := map[string]bool{}, map[string]bool{}
	for _, ref := range bundleImages(old) {
		recorded[ref] = true
	}
	for _, ref := range bundleImages(kept) {
		protect[ref] = true
	}
	if got := imageDifference(recorded, protect); !slices.Equal(got, []string{"expired"}) {
		t.Fatalf("shared image expired: %v", got)
	}
}

type unreadableBackupImages struct{}

func (unreadableBackupImages) KeptBackupReleases(context.Context) ([]ids.ID, error) {
	return nil, errors.New("backup read failed")
}

func TestAnUnreadableBackupKeepSetDeletesNothingAndRecordsUnknown(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	s := New(t.Context(), db, Dependencies{BackupImages: unreadableBackupImages{}})
	result, err := s.retainImages(t.Context())
	if err != nil || !strings.Contains(result.Unknown, "backup read failed") || len(result.Stores) != 0 {
		t.Fatalf("unknown backup pass: %+v %v", result, err)
	}
	receipts, err := audit.Recent(t.Context(), db, audit.Filter{KindPrefix: "images.retained", Limit: 10})
	if err != nil || len(receipts) != 1 || !strings.Contains(string(receipts[0].Payload), "backup read failed") {
		t.Fatalf("pass receipt: %+v %v", receipts, err)
	}
}

type keptImageBackups struct{}

func (keptImageBackups) KeptBackupReleases(context.Context) ([]ids.ID, error) { return nil, nil }

type retainedStore struct {
	deploy.Driver
	calls, collections int
	expired            []string
}

func (f *retainedStore) ImageStore(context.Context, deploy.Placement) (string, error) {
	return "shared-registry", nil
}
func (f *retainedStore) TrimImages(_ context.Context, _ deploy.Placement, expired, keep []string) ([]string, []deploy.Left, error) {
	f.calls++
	f.expired = expired
	return expired, nil, nil
}
func (f *retainedStore) CollectionStore(context.Context) (string, error) {
	return "registry-account", nil
}
func (f *retainedStore) CollectImages(context.Context) error { f.collections++; return nil }

func TestRetentionUnionsSharedStoresAndCollectsOnlyWhenReleasesAreIdle(t *testing.T) {
	for _, scenario := range []string{"idle", "busy", "shared", "unreadable backups"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			db := testDB(t)
			pid := createTestProject(t, db, "shop", "Shop")
			fake := &retainedStore{}
			factory := func(context.Context, *Target, *Deployment) (deploy.Driver, error) { return fake, nil }
			var backups backupImageKeeper = keptImageBackups{}
			if scenario == "unreadable backups" {
				backups = unreadableBackupImages{}
			}
			s := New(t.Context(), db, assembled(db, testRuntime{Drivers: map[string]DriverFactory{KindDOApp: factory}}, Dependencies{BackupImages: backups}))
			target, err := s.Targets.Put(t.Context(), &Target{Name: "cloud", Kind: KindDOApp, Origin: OriginAttached, Status: TargetReady})
			if err != nil {
				t.Fatal(err)
			}
			dep, err := s.Deployments.EnsureStanding(t.Context(), pid, "first", target.ID, Attributes{}.WithDefaults())
			if err != nil {
				t.Fatal(err)
			}
			var oldest, newest ids.ID
			for n := 0; n < 4; n++ {
				bundle, err := s.Bundles.Cut(t.Context(), pid, "commit", map[string]string{"web": fmt.Sprintf("image-%d", n)}, map[string]string{"init": "shared-init"}, []byte("contract"), BundleOrigin{})
				if err != nil {
					t.Fatal(err)
				}
				if n == 0 {
					oldest = bundle
				}
				newest = bundle
				release, err := s.Releases.Start(t.Context(), ReleaseDraft{Source: ReleaseFromBundle, ProjectID: pid, DeploymentID: dep.ID, Deployment: dep.Name, BundleID: bundle}, "fixture")
				if err != nil {
					t.Fatal(err)
				}
				// sql-guarantee: retention reads settled historical releases without a
				// live provider. No release operation can import an arbitrary old history.
				// sql-guarantee: import settled release history without a provider or deployment attempt.
				if _, err := db.SQL().ExecContext(t.Context(), `UPDATE releases SET state='released',released_at=now()-interval '30 days' WHERE id=$1`, release.ID); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "shared" {
				other, err := s.Deployments.EnsureStanding(t.Context(), pid, "other", target.ID, Attributes{}.WithDefaults())
				if err != nil {
					t.Fatal(err)
				}
				release, err := s.Releases.Start(t.Context(), ReleaseDraft{Source: ReleaseFromBundle, ProjectID: pid, DeploymentID: other.ID, Deployment: other.Name, BundleID: oldest}, "fixture")
				if err != nil {
					t.Fatal(err)
				}
				// sql-guarantee: import settled release history without a provider or deployment attempt.
				if _, err := db.SQL().ExecContext(t.Context(), `UPDATE releases SET state='released',released_at=now()-interval '30 days' WHERE id=$1`, release.ID); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "busy" {
				release, err := s.Releases.Start(t.Context(), ReleaseDraft{Source: ReleaseFromBundle, ProjectID: pid, DeploymentID: dep.ID, Deployment: dep.Name, BundleID: newest}, "fixture")
				if err != nil {
					t.Fatal(err)
				}
				// sql-guarantee: the account-level collector observes admitted in-flight
				// requests. Authority and preparation are outside this inventory fixture.
				// sql-guarantee: an admitted in-flight request blocks registry collection.
				if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO release_requests(release_id,target_id,base_generation,attributes_json) VALUES($1,$2,0,'{}')`, release.ID, target.ID); err != nil {
					t.Fatal(err)
				}
			}
			result, err := s.retainImages(t.Context())
			if scenario == "unreadable backups" {
				// A store with expired images is not visited when any keep-set input is unknown.
				if err != nil || !strings.Contains(result.Unknown, "backup read failed") || fake.calls != 0 || fake.collections != 0 {
					t.Fatalf("unknown keep set reached a store: %+v %v calls=%d", result, err, fake.calls)
				}
				return
			}
			if err != nil || result.Unknown != "" {
				t.Fatalf("pass: %+v %v", result, err)
			}
			if fake.calls != 1 {
				t.Fatalf("store visited %d times", fake.calls)
			}
			if scenario == "idle" {
				if !slices.Equal(fake.expired, []string{"image-0"}) || fake.collections != 1 {
					t.Fatalf("idle expiration/GC: %v %d", fake.expired, fake.collections)
				}
			} else if fake.collections != 0 || (scenario == "shared" && slices.Contains(fake.expired, "image-0")) {
				t.Fatalf("shared/in-flight keep lost: %v collections=%d", fake.expired, fake.collections)
			}
		})
	}
}
