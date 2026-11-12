package adoption

import (
	"encoding/json"
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"testing"
	"time"
)

func TestRoutineTargetChecksImmutableAssessedCandidate(t *testing.T) {
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
	ways := wow.New(db, wow.Dependencies{})
	service := New(db, Dependencies{Ways: ways})
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
	req := Request{Additions: []string{"team/first@2", "team/second@2"}}
	if _, err := service.PrepareAssessed(ctx, pid, req, candidate.ExpectedRevision-1, candidate.Digest); err == nil {
		t.Fatal("moved adoption revision was accepted")
	}
	if _, err := service.PrepareAssessed(ctx, pid, req, candidate.ExpectedRevision, candidate.Digest+"changed"); err == nil {
		t.Fatal("a different assessed candidate was accepted")
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
	got, err := check(target.Candidate)
	if err != nil || got.TargetID != target.ID || got.CandidateDigest == "" {
		t.Fatalf("unclassified target: %+v %v", got, err)
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
}

func TestAdoptionFingerprintIncludesMembershipVersions(t *testing.T) {
	c := Candidate{Packs: []PackPick{{Pack: "team", Version: 1}}, Receipt: EmptyReceipt()}
	before, err := c.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	c.Packs[0].Version = 2
	after, err := c.Fingerprint()
	if err != nil || before == after {
		t.Fatalf("different publication got the same fingerprint: %s %s %v", before, after, err)
	}
}
