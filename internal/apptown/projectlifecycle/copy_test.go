package projectlifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/clock/clocktest"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
)

// An eval's copy is its source as it was at one commit, with exactly the
// way of working the source is on now: its history up to the commit and
// nothing after, the same adoption receipt (packs, blocks, lanes, policies,
// extensions), no members, and its copy record written with it. That holds
// when the source's blocks were advanced past its pack's pins (an upgrade
// moves blocks and leaves the pack version alone): the copy reproduces what
// the source recorded. Only an eval's copy takes a way of working that way.
func TestACopyIsTheSourceAtOneCommitWithItsWayOfWorking(t *testing.T) {
	t.Parallel()
	f := newOwnerFixture(t, true)
	ctx := context.Background()
	src, base := copySource(t, f)
	applied, err := f.adoption.CurrentReceipt(ctx, src.ID)
	if err != nil || len(applied.Receipt.Guidance) == 0 {
		t.Fatalf("the pack gave no guidance: %+v %v", applied, err)
	}
	moved := applied.Receipt.Guidance[0]
	if err := wow.NewLibrary(f.db).Bump(ctx, moved.BlockID, "a newer version", "", "bls", moved.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := f.adoption.AdvanceToLatest(ctx, src.ID, "upgrade to 1.0", "bls"); err != nil {
		t.Fatal(err)
	}

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
	// Eval labels stay fixed through seed and later refresh jobs.
	wantDescription := "A throwaway copy of " + src.Name + " made for an eval"
	if copy.BlurbSource != "person" || copy.Blurb != wantDescription {
		t.Fatalf("copy description: %+v", copy)
	}
	d := Descriptions{Service: f.lifecycle, Models: descriptionModel("fixture/fast"), Completion: descriptionAnswer(func(context.Context, modelcall.Call) (string, *modelcall.Usage, error) {
		t.Fatal("eval copy called the description model")
		return "", nil, nil
	})}
	if err := d.Refresh(ctx, copy.ID); err != nil {
		t.Fatal(err)
	}
	held, err := f.lifecycle.projects.ByID(ctx, copy.ID)
	if err != nil || held.Blurb != wantDescription || held.BlurbSource != "person" {
		t.Fatalf("refreshed copy: %+v %v", held, err)
	}
	// Even clearing the label must not opt a non-ordinary project into generation.
	empty := ""
	if _, err := f.lifecycle.UpdateSettings(ctx, copy.ID, project.SettingsPatch{ExpectedRevision: &copy.SettingsRevision, Blurb: &empty}, "eval/1"); err != nil {
		t.Fatal(err)
	}
	if err := d.Refresh(ctx, copy.ID); err != nil {
		t.Fatal(err)
	}
	hub := filepath.Join(f.lifecycle.dataDir, "workspaces", "project-"+copy.ID.String())
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
	if want.Receipt.Guidance[0].Version <= moved.Version {
		t.Fatalf("the source's %s stayed at v%d", moved.Name, want.Receipt.Guidance[0].Version)
	}
	got, err := f.adoption.CurrentReceipt(ctx, copy.ID)
	if err != nil || adoption.Digest(got.Receipt) != adoption.Digest(want.Receipt) || len(got.Receipt.Guidance) == 0 {
		t.Fatalf("the copy's way of working differs:\n got %+v\nwant %+v\n%v", got, want, err)
	}
	other, err := f.lifecycle.Create(ctx, Draft{Slug: "other", Name: "Other", Actor: "bls"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.adoption.PrepareCopy(ctx, other.ID, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.lifecycle.Adopt(adoption.WithSystemAuthority(ctx, other.ID), other, c, "bls", "copied", adoption.OperationApplyPack); !errors.Is(err, adoption.ErrNotACopy) {
		t.Fatalf("a project that is not an eval's copy took a copy's way of working: %v", err)
	}
	grants, err := f.identity.GrantsAt(ctx, access.ScopeProject, copy.ID.String())
	if err != nil || len(grants) != 0 {
		t.Fatalf("the copy has members: %+v %v", grants, err)
	}
}

// An eval re-runs an adoption Change from where it started: a copy takes
// the receipt its source recorded at that revision, an adoption Change in
// the copy toward the source's way of working now carries the checks that
// move brings, and its settlement (the real one, after the Change ships)
// moves the copy onto exactly the source's receipt and stamps the Change.
func TestACopyStartsWhereAnAdoptionChangeStartedAndSettlesOnItsTarget(t *testing.T) {
	t.Parallel()
	f := newOwnerFixture(t, true)
	ctx := context.Background()
	src, base := copySource(t, f)
	before, err := f.adoption.CurrentReceipt(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	// One of the pack's blocks gets a newer version with the same checks,
	// and the source advances to it: the move the adoption Change made.
	var moved *wow.BlockObservation
	for _, b := range before.Receipt.Guidance {
		obs, err := f.ways.Block(ctx, wow.BlockRef{ID: b.BlockID, Version: b.Version})
		if err != nil {
			t.Fatal(err)
		}
		if obs.ChecksJSON != "" && obs.ChecksJSON != "[]" {
			moved = &obs
			break
		}
	}
	if moved == nil {
		t.Fatal("the pack has no block with checks")
	}
	if err := wow.NewLibrary(f.db).Bump(ctx, moved.Block.ID, moved.Body+"\nrevised", moved.ChecksJSON, "bls", moved.Block.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := f.adoption.AdvanceToLatest(ctx, src.ID, "upgrade to 1.0", "bls"); err != nil {
		t.Fatal(err)
	}
	now, err := f.adoption.CurrentReceipt(ctx, src.ID)
	if err != nil || now.Revision == before.Revision {
		t.Fatalf("the source did not move: %+v %v", now, err)
	}

	copy, err := f.lifecycle.CreateCopy(ctx, CopyDraft{Slug: "eval-1-1", Name: "Eval 1 baseline", SourceID: src.ID, Commit: base, Who: "eval/1", Revision: before.Revision,
		Record: func(*database.Tx, ids.ID) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := f.adoption.CurrentReceipt(ctx, copy.ID); err != nil || adoption.Digest(got.Receipt) != adoption.Digest(before.Receipt) {
		t.Fatalf("the copy did not start where the change started: %+v %v", got, err)
	}
	target, err := f.adoption.PrepareCopyUplift(ctx, copy.ID, src.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if target.Digest != adoption.Digest(now.Receipt) || len(target.NewChecks) == 0 {
		t.Fatalf("the target is %s with checks %+v, want the source's now (%s) with the moved block's checks", target.Digest, target.NewChecks, adoption.Digest(now.Receipt))
	}
	driver := access.WithCaller(ctx, access.Caller{Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: "eval", Via: "eval/1", ProjectID: copy.ID}, Verified: true, Receipt: "eval/1"})
	changes := workflow.NewChanges(f.db, access.Checked{Authority: identity.Scoped(f.identity, "work"), Projects: project.New(f.db, nil)}, nil)
	checks := []workflow.AcceptanceCheck{}
	for _, c := range target.NewChecks {
		checks = append(checks, workflow.AcceptanceCheck{Block: c.Block, Check: c.Check, Version: c.Version})
	}
	ch, err := changes.Uplift(driver, workflow.Uplift{ProjectID: copy.ID, ExpectedRevision: target.ExpectedRevision, Acceptance: workflow.Acceptance{TargetDigest: target.Digest, Checks: checks},
		Words: target.Words(), Target: copyTargetRecorder{adoption: f.adoption, candidate: *target, copy: copy.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if !ch.LandsWithoutCode() {
		t.Fatalf("the copy's change is not an adoption change: %+v", ch)
	}
	// sql-guarantee: shipping needs a whole landing; settlement reads only that the Change shipped.
	if _, err := f.db.SQL().ExecContext(ctx, `UPDATE changes SET state='shipped' WHERE id=$1`, ch.ID); err != nil {
		t.Fatal(err)
	}
	shipped, err := workflow.ReadChangeIn(ctx, f.db, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	var recorded adoption.Candidate
	if err := json.Unmarshal([]byte(shipped.TargetCandidateJSON), &recorded); err != nil {
		t.Fatal(err)
	}
	if err := f.adoption.CheckTarget(ctx, copy.ID, ch.ID, shipped.TargetAdoptionID, shipped.ExpectedRevision, recorded); err != nil {
		t.Fatal(err)
	}
	if _, err := f.lifecycle.SettleUplift(adoption.WithSystemAuthority(ctx, copy.ID), copy, &recorded, "apptown-system", "landed", ch.ID); err != nil {
		t.Fatal(err)
	}
	if settled, err := workflow.ReadChangeIn(ctx, f.db, ch.ID); err != nil || settled.AdoptedRevision == 0 {
		t.Fatalf("the change was not stamped: %+v %v", settled, err)
	}
	if got, err := f.adoption.CurrentReceipt(ctx, copy.ID); err != nil || adoption.Digest(got.Receipt) != adoption.Digest(now.Receipt) {
		t.Fatalf("the copy did not end on the source's way of working: %+v %v", got, err)
	}
}

// copyTargetRecorder records an adoption Change's target in a copy inside
// Work's admission, as an eval's driver does, with the copy's own
// authority.
type copyTargetRecorder struct {
	adoption  *adoption.Service
	candidate adoption.Candidate
	copy      ids.ID
}

func (r copyTargetRecorder) RecordTargetIn(ctx context.Context, tx *database.Tx, in workflow.TargetChange) (workflow.RecordedTarget, error) {
	out, err := r.adoption.RecordTargetIn(adoption.WithSystemAuthority(ctx, r.copy), tx, adoption.Target{ProjectID: in.ProjectID, ChangeID: in.ChangeID, Candidate: r.candidate, Actor: in.Actor})
	return workflow.RecordedTarget{ID: out.ID, Candidate: out.Candidate}, err
}

// copySource is project "shop" on the go pack, its repository seeded
// with two commits: the base it returns, and one after it.
func copySource(t *testing.T, f ownerFixture) (*project.Project, string) {
	t.Helper()
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
	pk, err := f.ways.Pack(ctx, "go")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.lifecycle.ApplyPack(adoption.WithSystemAuthority(ctx, src.ID), src, []*wow.Pack{pk}, "bls"); err != nil {
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
	return src, base
}

// wantMainRecorded: AppTown's record of a project's main is its hub's main,
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
	if want := scm.Ref(t.Context(), hubDir(dataDir, p.ID), "refs/heads/"+p.DefaultBranch); m.SHA != want || m.Via != string(via) {
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
	before := scm.Ref(t.Context(), hubDir(f.lifecycle.dataDir, p.ID), "refs/heads/main")
	pk, err := f.ways.Pack(t.Context(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.lifecycle.ApplyPack(adoption.WithSystemAuthority(context.Background(), p.ID), p, []*wow.Pack{pk}, "bls"); err != nil {
		t.Fatal(err)
	}
	if scm.Ref(t.Context(), hubDir(f.lifecycle.dataDir, p.ID), "refs/heads/main") == before {
		t.Fatal("the pack committed no starter files")
	}
	wantMainRecorded(t, f.db, f.lifecycle.dataDir, p.ID, scm.ViaGenesis)
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
	read, err := project.New(f.db, nil).ByID(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var row struct{ CreatedAt time.Time }
	if row.CreatedAt, err = time.Parse(time.RFC3339Nano, read.CreatedAt); err != nil {
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
