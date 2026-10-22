package adoption

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption/internal/adoptionsql"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
)

func TestAdoptInSettlesSelectionsAndReceiptTogether(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid := adoptionTestProject(t, db, "atomic-adopt", "Atomic adoption")
	if err := db.InTx(context.Background(), func(tx *database.Tx) error { return InitializeIn(t.Context(), tx, pid, 0, "", "review") }); err != nil {
		t.Fatal(err)
	}
	ways := wow.New(db, wow.Dependencies{})
	block, err := ways.CreateBlock(t.Context(), "atomic-guide", "held guide", "", "alice")
	if err != nil {
		t.Fatal(err)
	}
	blockID := block.ID
	selectedYAML := fmt.Sprintf(laneYAML, "selected-lane", "selected", "")
	systemYAML := fmt.Sprintf(laneYAML, "system-lane", "system", "  system: true\n")
	for _, body := range map[string]string{"selected-lane": selectedYAML, "system-lane": systemYAML} {
		if _, _, err := ways.PublishWorkflow(t.Context(), body, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	packVersion, err := ways.PublishPack(t.Context(), "atomic-pack", "", `{}`, "")
	if err != nil || packVersion != 1 {
		t.Fatalf("pack setup: %d %v", packVersion, err)
	}
	pack, err := ways.Pack(t.Context(), "atomic-pack")
	if err != nil {
		t.Fatal(err)
	}
	packName := pack.Name
	candidate := Candidate{ProjectID: pid, ExpectedRevision: 0, Packs: []PackPick{{Pack: "atomic-pack", Version: 1}}, Receipt: EmptyReceipt()}
	candidate.Installation = Installation{Policies: []Policy{{Name: "needs-registry", Rule: "requireCapabilityBound", With: map[string]any{"capability": "registry"}}}, Extensions: []string{"agent-addon"}}
	candidate.Receipt.Pack = "atomic-pack"
	candidate.Receipt.Guidance = []ReceiptBlock{{BlockID: blockID, Name: "atomic-guide", Version: 1}}
	candidate.Receipt.Workflows = []ReceiptWorkflow{{Name: "selected-lane", Version: 1, YAMLHash: HashText(selectedYAML)}, {Name: "system-lane", Version: 1, System: true, YAMLHash: HashText(systemYAML)}}
	policy, err := policyReceipt(candidate.Installation.Policies[0])
	if err != nil {
		t.Fatal(err)
	}
	candidate.Receipt.Policies = []ReceiptPolicy{policy}
	candidate.Receipt.Extensions = []string{"agent-addon"}
	candidate.Digest = Digest(candidate.Receipt)
	adopt := func() error {
		return db.InTx(context.Background(), func(tx *database.Tx) error {
			current, err := CurrentForUpdateIn(context.Background(), tx, pid)
			if err != nil {
				return err
			}
			_, err = adoptIn(context.Background(), tx, current, candidate, 0, "alice", "reviewed", nil, nil, time.Now())
			return err
		})
	}
	dbtest.FailNextStatement(t, db, "INSERT INTO audit_log")
	if err := adopt(); err == nil {
		t.Fatal("adoption without audit receipt succeeded")
	}
	for _, table := range []string{"guidance_imports", "project_lanes", "project_packs", "project_adoptions", "policies", "project_pi_extensions"} {
		// sql-guarantee: rollback proof over every table adoption writes, including lane rows whatever their name; no owner read lists a project's rows of every table.
		dbtest.Count(t, db, 0, `SELECT count(*) FROM `+table+` WHERE project_id=?`, pid)
	}
	if cur := currentOf(t, db, pid); cur.Revision != 0 || cur.Pack != "" || cur.Drift != "review" {
		t.Fatalf("failed adoption moved the current aggregate: %+v", cur)
	}
	if err := adopt(); err != nil {
		t.Fatal(err)
	}
	if n := countOf(importsOf(t, db, pid), func(i Import) bool { return i.BlockID == blockID }); n != 1 {
		t.Fatalf("imports of the guide: %d", n)
	}
	if n := countOf(policiesOf(t, db, pid), func(p Policy) bool { return p.Name == "needs-registry" }); n != 1 {
		t.Fatalf("installed policies: %d", n)
	}
	if n := countOf(extensionsOf(t, db, pid), func(e string) bool { return e == "agent-addon" }); n != 1 {
		t.Fatalf("enabled extensions: %d", n)
	}
	if found, enabled := laneRow(t, db, pid, "selected-lane"); !found || !enabled {
		t.Fatalf("selected lane: found=%v enabled=%v", found, enabled)
	}
	if found, _ := laneRow(t, db, pid, "system-lane"); found {
		t.Fatal("a system workflow became a project lane")
	}
	if n := countOf(packsOf(t, db, pid), func(p ProjectPackRecord) bool { return p.Pack == packName && p.Version == 1 }); n != 1 {
		t.Fatalf("pack memberships: %d", n)
	}
	if n := revisionsAt(t, db, pid, 1); n != 1 {
		t.Fatalf("revision 1 recorded %d times", n)
	}
	if cur := currentOf(t, db, pid); cur.Revision != 1 || cur.Pack != "atomic-pack" || cur.Drift != "" {
		t.Fatalf("current aggregate: %+v", cur)
	}
	if n := receiptsWhere(t, db, pid, "adoption.recorded", func(e audit.Entry) bool { return e.Actor == "alice" }); n != 1 {
		t.Fatalf("adoption receipts: %d", n)
	}
	if err := adopt(); err == nil {
		t.Fatal("stale expected revision was admitted")
	}
	if revs := revisionsOf(t, db, pid); len(revs) != 1 {
		t.Fatalf("stale adoption recorded: %+v", revs)
	}
}

func TestAdoptInChecksExtensionMembershipAfterSuppressedInsert(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid := adoptionTestProject(t, db, "extension-adopt", "Extension adoption")
	if err := db.InTx(context.Background(), func(tx *database.Tx) error { return InitializeIn(t.Context(), tx, pid, 0, "", "") }); err != nil {
		t.Fatal(err)
	}
	candidate := Candidate{ProjectID: pid, Receipt: EmptyReceipt(), Installation: Installation{Extensions: []string{"addon"}}}
	candidate.Receipt.Extensions = []string{"addon"}
	candidate.Digest = Digest(candidate.Receipt)
	adopt := func() error {
		return db.InTx(context.Background(), func(tx *database.Tx) error {
			current, err := CurrentForUpdateIn(context.Background(), tx, pid)
			if err != nil {
				return err
			}
			_, err = adoptIn(context.Background(), tx, current, candidate, 0, "alice", "extension", nil, nil, time.Now())
			return err
		})
	}
	if _, err := db.Exec(`CREATE FUNCTION suppress_adopt_extension() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER suppress_adopt_extension BEFORE INSERT ON project_pi_extensions FOR EACH ROW EXECUTE FUNCTION suppress_adopt_extension()`); err != nil {
		t.Fatal(err)
	}
	if err := adopt(); err == nil {
		t.Fatal("suppressed new extension was accepted as already enabled")
	}
	if revs := revisionsOf(t, db, pid); len(revs) != 0 {
		t.Fatalf("suppressed extension recorded a revision: %+v", revs)
	}
	if cur := currentOf(t, db, pid); cur.Revision != 0 {
		t.Fatalf("suppressed extension moved the aggregate: %+v", cur)
	}
	if _, err := db.Exec(`DROP TRIGGER suppress_adopt_extension ON project_pi_extensions`); err != nil {
		t.Fatal(err)
	}
	if _, err := adoptionsql.New(db.SQL()).EnableProjectExtension(t.Context(), adoptionsql.EnableProjectExtensionParams{ProjectID: pid, Name: "addon"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER suppress_adopt_extension BEFORE INSERT ON project_pi_extensions FOR EACH ROW EXECUTE FUNCTION suppress_adopt_extension()`); err != nil {
		t.Fatal(err)
	}
	if err := adopt(); err != nil {
		t.Fatalf("already enabled membership refused: %v", err)
	}
	if n := countOf(extensionsOf(t, db, pid), func(e string) bool { return e == "addon" }); n != 1 {
		t.Fatalf("enabled extensions: %d", n)
	}
}
