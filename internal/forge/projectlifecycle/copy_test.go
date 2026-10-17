package projectlifecycle

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/clock/clocktest"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
)

// An eval's copy is its source as it was at one commit, with exactly the
// way of working the source is on now: its history up to the commit and
// nothing after, the same adoption receipt (packs, blocks, lanes, policies,
// extensions), no members, and its copy record written with it.
func TestACopyIsTheSourceAtOneCommitWithItsWayOfWorking(t *testing.T) {
	t.Parallel()
	f := newOwnerFixture(t, true)
	ctx := context.Background()
	src, err := f.lifecycle.Create(ctx, Draft{Slug: "shop", Name: "Shop", Actor: "bls"})
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	gittest.RunAsTester(t, work, "init", "-q", "-b", "main")
	for name, body := range map[string]string{".gitignore": "node_modules\n", "README.md": "the shop\n"} {
		if err := os.WriteFile(filepath.Join(work, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gittest.RunAsTester(t, work, "add", "-A")
	gittest.RunAsTester(t, work, "commit", "-qm", "base")
	base := gittest.RunAsTester(t, work, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(work, "LATER.md"), []byte("done since\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.RunAsTester(t, work, "add", "-A")
	gittest.RunAsTester(t, work, "commit", "-qm", "later")
	bundle := filepath.Join(t.TempDir(), "shop.bundle")
	gittest.RunAsTester(t, work, "bundle", "create", "-q", bundle, "HEAD", "refs/heads/main")
	// The pack first: the fixture publishes nothing, and a pack applied to
	// a repository commits its starter files.
	pk, err := f.ways.Pack(ctx, "go-small")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.lifecycle.ApplyPack(adoption.WithSystemAuthority(ctx, src.ID), src, pk, "bls"); err != nil {
		t.Fatal(err)
	}
	body, err := os.Open(bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	if err := f.lifecycle.SeedFromBundle(ctx, src, body, "main", "bls"); err != nil {
		t.Fatal(err)
	}
	wantMainRecorded(t, f.db, f.lifecycle.dataDir, src.ID, scm.ViaSeed)

	var recorded ids.ID
	copy, err := f.lifecycle.CreateCopy(ctx, CopyDraft{Slug: "eval-1-1", Name: "Eval 1 baseline", SourceID: src.ID, Commit: base, Who: "eval/1",
		Record: func(_ *database.Tx, projectID ids.ID) error { recorded = projectID; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if recorded != copy.ID {
		t.Fatalf("the copy record names %d, the copy is %d", recorded, copy.ID)
	}
	if got, err := project.PurposeIn(ctx, f.db, copy.ID); err != nil || got != project.EvalCopy {
		t.Fatalf("the copy's purpose is %q (%v), want an eval copy", got, err)
	}
	hub := filepath.Join(f.lifecycle.dataDir, "workspaces", "project-"+copy.Slug)
	if head := gittest.RunAsTester(t, hub, "rev-parse", "HEAD"); head != base {
		t.Fatalf("the copy's main is %s, want the base %s", head, base)
	}
	wantMainRecorded(t, f.db, f.lifecycle.dataDir, copy.ID, scm.ViaCopy)
	if _, err := os.Stat(filepath.Join(hub, "LATER.md")); !os.IsNotExist(err) {
		t.Fatalf("work done after the base is in the copy: %v", err)
	}
	want, err := f.adoption.CurrentReceipt(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.adoption.CurrentReceipt(ctx, copy.ID)
	if err != nil || adoption.Digest(got.Receipt) != adoption.Digest(want.Receipt) || len(got.Receipt.Guidance) == 0 {
		t.Fatalf("the copy's way of working differs:\n got %+v\nwant %+v\n%v", got, want, err)
	}
	grants, err := f.identity.GrantsAt(ctx, access.ScopeProject, copy.ID.String())
	if err != nil || len(grants) != 0 {
		t.Fatalf("the copy has members: %+v %v", grants, err)
	}
}

// wantMainRecorded: the forge's record of a project's main is its hub's main,
// recorded by the door named, read from the SCM owner.
func wantMainRecorded(t *testing.T, db *database.DB, dataDir string, projectID ids.ID, via scm.MainVia) {
	t.Helper()
	p, err := project.ByIDIn(context.Background(), db, projectID)
	if err != nil {
		t.Fatal(err)
	}
	m, known, err := scm.ReadMain(context.Background(), db, projectID)
	if err != nil || !known {
		t.Fatalf("%s: no recorded main (%v)", p.Slug, err)
	}
	if want := scm.Ref(t.Context(), hubDir(dataDir, p.Slug), "refs/heads/"+p.DefaultBranch); m.SHA != want || m.Via != string(via) {
		t.Fatalf("%s: recorded main %s by %s, want the hub's %s by %s", p.Slug, m.SHA, m.Via, want, via)
	}
}

// Every lifecycle door that writes main records it, each taking the
// repository lock once: setting up a repository, then a pack's starter files.
func TestRepositorySetupAndStarterFilesRecordMain(t *testing.T) {
	t.Parallel()
	f := newOwnerFixture(t, true)
	p, err := f.lifecycle.Create(context.Background(), Draft{Slug: "starter", Name: "Starter", Actor: "bls"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.lifecycle.InitializeRepository(t.Context(), p, "main", ""); err != nil {
		t.Fatal(err)
	}
	wantMainRecorded(t, f.db, f.lifecycle.dataDir, p.ID, scm.ViaInit)
	before := scm.Ref(t.Context(), hubDir(f.lifecycle.dataDir, p.Slug), "refs/heads/main")
	pk, err := f.ways.Pack(t.Context(), "go-small")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.lifecycle.ApplyPack(adoption.WithSystemAuthority(context.Background(), p.ID), p, pk, "bls"); err != nil {
		t.Fatal(err)
	}
	if scm.Ref(t.Context(), hubDir(f.lifecycle.dataDir, p.Slug), "refs/heads/main") == before {
		t.Fatal("the pack committed no starter files")
	}
	wantMainRecorded(t, f.db, f.lifecycle.dataDir, p.ID, scm.ViaGenesis)
}

// The offline backfill records each repository-backed project's current main
// as backfill, keeps a project already recorded, leaves one whose main cannot
// be read unknown, and changes nothing when run again.
func TestBackfillRecordsCurrentMainOnce(t *testing.T) {
	t.Parallel()
	f := newOwnerFixture(t, true)
	ctx := context.Background()
	dataDir := f.lifecycle.dataDir
	// A project set up before main was recorded: its hub and default branch,
	// through the owners, with no record.
	older, err := f.lifecycle.Create(ctx, Draft{Slug: "older", Name: "Older", Actor: "bls"})
	if err != nil {
		t.Fatal(err)
	}
	if err := scm.InitializeRepository(ctx, hubDir(dataDir, older.Slug), "main"); err != nil {
		t.Fatal(err)
	}
	if err := f.lifecycle.projects.SetDefaultBranch(ctx, older.ID, "main"); err != nil {
		t.Fatal(err)
	}
	recorded, err := f.lifecycle.Create(ctx, Draft{Slug: "recorded", Name: "Recorded", Actor: "bls"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.lifecycle.InitializeRepository(ctx, recorded, "main", ""); err != nil {
		t.Fatal(err)
	}
	lost, err := f.lifecycle.Create(ctx, Draft{Slug: "lost", Name: "Lost", Actor: "bls"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.lifecycle.projects.SetDefaultBranch(ctx, lost.ID, "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.lifecycle.Create(ctx, Draft{Slug: "no-repo", Name: "No repo", Actor: "bls"}); err != nil {
		t.Fatal(err)
	}
	report, err := BackfillRecordedMain(ctx, f.db, dataDir, nil)
	if err != nil || report.Recorded != 1 || report.Kept != 1 || report.Unknown != 1 || len(report.Unreadable) != 1 || report.Unreadable[0] != "lost" {
		t.Fatalf("backfill: %+v %v", report, err)
	}
	wantMainRecorded(t, f.db, dataDir, older.ID, scm.ViaBackfill)
	wantMainRecorded(t, f.db, dataDir, recorded.ID, scm.ViaInit)
	if _, known, err := scm.ReadMain(ctx, f.db, lost.ID); err != nil || known {
		t.Fatalf("an unreadable main was recorded: %v %v", known, err)
	}
	again, err := BackfillRecordedMain(ctx, f.db, dataDir, nil)
	if err != nil || again.Recorded != 0 || again.Kept != 2 || again.Unknown != 1 {
		t.Fatalf("second backfill: %+v %v", again, err)
	}
	wantMainRecorded(t, f.db, dataDir, older.ID, scm.ViaBackfill)
}

// Creating a project takes one reading of the lifecycle's clock for the
// project and its receipt: on a clock months from the machine's that moves
// on a second at every reading, the project's time is one of that clock's
// readings, and the receipt carries it, not a later one.
//
// Kills: the project or its receipt stamped by the machine's clock (two
// such writes would agree with each other, but not with the injected
// clock), or by two separate readings.
func TestACreatedProjectsReceiptCarriesItsTime(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	clk := clocktest.NewStepping(start, time.Second)
	f := newOwnerFixtureOn(t, false, clk.Now)
	p, err := f.lifecycle.Create(context.Background(), Draft{Slug: "shop", Name: "Shop", Actor: "bls"})
	if err != nil {
		t.Fatal(err)
	}
	exported, err := project.ExportIn(context.Background(), f.db, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var row struct {
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(exported, &row); err != nil {
		t.Fatal(err)
	}
	// Provenance, not only agreement: the time is a reading the injected
	// clock gave, between its first reading and the one it would give next.
	if since := row.CreatedAt.Sub(start); row.CreatedAt.Before(start) || !row.CreatedAt.Before(clk.Next()) || since%time.Second != 0 {
		t.Fatalf("created at %v, not a reading of the lifecycle's clock (%v to %v)", row.CreatedAt, start, clk.Next())
	}
	lines, err := audit.Recent(context.Background(), f.db, audit.Filter{KindPrefix: "project.created", Limit: 1})
	if err != nil || len(lines) != 1 || lines[0].At != row.CreatedAt.UTC().Format(time.RFC3339) {
		t.Fatalf("receipt %+v %v, want it stamped %v, the project's time", lines, err, row.CreatedAt)
	}
}
