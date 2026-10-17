package adoption

import (
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/wow"
	"testing"
	"time"
)

func TestMemoryDefaultReadsThePrimaryAdoptedPublication(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid := adoptionTestProject(t, db, "memory-defaults", "Memory defaults")
	ways := wow.New(db, wow.Dependencies{})
	if _, err := ways.PublishPack(t.Context(), "primary", "", `{"resources":{"memory":"160M"}}`, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := ways.PublishPack(t.Context(), "primary", "", `{"resources":{"memory":"1G"}}`, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := ways.PublishPack(t.Context(), "supplement", "", `{"resources":{"memory":"2G"}}`, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.InTx(t.Context(), func(tx *database.Tx) error {
		if err := InitializeIn(t.Context(), tx, pid, 0, "primary", ""); err != nil {
			return err
		}
		return SetMembershipIn(t.Context(), tx, pid, []Membership{{Pack: "primary", Version: 1}, {Pack: "supplement", Version: 1}}, "chosen", "alice", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	s := New(db, Dependencies{})
	d, name, version, err := s.MemoryDefault(t.Context(), pid)
	if err != nil || d.Memory != "160M" || name != "primary" || version != 1 {
		t.Fatalf("pinned primary default: %+v %s %d %v", d, name, version, err)
	}
	dbtest.FailNextStatement(t, db, "FROM adoptions a LEFT JOIN packs")
	if _, _, _, err := s.MemoryDefault(t.Context(), pid); err == nil {
		t.Fatal("default read failure became forge fallback")
	}
}

func TestAnExplicitPackPullPinsItsSelectedPublicationForMemoryDefaults(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid := adoptionTestProject(t, db, "pulled-memory", "Pulled memory")
	ways := wow.New(db, wow.Dependencies{})
	for _, p := range []struct{ name, memory string }{{"before", "64M"}, {"after", "160M"}, {"supplement", "2G"}} {
		if _, err := ways.PublishPack(t.Context(), p.name, "", `{"resources":{"memory":"`+p.memory+`"}}`, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.InTx(t.Context(), func(tx *database.Tx) error {
		if err := InitializeIn(t.Context(), tx, pid, 0, "before", ""); err != nil {
			return err
		}
		return SetMembershipIn(t.Context(), tx, pid, []Membership{{Pack: "before", Version: 1}, {Pack: "supplement", Version: 1}}, "chosen", "alice", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	before, err := ProjectPacksIn(t.Context(), db, pid)
	if err != nil {
		t.Fatal(err)
	}
	var supplemental ProjectPackRecord
	for _, m := range before {
		if m.Pack == "supplement" {
			supplemental = m
		}
	}
	s := New(db, Dependencies{Ways: ways})
	selected, err := ways.Pack(t.Context(), "after")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ways.PublishPack(t.Context(), "after", "", `{"resources":{"memory":"1G"}}`, ""); err != nil {
		t.Fatal(err)
	}
	dbtest.FailNextStatement(t, db, "INSERT INTO project_adoptions")
	if _, err := s.PullPack(t.Context(), pid, *selected, "bob"); err == nil {
		t.Fatal("pull survived failed revision persistence")
	}
	if d, name, version, err := s.MemoryDefault(t.Context(), pid); err != nil || d.Memory != "64M" || name != "before" || version != 1 {
		t.Fatalf("failed pull changed primary pin: %+v %s %d %v", d, name, version, err)
	}
	if _, err := s.PullPack(t.Context(), pid, *selected, "bob"); err != nil {
		t.Fatal(err)
	}
	d, name, version, err := s.MemoryDefault(t.Context(), pid)
	if err != nil || d.Memory != "160M" || name != "after" || version != 1 {
		t.Fatalf("explicit pull lost selected publication: %+v %s %d %v", d, name, version, err)
	}
	memberships, err := ProjectPacksIn(t.Context(), db, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(memberships) != 2 {
		t.Fatalf("pull changed supplemental membership scope: %+v", memberships)
	}
	for _, m := range memberships {
		if m.Pack == "supplement" && (m.AdoptedBy != supplemental.AdoptedBy || m.AdoptedAt != supplemental.AdoptedAt || m.Reason != supplemental.Reason) {
			t.Fatalf("pull rewrote supplemental attribution: before%+v after%+v", supplemental, m)
		}
		if m.Pack == "before" || m.Version != 1 {
			t.Fatalf("pull kept replaced primary or changed supplemental: %+v", memberships)
		}
	}
}

func TestPackPullPreservesTheRepositoryPlatformMembership(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid := adoptionTestProject(t, db, "platform-pull", "Platform pull")
	ways := wow.New(db, wow.Dependencies{})
	if _, err := ways.PublishPack(t.Context(), "foundation", "", `{"platform":true}`, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := ways.PublishPack(t.Context(), "application", "", `{"resources":{"memory":"160M"}}`, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.InTx(t.Context(), func(tx *database.Tx) error {
		if err := InitializeIn(t.Context(), tx, pid, 0, "foundation", ""); err != nil {
			return err
		}
		return SetMembershipIn(t.Context(), tx, pid, []Membership{{Pack: "foundation", Version: 1}}, "repository platform", "alice", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	before, err := ProjectPacksIn(t.Context(), db, pid)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := ways.Pack(t.Context(), "application")
	if err != nil {
		t.Fatal(err)
	}
	s := New(db, Dependencies{Ways: ways})
	if _, err := s.PullPack(t.Context(), pid, *selected, "bob"); err != nil {
		t.Fatal(err)
	}
	after, err := ProjectPacksIn(t.Context(), db, pid)
	if err != nil || len(after) != 2 {
		t.Fatalf("platform membership lost: %+v %v", after, err)
	}
	for _, m := range after {
		if m.Pack == "foundation" && (m.AdoptedBy != before[0].AdoptedBy || m.AdoptedAt != before[0].AdoptedAt || m.Reason != before[0].Reason) {
			t.Fatalf("platform attribution rewritten: before%+v after%+v", before[0], m)
		}
	}
	if d, name, version, err := s.MemoryDefault(t.Context(), pid); err != nil || d.Memory != "160M" || name != "application" || version != 1 {
		t.Fatalf("application did not become pinned primary: %+v %s %d %v", d, name, version, err)
	}
}
