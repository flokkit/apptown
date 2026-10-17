package adoption

import (
	"context"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption/internal/adoptionsql"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/ids"
)

// The participant clears one project's ways-of-working rows and leaves
// another project's alone.
func TestRemoveProjectInKeepsOtherProjects(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	a := adoptionTestProject(t, db, "a", "A")
	b := adoptionTestProject(t, db, "b", "B")
	ways := wow.New(db, wow.Dependencies{})
	blockRecord, err := ways.CreateBlock(t.Context(), "lang/go", "Go", "", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if version, err := ways.PublishPack(t.Context(), "go", "Go", `{}`, ""); err != nil || version != 1 {
		t.Fatalf("publish pack: %d %v", version, err)
	}
	packRecord, err := ways.Pack(t.Context(), "go")
	if err != nil {
		t.Fatal(err)
	}
	for _, pid := range []ids.ID{a, b} {
		q := adoptionsql.New(db.SQL())
		if _, err := q.InsertCurrentAdoption(context.Background(), adoptionsql.InsertCurrentAdoptionParams{ProjectID: pid, Revision: 1, Pack: "go", DriftNote: "drift"}); err != nil {
			t.Fatal(err)
		}
		if rows, err := q.PutProjectPolicy(context.Background(), adoptionsql.PutProjectPolicyParams{ID: ids.New(time.Now().UTC()), ProjectID: pid, Name: "p", Rule: "r", Config: []byte(`{}`), At: time.Now().UTC()}); err != nil || rows != 1 {
			t.Fatalf("policy: rows=%d err=%v", rows, err)
		}
		if _, err := q.InsertGuidanceImport(context.Background(), adoptionsql.InsertGuidanceImportParams{ID: ids.New(time.Now()), ProjectID: pid, BlockID: blockRecord.ID, Version: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := q.SetProjectLane(context.Background(), adoptionsql.SetProjectLaneParams{ProjectID: pid, Name: "land", Enabled: true, At: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		if rows, err := q.EnableProjectExtension(context.Background(), adoptionsql.EnableProjectExtensionParams{ProjectID: pid, Name: "lsp"}); err != nil || rows != 1 {
			t.Fatalf("extension: rows=%d err=%v", rows, err)
		}
		if _, err := q.InsertProjectPack(context.Background(), adoptionsql.InsertProjectPackParams{ID: ids.New(time.Now().UTC()), ProjectID: pid, PackID: packRecord.ID, Version: 1, Actor: "fixture", At: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		if _, err := q.InsertAdoption(context.Background(), adoptionsql.InsertAdoptionParams{ID: ids.New(time.Now().UTC()), ProjectID: pid, Revision: 1, Kind: AdoptionEdit.String(), Digest: "d", Receipt: []byte(`{}`), At: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.InTx(context.Background(), func(tx *database.Tx) error { return RemoveProjectIn(context.Background(), tx, a) }); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"policies", "guidance_imports", "project_lanes", "project_pi_extensions", "project_packs", "project_adoptions", "adoptions"} {
		var na, nb int
		// sql-guarantee: removal must clear every table adoption owns for one project and leave a neighbour's rows; no owner read lists every table's rows.
		if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE project_id=?`, a).Scan(&na); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE project_id=?`, b).Scan(&nb); err != nil {
			t.Fatal(err)
		}
		if na != 0 || nb != 1 {
			t.Fatalf("%s: a=%d b=%d", table, na, nb)
		}
	}
}
