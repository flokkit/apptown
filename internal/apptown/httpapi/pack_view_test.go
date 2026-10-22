package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
)

func TestPinnedPackReadersShareExactContents(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	projectID, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "packs", Name: "Packs"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wow.NewLibrary(db).Create(t.Context(), "rule", "A rule.", "", "alice"); err != nil {
		t.Fatal(err)
	}
	catalogPacks := wow.NewPacks(db)
	for version := 1; version <= 2; version++ {
		got, err := catalogPacks.Publish(t.Context(), "pinned", "Pinned", encodedJSON(wow.PackSpec{Platform: version == 1, Guidance: []string{"rule"}}), "")
		if err != nil || got != version {
			t.Fatalf("publish pinned v%d: version=%d err=%v", version, got, err)
		}
		if version == 1 {
			if err := db.InTx(context.Background(), func(tx *database.Tx) error {
				return adoption.SetMembershipIn(t.Context(), tx, projectID, []adoption.Membership{{Pack: "pinned", Version: 1}}, "chosen", "alice", time.Now())
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	ways := wow.New(db, wow.Dependencies{})
	named, err := ways.Pack(t.Context(), "pinned")
	if err != nil {
		t.Fatal(err)
	}
	listed, err := ways.Packs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if named == nil || len(listed) != 1 || named.ID != listed[0].ID || named.LatestVersion != 2 || encodedJSON(named.Spec()) != encodedJSON(listed[0].Spec()) {
		t.Fatalf("point/list metadata differs: %+v / %+v", named, listed)
	}
	if missing, err := ways.Pack(t.Context(), "missing"); err == nil || missing != nil {
		t.Fatal("missing named pack did not remain absent")
	}
	counted := &countedQueries{Handle: db.SQL()}
	// Publication reads are tested in Adoption; this view still takes one read.
	packs, err := readProjectPacks(t.Context(), counted, projectID)
	if err != nil || len(packs) != 1 || !packs[0].Platform || packs[0].Recommended != 1 || !packGuidance(packs)["rule"] || counted.reads != 1 {
		t.Fatalf("membership snapshot: %+v, reads=%d, err=%v", packs, counted.reads, err)
	}
	body, err := json.Marshal(packs)
	if err != nil || strings.Contains(string(body), "guidance") {
		t.Fatalf("private contents leaked into membership API: %s, %v", body, err)
	}
	tx, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := adoption.SetMembershipIn(t.Context(), tx, projectID, []adoption.Membership{{Pack: "pinned", Version: 2}}, "chosen", "alice", time.Now()); err != nil {
		t.Fatal(err)
	}
	inside, err := readProjectPacks(t.Context(), tx, projectID)
	if err != nil || len(inside) != 1 || inside[0].Version != 2 || inside[0].Platform {
		t.Fatalf("transaction membership: %+v, %v", inside, err)
	}
	outside, err := readProjectPacks(t.Context(), db, projectID)
	if err != nil || len(outside) != 1 || outside[0].Version != 1 {
		t.Fatalf("uncommitted membership escaped: %+v, %v", outside, err)
	}
}
