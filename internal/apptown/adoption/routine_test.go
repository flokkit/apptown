package adoption

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"testing"
	"time"
)

func TestRoutineAdoptionInventoryCannotHideUnclassifiedSemantics(t *testing.T) {
	before := EmptyReceipt()
	before.Guidance = []ReceiptBlock{{Position: 0, Name: "team/clarity", BlockID: idstest.N(1), Version: 1, Tier: "team"}}
	before.Workflows = []ReceiptWorkflow{{Name: "team-flow", Version: 1, YAMLHash: "exact", Origin: "here"}}
	before.Policies = []ReceiptPolicy{{Name: "review", Rule: "requireStepBefore", ConfigHash: "exact"}}
	before.Extensions = []string{"project-extension"}
	memberships := []ProjectPackRecord{{Pack: "team", Version: 1}}
	picks := []PackPick{{Pack: "team", Version: 2}}
	after := before
	after.Guidance = append([]ReceiptBlock(nil), before.Guidance...)
	after.Guidance[0].Version = 2
	moves, err := routineAdoptionMoves(before, after, memberships, picks)
	if err != nil || len(moves) != 1 || moves[0].ID != idstest.N(1) || moves[0].From != 1 || moves[0].To != 2 {
		t.Fatalf("exact move: %+v %v", moves, err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Receipt)
	}{
		{"disabled", func(r *Receipt) { r.Guidance[0].Disabled = true }},
		{"identity", func(r *Receipt) { r.Guidance[0].BlockID = idstest.N(2) }},
		{"tier", func(r *Receipt) { r.Guidance[0].Tier = "platform" }},
		{"position", func(r *Receipt) { r.Guidance[0].Position = 1 }},
		{"backward", func(r *Receipt) { r.Guidance[0].Version = 0 }},
		{"workflow", func(r *Receipt) { r.Workflows = nil }},
		{"policy", func(r *Receipt) { r.Policies = nil }},
		{"extensions", func(r *Receipt) { r.Extensions = nil }},
		{"resolver", func(r *Receipt) { r.Resolver++ }},
		{"selected pack", func(r *Receipt) { r.Packs = []string{"other"} }},
		{"membership", func(r *Receipt) { r.Guidance = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := after
			changed.Guidance = append([]ReceiptBlock(nil), after.Guidance...)
			tc.change(&changed)
			if _, err := routineAdoptionMoves(before, changed, memberships, picks); err == nil {
				t.Fatal("unsupported semantic change accepted")
			}
		})
	}
	for _, changed := range [][]PackPick{nil, {{Pack: "other", Version: 2}}, {{Pack: "team", Version: 0}}, {{Pack: "team", Version: 2}, {Pack: "extra", Version: 1}}} {
		if _, err := routineAdoptionMoves(before, after, memberships, changed); err == nil {
			t.Fatalf("changed pack membership accepted: %+v", changed)
		}
	}
}

func TestRoutineTargetRequiresEveryActualPublicationTransition(t *testing.T) {
	db := testDB(t)
	people := identity.New(db, identity.Config{})
	person, err := people.EnsurePerson(t.Context(), "standards-manager")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = people.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: person.Username, Role: access.RoleStandardsAdmin, Scope: access.ScopeInstance, By: person.Username}, ""); err != nil {
		t.Fatal(err)
	}
	ctx := access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{ID: person.PrincipalID, Name: person.Username, Kind: "user", Via: "session"}, Verified: true})
	pid := adoptionTestProject(t, db, "routine-target", "Routine target")
	publications := wow.NewRoutineTransitions(db, access.Checked{Authority: people}, people, nil)
	ways := wow.New(db, wow.Dependencies{})
	service := New(db, Dependencies{Ways: ways, RoutinePublications: publications})
	before := EmptyReceipt()
	var idsByName []ids.ID
	for i, name := range []string{"team/first", "team/second"} {
		block, e := ways.CreateBlock(ctx, name, "Original guidance.", "", person.Username)
		if e != nil {
			t.Fatal(e)
		}
		idsByName = append(idsByName, block.ID)
		before.Guidance = append(before.Guidance, ReceiptBlock{Position: i, Name: name, BlockID: block.ID, Version: 1})
	}
	current, err := service.Advance(ctx, pid, AdoptionEdit, "fixture initial adoption", person.Username, func(*database.Tx, *RecordedRevision, time.Time) (Receipt, error) { return before, nil })
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range idsByName {
		if err = ways.BumpBlock(ctx, id, "Clarified guidance.", "[]", person.Username, before.Guidance[i].Name); err != nil {
			t.Fatal(err)
		}
	}
	candidate, err := service.Prepare(ctx, pid, Request{Additions: []string{"team/first@2", "team/second@2"}})
	if err != nil {
		t.Fatal(err)
	}
	// Record the real owner-prepared target. Its complete stored JSON, not the
	// presentation summaries, is what the future Work assessment submits.
	changeID := idstest.N(44)
	target, err := database.Transact(ctx, db, func(tx *database.Tx) (ScopedTarget, error) {
		return recordTargetIn(ctx, tx, Target{ProjectID: pid, ChangeID: changeID, Candidate: *candidate, Actor: person.Username}, time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	check := func(raw []byte) (RoutineTarget, error) {
		return database.Transact(ctx, db, func(tx *database.Tx) (RoutineTarget, error) {
			if _, e := CurrentForUpdateIn(ctx, tx, pid); e != nil {
				return RoutineTarget{}, e
			}
			return service.CheckRoutineTargetIn(ctx, tx, pid, changeID, target.ID, current.Revision, raw)
		})
	}
	if _, err = check(target.Candidate); err == nil {
		t.Fatal("unclassified target accepted")
	}
	var consents []wow.RoutinePublicationClassification
	for i, id := range idsByName {
		move := wow.RoutinePublicationTransition{Kind: "block", ID: id, From: 1, To: 2}
		question, e := publications.Preview(ctx, move)
		if e != nil {
			t.Fatal(e)
		}
		consent, e := publications.Classify(ctx, wow.RoutinePublicationConsent{RequestID: ids.New(time.Now()), Transition: move, ShownDigest: question.Digest})
		if e != nil {
			t.Fatal(e)
		}
		consents = append(consents, consent)
		if i == 0 {
			if _, e = check(target.Candidate); e == nil {
				t.Fatal("one classified block authorized unrelated second block")
			}
		}
	}
	got, err := check(target.Candidate)
	if err != nil || got.CurrentID != current.ID || got.TargetID != target.ID || len(got.Classifications) != 2 || got.CandidateDigest == "" {
		t.Fatalf("complete classified target: %+v %v", got, err)
	}
	altered := *candidate
	altered.RequiredCapabilities = []string{"new-authority"}
	raw, err := json.Marshal(altered)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = check(raw); err == nil {
		t.Fatal("same receipt digest concealed new authority")
	}
	if _, err = publications.Revoke(ctx, consents[1].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = check(target.Candidate); err == nil {
		t.Fatal("revoked subset remained sufficient")
	}
}

func TestRoutinePackTargetPreservesExistingCapabilityRequirements(t *testing.T) {
	db := testDB(t)
	people := identity.New(db, identity.Config{})
	person, err := people.EnsurePerson(t.Context(), "standards-manager")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = people.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: person.Username, Role: access.RoleStandardsAdmin, Scope: access.ScopeInstance, By: person.Username}, ""); err != nil {
		t.Fatal(err)
	}
	ctx := access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{ID: person.PrincipalID, Name: person.Username, Kind: "user", Via: "session"}, Verified: true})
	pid := adoptionTestProject(t, db, "routine-requirements", "Routine requirements")
	publications := wow.NewRoutineTransitions(db, access.Checked{Authority: people}, people, nil)
	ways := wow.New(db, wow.Dependencies{})
	service := New(db, Dependencies{Ways: ways, RoutinePublications: publications, Authority: serviceAuthorityFunc(func(context.Context, access.Permission) error { return nil }), Capabilities: serviceCapabilitiesFunc(func(_ context.Context, _ database.Queryer, _ ids.ID, required []string) error {
		if len(required) != 1 || required[0] != "scanner" {
			return fmt.Errorf("unexpected capability selection %v", required)
		}
		return nil
	})})
	if _, err = service.Advance(ctx, pid, AdoptionEdit, "initial empty adoption", person.Username, func(*database.Tx, *RecordedRevision, time.Time) (Receipt, error) { return EmptyReceipt(), nil }); err != nil {
		t.Fatal(err)
	}
	block, err := ways.CreateBlock(ctx, "team/scanning", "Explain scanning.", "", person.Username)
	if err != nil {
		t.Fatal(err)
	}
	packs := wow.NewPacks(db)
	declaration := `{"guidance":["team/scanning"],"requires":["scanner"]}`
	if _, err = packs.Publish(ctx, "scanner-pack", "Scanner", declaration, ""); err != nil {
		t.Fatal(err)
	}
	initial, err := service.Prepare(ctx, pid, Request{Packs: []PackPick{{Pack: "scanner-pack", Version: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.InTx(ctx, func(tx *database.Tx) error {
		_, e := service.AdoptIn(WithSystemAuthority(ctx, pid), tx, pid, *initial, initial.ExpectedRevision, person.Username, "adopt scanner pack", OperationSelect)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if err = ways.BumpBlock(ctx, block.ID, "Explain scanning and its evidence.", "[]", person.Username, block.Name); err != nil {
		t.Fatal(err)
	}
	if _, err = packs.CutOnPinChange(ctx); err != nil {
		t.Fatal(err)
	}
	pack, err := packs.ByName(ctx, "scanner-pack")
	if err != nil {
		t.Fatal(err)
	}
	move := wow.RoutinePublicationTransition{Kind: "pack", ID: pack.ID, From: 1, To: 2}
	question, err := publications.Preview(ctx, move)
	if err != nil || !question.Eligible {
		t.Fatalf("unchanged requirement publication: %+v %v", question, err)
	}
	if _, err = publications.Classify(ctx, wow.RoutinePublicationConsent{RequestID: ids.New(time.Now()), Transition: move, ShownDigest: question.Digest}); err != nil {
		t.Fatal(err)
	}
	candidate, err := service.Prepare(ctx, pid, Request{Packs: []PackPick{{Pack: "scanner-pack", Version: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidate.RequiredCapabilities) != 1 || candidate.RequiredCapabilities[0] != "scanner" {
		t.Fatalf("fixture lost retained requirement: %+v", candidate)
	}
	changeID := idstest.N(55)
	target, err := database.Transact(ctx, db, func(tx *database.Tx) (ScopedTarget, error) {
		return recordTargetIn(ctx, tx, Target{ProjectID: pid, ChangeID: changeID, Candidate: *candidate, Actor: person.Username}, time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	checked, err := database.Transact(ctx, db, func(tx *database.Tx) (RoutineTarget, error) {
		if _, e := CurrentForUpdateIn(ctx, tx, pid); e != nil {
			return RoutineTarget{}, e
		}
		return service.CheckRoutineTargetIn(ctx, tx, pid, changeID, target.ID, candidate.ExpectedRevision, target.Candidate)
	})
	if err != nil || len(checked.Classifications) != 1 {
		t.Fatalf("guidance-only target lost existing capability: %+v %v", checked, err)
	}
	if _, err = packs.Publish(ctx, "scanner-pack", "Scanner", `{"guidance":["team/scanning"],"requires":["scanner","registry"]}`, ""); err != nil {
		t.Fatal(err)
	}
	changed, err := publications.Preview(ctx, wow.RoutinePublicationTransition{Kind: "pack", ID: pack.ID, From: 2, To: 3})
	if err != nil || changed.Eligible {
		t.Fatalf("changed requirement became routine: %+v %v", changed, err)
	}
}

func TestRoutineAdoptionEmptyCollectionsPreserveTheirMeaning(t *testing.T) {
	before := EmptyReceipt()
	before.Guidance = []ReceiptBlock{{Name: "guide", BlockID: idstest.N(1), Version: 1}}
	after := before
	after.Guidance = []ReceiptBlock{{Name: "guide", BlockID: idstest.N(1), Version: 2}}
	after.Workflows = nil
	after.Policies = nil
	after.Extensions = nil
	moves, err := routineAdoptionMoves(before, after, nil, nil)
	if err != nil || len(moves) != 1 {
		t.Fatalf("empty collection representation changed selection: %+v %v", moves, err)
	}
}
