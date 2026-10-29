package adoption

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
)

type serviceAuthorityFunc func(context.Context, access.Permission) error

func (f serviceAuthorityFunc) Require(ctx context.Context, p access.Permission) (access.Caller, error) {
	c, _ := access.CallerFrom(ctx)
	return c, f(ctx, p)
}

type serviceCapabilitiesFunc func(context.Context, database.Queryer, ids.ID, []string) error

func (f serviceCapabilitiesFunc) HasCapabilitiesIn(ctx context.Context, q database.Queryer, pid ids.ID, required []string) error {
	return f(ctx, q, pid, required)
}

func serviceAdoptionProject(t *testing.T, db *database.DB) ids.ID {
	t.Helper()
	pid := adoptionTestProject(t, db, "checked-adoption", "Checked adoption")
	if err := db.InTx(context.Background(), func(tx *database.Tx) error { return InitializeIn(t.Context(), tx, pid, 0, "", "pending review") }); err != nil {
		t.Fatal(err)
	}
	return pid
}

func settleServiceCandidate(db *database.DB, s *Service, pid ids.ID, c Candidate, operation Operation) error {
	return db.InTx(context.Background(), func(tx *database.Tx) error {
		_, err := s.AdoptIn(adoptionTestCaller(), tx, pid, c, c.ExpectedRevision, "alice", "reviewed", operation)
		return err
	})
}

func assertUnadoptedServiceProject(t *testing.T, db *database.DB, pid ids.ID) {
	t.Helper()
	if cur := currentOf(t, db, pid); cur.Revision != 0 || cur.Pack != "" || cur.Drift != "pending review" {
		t.Fatalf("refused settlement moved the aggregate: %+v", cur)
	}
	for _, table := range []string{"guidance_imports", "project_lanes", "project_packs", "project_adoptions", "policies", "project_pi_extensions", "audit_log"} {
		// sql-guarantee: a refusal writes nothing to any table adoption owns, lane rows of any name included; no owner read lists every table's rows.
		dbtest.Count(t, db, 0, `SELECT count(*) FROM `+table+` WHERE project_id=$1`, pid)
	}
}

func TestServiceAdoptInDeniedAuthorityLeavesNoSettlement(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	testAccounts(t, db, "alice")
	pid := serviceAdoptionProject(t, db)
	denied := errors.New("selection is not permitted")
	calls := 0
	s := New(db, Dependencies{Ways: wow.New(db, wow.Dependencies{}), Authority: serviceAuthorityFunc(func(_ context.Context, p access.Permission) error {
		calls++
		if p.Resource.ProjectID != pid || p.Capability != access.CapProjectWayOfWorking {
			t.Fatalf("wrong authority request: %+v", p)
		}
		return denied
	})})
	c := Candidate{ProjectID: pid, Receipt: EmptyReceipt()}
	c.Digest = Digest(c.Receipt)
	if err := settleServiceCandidate(db, s, pid, c, OperationSelect); !errors.Is(err, denied) {
		t.Fatalf("authority refusal was lost: %v", err)
	}
	if calls != 1 {
		t.Fatalf("authority called %d times", calls)
	}
	assertUnadoptedServiceProject(t, db, pid)
}

func TestServiceAdoptInRecomputesErasedWeakening(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	testAccounts(t, db, "alice")
	pid := serviceAdoptionProject(t, db)
	ways := wow.New(db, wow.Dependencies{})
	if err := wow.NewLibrary(db).StockInsert(t.Context(), wow.StockBlock{Name: "service-gate", Body: "Must pass", ChecksJSON: `[{"name":"safe","severity":"high"}]`}); err != nil {
		t.Fatal(err)
	}
	blockRecord, err := ways.BlockByName(t.Context(), "service-gate")
	if err != nil {
		t.Fatal(err)
	}
	block := blockRecord.ID
	if version, err := ways.PublishPack(t.Context(), "service-house", "", `{"guidance":["service-gate"]}`, ""); err != nil || version != 1 {
		t.Fatalf("publish pack: %d %v", version, err)
	}
	packRecord, err := ways.Pack(t.Context(), "service-house")
	if err != nil {
		t.Fatal(err)
	}
	prior := EmptyReceipt()
	prior.Pack = "service-house"
	prior.Guidance = []ReceiptBlock{{BlockID: block, Name: "service-gate", Version: 1}}
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		if err := PutImportsIn(t.Context(), tx, pid, []wow.ResolvedPin{{BlockID: block, Name: "service-gate", Version: 1}}, time.Now()); err != nil {
			return err
		}
		if err := SetMembershipIn(t.Context(), tx, pid, []Membership{{Pack: "service-house", Version: 1}}, "initial", "alice", time.Now()); err != nil {
			return err
		}
		return RecordIn(t.Context(), tx, pid, 1, AdoptionCreate, "initial", prior, "alice", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("standards authority is required")
	allow := false
	calls := 0
	s := New(db, Dependencies{Ways: wow.New(db, wow.Dependencies{}), Authority: serviceAuthorityFunc(func(_ context.Context, p access.Permission) error {
		if p.Capability == access.CapProjectWayOfWorking {
			return nil
		}
		calls++
		if p.Capability != access.CapStandardsPacks {
			t.Fatalf("wrong weakening authority: %+v", p)
		}
		if !allow {
			return denied
		}
		return nil
	})})
	c := Candidate{ProjectID: pid, ExpectedRevision: 1, Packs: []PackPick{}, Receipt: EmptyReceipt(), Weakens: nil}
	c.Digest = Digest(c.Receipt)
	if err := settleServiceCandidate(db, s, pid, c, OperationSelect); !errors.Is(err, denied) {
		t.Fatalf("erased weakening bypassed standards: %v", err)
	}
	got, err := CurrentReceiptIn(t.Context(), db, pid)
	if err != nil || got.Revision != 1 || got.Digest != Digest(prior) {
		t.Fatalf("denied adoption altered the receipt: %+v %v", got, err)
	}
	if n := countOf(importsOf(t, db, pid), func(i Import) bool { return i.BlockID == block && i.Version == 1 && !i.Disabled }); n != 1 {
		t.Fatalf("denied adoption changed imports: %d", n)
	}
	if n := countOf(packsOf(t, db, pid), func(p ProjectPackRecord) bool { return p.Pack == packRecord.Name && p.Version == 1 }); n != 1 {
		t.Fatalf("denied adoption changed packs: %d", n)
	}
	if revs := revisionsOf(t, db, pid); len(revs) != 1 {
		t.Fatalf("denied adoption recorded: %+v", revs)
	}
	if n := receiptsWhere(t, db, pid, "adoption.recorded", nil); n != 1 {
		t.Fatalf("adoption receipts: %d", n)
	}
	allow = true
	if err := settleServiceCandidate(db, s, pid, c, OperationSelect); err != nil {
		t.Fatalf("the authorized removal should succeed: %v", err)
	}
	if calls != 2 {
		t.Fatalf("authority called %d times", calls)
	}
	if cur := currentOf(t, db, pid); cur.Revision != 2 {
		t.Fatalf("authorized removal: %+v", cur)
	}
	if im := importsOf(t, db, pid); len(im) != 0 {
		t.Fatalf("authorized removal kept imports: %+v", im)
	}
}

func TestServiceAdoptInRecomputesErasedPublishedCapabilities(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	testAccounts(t, db, "alice")
	pid := serviceAdoptionProject(t, db)
	ways := wow.New(db, wow.Dependencies{})
	if version, err := ways.PublishPack(t.Context(), "requires-scanner", "", `{"requires":["scanner"]}`, ""); err != nil || version != 1 {
		t.Fatalf("publish pack: %d %v", version, err)
	}
	packRecord, err := ways.Pack(t.Context(), "requires-scanner")
	if err != nil {
		t.Fatal(err)
	}
	missing := errors.New("scanner is not attached")
	allow := false
	calls := 0
	s := New(db, Dependencies{
		Ways:      wow.New(db, wow.Dependencies{}),
		Authority: serviceAuthorityFunc(func(context.Context, access.Permission) error { return nil }),
		Capabilities: serviceCapabilitiesFunc(func(_ context.Context, q database.Queryer, projectID ids.ID, required []string) error {
			calls++
			if _, ok := q.(*database.Tx); !ok || projectID != pid || !slices.Equal(required, []string{"scanner"}) {
				t.Fatalf("requirements were not reobserved in the scoped settlement transaction: %T %d %v", q, projectID, required)
			}
			if !allow {
				return missing
			}
			return nil
		}),
	})
	c := Candidate{ProjectID: pid, Packs: []PackPick{{Pack: "requires-scanner", Version: 1}}, Receipt: EmptyReceipt(), RequiredCapabilities: nil}
	c.Receipt.Pack = "requires-scanner"
	c.Digest = Digest(c.Receipt)
	if err := settleServiceCandidate(db, s, pid, c, OperationSelect); !errors.Is(err, missing) {
		t.Fatalf("erased requirements bypassed attached capabilities: %v", err)
	}
	assertUnadoptedServiceProject(t, db, pid)
	allow = true
	if err := settleServiceCandidate(db, s, pid, c, OperationSelect); err != nil {
		t.Fatalf("the attached requirement should permit settlement: %v", err)
	}
	if calls != 2 {
		t.Fatalf("capabilities checked %d times", calls)
	}
	if n := countOf(packsOf(t, db, pid), func(p ProjectPackRecord) bool { return p.Pack == packRecord.Name && p.Version == 1 }); n != 1 {
		t.Fatalf("pack memberships: %d", n)
	}
	if n := revisionsAt(t, db, pid, 1); n != 1 {
		t.Fatalf("revision 1 recorded %d times", n)
	}
}

func TestServiceAdoptInCannotClaimPackAuthorityForAnUnsealedCandidate(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid := serviceAdoptionProject(t, db)
	ways := wow.New(db, wow.Dependencies{})
	if version, err := ways.PublishPack(t.Context(), "published-pack", "", `{}`, ""); err != nil || version != 1 {
		t.Fatalf("publish pack: %d %v", version, err)
	}
	s := New(db, Dependencies{Ways: wow.New(db, wow.Dependencies{}), Authority: serviceAuthorityFunc(func(_ context.Context, p access.Permission) error {
		if p.Capability == access.CapProjectWayOfWorking {
			return nil // This caller may apply published packs but may not configure an arbitrary selection.
		}
		return errors.New("arbitrary selection is not permitted")
	})})
	c := Candidate{ProjectID: pid, Packs: []PackPick{{Pack: "published-pack", Version: 1}}, Receipt: EmptyReceipt()}
	c.Receipt.Pack = "published-pack"
	c.Digest = Digest(c.Receipt)
	if err := settleServiceCandidate(db, s, pid, c, OperationApplyPack); err == nil {
		t.Fatal("caller-selected operation conferred pack authority without a package-prepared application")
	}
	assertUnadoptedServiceProject(t, db, pid)
}

func TestSelectionCannotInstallPoliciesOrCreateAnInstallationTarget(t *testing.T) {
	t.Parallel()
	db, service, c := validationCandidate(t)
	called := 0
	service.deps.Authority = serviceAuthorityFunc(func(context.Context, access.Permission) error { called++; return nil })
	err := db.InTx(context.Background(), func(tx *database.Tx) error {
		_, err := service.AdoptIn(adoptionTestCaller(), tx, c.ProjectID, c, 0, "alice", "selection", OperationSelect)
		return err
	})
	if !errors.Is(err, ErrPackApplication) {
		t.Fatalf("selection installed pack setup: %v", err)
	}
	err = db.InTx(context.Background(), func(tx *database.Tx) error {
		_, err := service.RecordTargetIn(adoptionTestCaller(), tx, Target{ProjectID: c.ProjectID, ChangeID: idstest.N(7), Candidate: c, Actor: "alice"})
		return err
	})
	if !errors.Is(err, ErrPackApplication) {
		t.Fatalf("installation escaped into target without its durable setup: %v", err)
	}
	if _, err := service.OpenUplift(adoptionTestCaller(), c.ProjectID, c, "alice", "", nil); !errors.Is(err, ErrPackApplication) {
		t.Fatalf("installation admitted for uplift: %v", err)
	}
	if called != 0 {
		t.Fatalf("invalid command reached authority %d times", called)
	}
	if p := policiesOf(t, db, c.ProjectID); len(p) != 0 {
		t.Fatalf("invalid command installed policies: %+v", p)
	}
	if revs := revisionsOf(t, db, c.ProjectID); len(revs) != 0 {
		t.Fatalf("invalid command recorded: %+v", revs)
	}
}

func adoptionTestCaller() context.Context {
	return access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "alice", Via: "session"}, Verified: true, Receipt: "alice"})
}

func (serviceAuthorityFunc) VisibleProjects(context.Context, access.Capability) ([]ids.ID, error) {
	return nil, access.ErrAuthorityUnavailable
}

// PicksWith is the one change a what-if and a pack-version eval ask about:
// the named pack moved to the version, or added when the project is not on
// it, and every other pick the project made left as it is, the pack its
// receipt names first (the first pick is the one a candidate names); and
// any other pack named with it that the project already holds.
func TestPicksWithMovesOnlyTheNamedPack(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid := serviceAdoptionProject(t, db)
	ways := wow.New(db, wow.Dependencies{})
	for _, name := range []string{"house", "extra", "candidate"} {
		if _, err := ways.PublishPack(t.Context(), name, "", `{}`, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		if err := SetMembershipIn(t.Context(), tx, pid, []Membership{{Pack: "house", Version: 1}, {Pack: "extra", Version: 1}}, "initial", "alice", time.Now()); err != nil {
			return err
		}
		receipt := EmptyReceipt()
		receipt.Pack = "house"
		return RecordIn(t.Context(), tx, pid, 1, AdoptionCreate, "initial", receipt, "alice", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	s := New(db, Dependencies{Ways: ways})
	moved, err := s.PicksWith(t.Context(), pid, "extra", 4)
	if err != nil {
		t.Fatal(err)
	}
	if want := []PackPick{{Pack: "house", Version: 1}, {Pack: "extra", Version: 4}}; !slices.Equal(moved, want) {
		t.Fatalf("moving a pack the project is on: %+v, want %+v", moved, want)
	}
	added, err := s.PicksWith(t.Context(), pid, "candidate", 2)
	if err != nil {
		t.Fatal(err)
	}
	if want := []PackPick{{Pack: "house", Version: 1}, {Pack: "extra", Version: 1}, {Pack: "candidate", Version: 2}}; !slices.Equal(added, want) {
		t.Fatalf("a pack the project is not on joins: %+v, want %+v", added, want)
	}
	// Other packs moved with it move only where the project holds them:
	// "each project's own pack at its newest" never adds a pack.
	paired, err := s.PicksWith(t.Context(), pid, "extra", 4, PackPick{Pack: "house", Version: 3}, PackPick{Pack: "candidate", Version: 2})
	if err != nil {
		t.Fatal(err)
	}
	if want := []PackPick{{Pack: "house", Version: 3}, {Pack: "extra", Version: 4}}; !slices.Equal(paired, want) {
		t.Fatalf("moving the project's own pack with the named one: %+v, want %+v", paired, want)
	}
}
