package adoption

import (
	"context"
	"time"

	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/wow"
)

func multipackService(t *testing.T) (*Service, *wow.Service) {
	t.Helper()
	db := testDB(t)
	testAccounts(t, db, "alice")
	ways := wow.New(db, wow.Dependencies{})
	for _, p := range []wow.LibraryPolicy{
		{Name: "base-gate", Rule: "requireCapabilityBound", With: map[string]any{"capability": "registry"}},
		{Name: "data-gate", Rule: "requireCapabilityBound", With: map[string]any{"capability": "scanner"}},
	} {
		if err := ways.SaveLibraryPolicy(t.Context(), p, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	return New(db, Dependencies{Ways: ways, Authority: serviceAuthorityFunc(func(context.Context, access.Permission) error { return nil })}), ways
}

// multipackProject is a project with its first, empty revision recorded.
func multipackProject(t *testing.T, db *database.DB) ids.ID {
	t.Helper()
	pid := serviceAdoptionProject(t, db)
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		return RecordIn(t.Context(), tx, pid, 1, AdoptionCreate, "created", EmptyReceipt(), "alice", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	return pid
}

func publishFixturePack(t *testing.T, ways *wow.Service, name, spec string) {
	t.Helper()
	if _, err := ways.PublishPack(t.Context(), name, "", spec, ""); err != nil {
		t.Fatalf("publish %s: %v", name, err)
	}
}

// Applying several packs installs every pack's policies and extensions and
// records every pack, none above another: the receipt names them all and
// so do the memberships.
//
// Kills: setup taken from the first pick only (each pack's own policy and
// extension must arrive), and a receipt naming one "primary" pack.
func TestApplyingSeveralPacksInstallsEachOnesSetup(t *testing.T) {
	t.Parallel()
	s, ways := multipackService(t)
	pid := multipackProject(t, s.db)
	publishFixturePack(t, ways, "base", `{"policies":[{"name":"base-gate"}],"extensions":["lsp"]}`)
	publishFixturePack(t, ways, "data", `{"policies":[{"name":"data-gate"}],"extensions":["sql"]}`)
	c, err := s.PreparePack(t.Context(), pid, []PackPick{{Pack: "data", Version: 1}, {Pack: "base", Version: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Receipt.Packs, []string{"base", "data"}) {
		t.Fatalf("the receipt names %v, want both packs", c.Receipt.Packs)
	}
	var installed []string
	for _, p := range c.Installation.Policies {
		installed = append(installed, p.Name)
	}
	if !slices.Equal(installed, []string{"base-gate", "data-gate"}) || !slices.Equal(c.Installation.Extensions, []string{"lsp", "sql"}) {
		t.Fatalf("installation: policies %v, extensions %v; want each pack's", installed, c.Installation.Extensions)
	}
	if err := settleServiceCandidate(s.db, s, pid, *c, OperationApplyPack); err != nil {
		t.Fatal(err)
	}
	var members []string
	for _, m := range packsOf(t, s.db, pid) {
		members = append(members, m.Pack)
	}
	if !slices.Equal(members, []string{"base", "data"}) {
		t.Fatalf("memberships %v, want both packs", members)
	}
}

// Packs that disagree on something a project has one of are not taken
// together: the candidate names the problem and both packs, and adopting
// it is refused before anything is written.
//
// Kills: a later or earlier pick silently winning the disagreement.
func TestPacksThatDisagreeAreNotAdoptedTogether(t *testing.T) {
	t.Parallel()
	s, ways := multipackService(t)
	pid := multipackProject(t, s.db)
	publishFixturePack(t, ways, "go-ish", `{"toolchain":"go"}`)
	publishFixturePack(t, ways, "node-ish", `{"toolchain":"node"}`)
	c, err := s.Prepare(t.Context(), pid, Request{Packs: []PackPick{{Pack: "go-ish", Version: 1}, {Pack: "node-ish", Version: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Problems) != 1 || !strings.Contains(c.Problems[0], "go-ish and node-ish disagree on the toolchain") {
		t.Fatalf("problems %v, want the toolchain disagreement naming both packs", c.Problems)
	}
	prepared, err := s.PreparePack(t.Context(), pid, c.Packs)
	if err != nil {
		t.Fatal(err)
	}
	if err := settleServiceCandidate(s.db, s, pid, *prepared, OperationApplyPack); !errors.Is(err, ErrNotAdoptable) {
		t.Fatalf("adopting disagreeing packs: %v", err)
	}
	if cur := currentOf(t, s.db, pid); cur.Revision != 1 || len(packsOf(t, s.db, pid)) != 0 {
		t.Fatalf("a refused adoption moved the project: %+v %v", cur, packsOf(t, s.db, pid))
	}
}

// A pull moves one of the project's packs; a lane the pulled pack no longer
// carries stays selected while another of the project's packs carries it.
//
// Kills: pull treating the pulled pack as the project's whole way of
// working and deselecting the other packs' lanes.
func TestPullKeepsALaneAnotherOfTheProjectsPacksCarries(t *testing.T) {
	t.Parallel()
	s, ways := multipackService(t)
	pid := multipackProject(t, s.db)
	for _, name := range []string{"shared", "own"} {
		if _, _, err := ways.PublishWorkflow(t.Context(), strings.Replace(pullWorkflowYAML, "metadata: { name: flow }", "metadata: { name: "+name+" }", 1), "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	publishFixturePack(t, ways, "first", `{"workflows":["shared"]}`)
	publishFixturePack(t, ways, "second", `{"workflows":["shared","own"]}`)
	c, err := s.Prepare(t.Context(), pid, Request{Packs: []PackPick{{Pack: "first", Version: 1}, {Pack: "second", Version: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := settleServiceCandidate(s.db, s, pid, *c, OperationSelect); err != nil {
		t.Fatal(err)
	}
	publishFixturePack(t, ways, "second", `{"workflows":["own"]}`)
	second, err := ways.PackDeclaration(t.Context(), "second")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PackPullPlan(t.Context(), pid, *second)
	if err != nil || slices.Contains(plan.Removals, "shared") {
		t.Fatalf("the pull would deselect a lane the first pack carries: %+v %v", plan, err)
	}
}

// A pull is judged with the packs it leaves the project on, as any
// adoption is: a newer version of one pack that disagrees with another the
// project holds is refused, and nothing moves.
//
// Kills: pull recording a combination Settings would refuse.
func TestPullRefusesAVersionThatDisagreesWithAHeldPack(t *testing.T) {
	t.Parallel()
	s, ways := multipackService(t)
	pid := multipackProject(t, s.db)
	publishFixturePack(t, ways, "ui", `{"tools":{}}`)
	publishFixturePack(t, ways, "server", `{"toolchain":"go"}`)
	c, err := s.Prepare(t.Context(), pid, Request{Packs: []PackPick{{Pack: "server", Version: 1}, {Pack: "ui", Version: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := settleServiceCandidate(s.db, s, pid, *c, OperationSelect); err != nil {
		t.Fatal(err)
	}
	publishFixturePack(t, ways, "ui", `{"toolchain":"node"}`)
	ui, err := ways.PackDeclaration(t.Context(), "ui")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PullPack(adoptionTestCaller(), pid, *ui, "alice"); !errors.Is(err, ErrNotAdoptable) {
		t.Fatalf("pulling a disagreeing version: %v", err)
	}
	if cur := currentOf(t, s.db, pid); cur.Revision != 2 {
		t.Fatalf("the refused pull moved the revision to %d", cur.Revision)
	}
	for _, m := range packsOf(t, s.db, pid) {
		if m.Version != 1 {
			t.Fatalf("the refused pull moved %s to v%d", m.Pack, m.Version)
		}
	}
}

// After the upgrade, a project is recorded again by saving its guidance
// unchanged (the drift door). That records the resolved current selection:
// a receipt recorded before multipack, with its single "pack" name, reads;
// a block the project advanced past its pack's pin keeps its version; and
// the new receipt names the project's packs.
//
// Kills: a re-record that re-applies the pack's pins (rolling the block
// back to v1), and one that loses the packs.
func TestSavingGuidanceUnchangedRecordsThePacksAndKeepsEveryPin(t *testing.T) {
	t.Parallel()
	s, ways := multipackService(t)
	pid := multipackProject(t, s.db)
	block, err := ways.CreateBlock(t.Context(), "house-rule", "v1 body", "", "alice")
	if err != nil {
		t.Fatal(err)
	}
	publishFixturePack(t, ways, "house", `{"guidance":["house-rule"]}`) // pins house-rule v1
	if err := ways.BumpBlock(t.Context(), block.ID, "v2 body", "", "alice", "house-rule"); err != nil {
		t.Fatal(err)
	}
	pins := []wow.ResolvedPin{{BlockID: block.ID, Name: "house-rule", Version: 2}}
	receipt := EmptyReceipt()
	receipt.Guidance = []ReceiptBlock{{BlockID: block.ID, Name: "house-rule", Version: 2}}
	if err := s.db.InTx(context.Background(), func(tx *database.Tx) error {
		if err := PutImportsIn(t.Context(), tx, pid, pins, time.Now()); err != nil {
			return err
		}
		if err := SetMembershipIn(t.Context(), tx, pid, []Membership{{Pack: "house", Version: 1}}, "initial", "alice", time.Now()); err != nil {
			return err
		}
		return RecordIn(t.Context(), tx, pid, 2, AdoptionEdit, "advanced", receipt, "alice", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: a receipt as a release before multipack recorded it, naming one pack; no current operation writes that shape.
	if _, err := s.db.SQL().ExecContext(context.Background(), `UPDATE project_adoptions SET receipt_json = receipt_json || '{"pack":"house"}'::jsonb WHERE project_id=$1 AND revision=2`, pid); err != nil {
		t.Fatal(err)
	}
	expected := int64(2)
	if _, err := s.ReplaceGuidance(adoptionTestCaller(), pid, &expected, pins, "recorded again after the upgrade", "alice"); err != nil {
		t.Fatal(err)
	}
	got, err := s.CurrentReceipt(t.Context(), pid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 3 || !slices.Equal(got.Receipt.Packs, []string{"house"}) || len(got.Receipt.Guidance) != 1 || got.Receipt.Guidance[0].Version != 2 {
		t.Fatalf("re-recorded revision %d: packs %v, guidance %+v; want revision 3 naming house with house-rule v2", got.Revision, got.Receipt.Packs, got.Receipt.Guidance)
	}
}
