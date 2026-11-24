package dataprotection

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
)

// The Backups page lists each deployment that holds data with the same
// protection answer its own page reads, so the list and the deployment
// never disagree; one never deployed, or known to hold nothing, is not
// listed.
//
// Kills: a second classification for the list; listing a deployment
// nothing was deployed to; listing one that holds no data.
func TestTheBackupsListCarriesEachDeploymentsOwnProtectionAnswer(t *testing.T) {
	t.Parallel()
	w := newOffhostWorld(t, "fl", 40140)
	ctx := context.Background()
	b := w.take("scheduled")
	collectProtectionForTest(t, w.s, w.pid, "main")
	target, err := w.s.Targets.ByName(ctx, "local")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Deployments.EnsureStanding(ctx, w.pid, "never", target.ID, deployment.Attributes{DataClass: "production"}.WithDefaults()); err != nil {
		t.Fatal(err)
	}
	fleet, err := w.s.FleetProtection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(fleet.Deployments) != 1 {
		t.Fatalf("listed: %+v", fleet.Deployments)
	}
	row := fleet.Deployments[0]
	own, err := w.s.DeploymentProtection(ctx, w.pid, "main")
	if err != nil {
		t.Fatal(err)
	}
	listed, _ := json.Marshal(row.Protection)
	alone, _ := json.Marshal(own)
	if string(listed) != string(alone) {
		t.Fatalf("the list's answer differs from the deployment's own:\n%s\n%s", listed, alone)
	}
	if row.Project != "fl" || row.ProjectName != "FL" || row.Deployment != "main" || row.TargetKind != deployment.KindDockerHost ||
		row.Newest == nil || row.Newest.Backup != b.ID || row.Newest.Bytes != b.Bytes {
		t.Fatalf("row: %+v %+v", row, row.Newest)
	}
	if fleet.LastNight.Of != 1 || fleet.LastNight.Copied != 1 || !fleet.LastNight.At.Equal(mustTime(b.TakenAt)) {
		t.Fatalf("last night: %+v", fleet.LastNight)
	}

	releaseMain(t, w.s, "fl", "app:\n  web:\n    image: registry.example/fl:1\n    ports: [\"8080\"]\n    public: true\n")
	collectProtectionForTest(t, w.s, w.pid, "main")
	if fleet, err := w.s.FleetProtection(ctx); err != nil || len(fleet.Deployments) != 0 || fleet.LastNight.Of != 0 {
		t.Fatalf("a deployment that holds nothing: %+v %v", fleet, err)
	}
}

// Last night counts what the records show over the listed deployments:
// only a complete nightly set from the last day is copied. A partial set,
// a copy that failed before it recorded a set, and an older one are not,
// so the count can understate and never overstates.
//
// Kills: counting a partial set as copied; counting a deployment with no
// set last night out of the total; reading an older nightly set.
func TestLastNightCountsOnlyCompleteNightlySetsFromTheLastDay(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	set := func(trigger string, status BackupState, hours float64) Backup {
		return Backup{Trigger: trigger, Status: status, TakenAt: now.Add(-time.Duration(hours * float64(time.Hour))).Format(time.RFC3339Nano)}
	}
	var n LastNight
	for _, sets := range [][]Backup{
		{set("scheduled", BackupOK, 6)},
		{set("manual", BackupOK, 1), set("scheduled", BackupOK, 5.9)},
		{set("pre-release", BackupOK, 2), set("scheduled", BackupOK, 5.8)},
		{set("scheduled", BackupPartial, 6), set("scheduled", BackupOK, 30)},
		// Its copy last night failed before it recorded a set.
		{set("scheduled", BackupOK, 30)},
	} {
		n.add(sets, now)
	}
	if n.Of != 5 || n.Copied != 3 || !n.At.Equal(now.Add(-6*time.Hour)) {
		t.Fatalf("last night: %+v", n)
	}
}

// Without a store, and with one attached while the newest set is still on
// AppTown's disk, the deployment says AppTown's disk: the sets are under
// AppTown's own data directory, whichever host the app runs on.
//
// Kills: "this host", which names the app's host over SSH.
func TestADockerDeploymentsCopiesOnTheDiskAreOnAppTownsDisk(t *testing.T) {
	t.Parallel()
	w := newOffhostWorld(t, "fd", 40141)
	ctx := context.Background()
	w.take("manual")
	collectProtectionForTest(t, w.s, w.pid, "main")
	if p, err := w.s.DeploymentProtection(ctx, w.pid, "main"); err != nil || p.Note != "Backups are kept on AppTown's disk only: they do not outlive it." {
		t.Fatalf("without a store: %q %v", p.Note, err)
	}
	w.attach()
	collectProtectionForTest(t, w.s, w.pid, "main")
	if p, err := w.s.DeploymentProtection(ctx, w.pid, "main"); err != nil || p.Note != "The newest backup is still on AppTown's disk; it moves to the backup store with the nightly sweep." {
		t.Fatalf("store attached, newest set on the disk: %q %v", p.Note, err)
	}
	// What the store holds counts only the sets kept there.
	if kept, err := w.s.KeptInStore(ctx); err != nil || kept != 0 {
		t.Fatalf("kept in the store with the only set on the disk: %d %v", kept, err)
	}
	b := w.take("manual")
	if kept, err := w.s.KeptInStore(ctx); err != nil || b.Store != StoreBackupStore || kept != b.Bytes || kept == 0 {
		t.Fatalf("kept in the store: %d of %+v %v", kept, b, err)
	}
}

// An overdue copy's age reads as a person says it, not as a duration.
//
// Kills: "the newest copy is 249h0m0s old".
func TestAnOverdueCopySaysItsAgeInHoursOrDays(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	for taken, want := range map[time.Duration]string{31 * time.Hour: "the newest copy is 31 hours old", 249 * time.Hour: "the newest copy is 10 days old"} {
		_, note := copyState(&CopyRef{TakenAt: now.Add(-taken).Format(time.RFC3339Nano)}, nil, now)
		if note != want {
			t.Errorf("%s: %q, want %q", taken, note, want)
		}
	}
}
