package adoption

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestTargetRecorderOwnsReceiptAndCandidateIdentity(t *testing.T) {
	t.Parallel()
	db, _, candidate := validationCandidate(t)
	candidate.RequiredCapabilities = []string{"registry"}
	input := Target{ProjectID: candidate.ProjectID, ChangeID: idstest.N(19), Candidate: candidate, Actor: "alice"}
	var target ScopedTarget
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		var err error
		target, err = recordTargetIn(context.Background(), tx, input, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var decoded Candidate
	if err := json.Unmarshal(target.Candidate, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Digest != Digest(decoded.Receipt) || len(decoded.RequiredCapabilities) != 1 || decoded.RequiredCapabilities[0] != "registry" {
		t.Fatalf("target document changed: %+v", decoded)
	}
	if r, err := ByIDIn(t.Context(), db, target.ID); err != nil || r.ProjectID != candidate.ProjectID || r.Revision != 0 || r.ChangeID != idstest.N(19) || r.Kind != AdoptionTarget || r.Digest != candidate.Digest || r.CreatedBy != "alice" {
		t.Fatalf("stored target: %+v %v", r, err)
	}
	if cur := currentOf(t, db, candidate.ProjectID); cur.Revision != 0 {
		t.Fatalf("a target moved the current adoption: %+v", cur)
	}
	input.ChangeID = idstest.N(20)
	input.Candidate.Receipt.Guidance[0].Version = 3
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		_, err := recordTargetIn(context.Background(), tx, input, time.Now())
		return err
	}); err == nil {
		t.Fatal("mismatched target digest was stored")
	}
	if revs := revisionsOf(t, db, candidate.ProjectID); len(revs) != 1 {
		t.Fatalf("mismatched target stored: %+v", revs)
	}
}

func TestTargetCandidateMustMatchItsImmutableReceipt(t *testing.T) {
	t.Parallel()
	receipt := EmptyReceipt()
	candidate := Candidate{ExpectedRevision: 3, Receipt: receipt, Digest: Digest(receipt)}
	target := RecordedRevision{ProjectID: idstest.N(7), ChangeID: idstest.N(9), Kind: AdoptionTarget, Receipt: receipt, Digest: Digest(receipt)}
	if err := target.checkTarget(idstest.N(7), idstest.N(9), 3, candidate); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*RecordedRevision, *Candidate){
		func(r *RecordedRevision, c *Candidate) { r.ProjectID = idstest.N(999999) },
		func(r *RecordedRevision, c *Candidate) { r.ChangeID = idstest.N(10) },
		func(r *RecordedRevision, c *Candidate) { r.Kind = AdoptionEdit },
		func(r *RecordedRevision, c *Candidate) { r.Digest = "invented" },
		func(r *RecordedRevision, c *Candidate) { c.ExpectedRevision++ },
		func(r *RecordedRevision, c *Candidate) { c.Receipt.Pack = "other"; c.Digest = Digest(c.Receipt) },
		func(r *RecordedRevision, c *Candidate) { c.Digest = "invented" },
	} {
		r, c := target, candidate
		mutate(&r, &c)
		if err := r.checkTarget(idstest.N(7), idstest.N(9), 3, c); err == nil {
			t.Fatal("altered target accepted")
		}
	}
}
