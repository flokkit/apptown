package adoption

import (
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/ids/idstest"
)

func objectCandidate(a Adoption) Candidate {
	r := EmptyReceipt()
	r.Packs = []string{"selected"}
	r.Guidance = []ReceiptBlock{{Name: "guide", BlockID: idstest.N(12), Version: 3}}
	return Candidate{ProjectID: a.ProjectID, ExpectedRevision: a.Revision, Receipt: r}
}

func TestAdoptionPrepareRefusesMissingCapabilitiesWithoutMutation(t *testing.T) {
	t.Parallel()
	a := Adoption{ProjectID: idstest.N(7), Revision: 3, Drift: "review"}
	before := a
	c := objectCandidate(a)
	c.RequiredCapabilities = []string{"registry", "git", "registry"}
	refusal := errors.New("registry not attached")
	calls := 0
	if _, err := a.prepare(c, func(required []string) error {
		calls++
		if !reflect.DeepEqual(required, []string{"git", "registry"}) {
			t.Fatalf("requirements: %v", required)
		}
		required[0] = "mutated callback input"
		return refusal
	}); !errors.Is(err, refusal) || calls != 1 {
		t.Fatalf("missing capability: %v, calls=%d", err, calls)
	}
	if a != before || c.RequiredCapabilities[1] != "git" {
		t.Fatal("refusal mutated current state or candidate")
	}
	if _, err := a.prepare(c, nil); !errors.Is(err, errCapabilityUnavailable) {
		t.Fatalf("missing locker: %v", err)
	}
	c.RequiredCapabilities = nil
	if _, err := a.prepare(c, nil); err != nil {
		t.Fatalf("candidate without requirements: %v", err)
	}
	c.Problems = []string{"conflicting pins"}
	if _, err := a.prepare(c, nil); err == nil {
		t.Fatal("unresolved candidate was admitted")
	}
}

func TestAdoptionPlanOwnsItsContentAndAdvanceChecksScopeAndRevision(t *testing.T) {
	t.Parallel()
	a := Adoption{ProjectID: idstest.N(7), Revision: 3, Drift: "review"}
	c := objectCandidate(a)
	c.Packs = []PackPick{{Pack: "selected", Version: 2}}
	c.Installation = Installation{Policies: []Policy{{Name: "needed", Rule: "requireCapabilityBound", With: map[string]any{"capability": "registry"}}}, Extensions: []string{"enabled"}}
	plan, err := a.prepare(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Receipt.Guidance[0].Version = 99
	c.Packs[0].Version = 99
	c.Installation.Policies[0].With["capability"] = "changed"
	c.Installation.Extensions[0] = "changed"
	next, event, err := a.advance(plan, 3)
	if err != nil || next.Revision != 4 || next.Drift != "" || !event.Recorded || plan.receipt.Guidance[0].Version != 3 || plan.memberships[0].Version != 2 || string(plan.policies[0].config) != `{"capability":"registry"}` || plan.extensions[0] != "enabled" {
		t.Fatalf("advance: %+v %+v %v", next, event, err)
	}
	if a.Revision != 3 || a.Drift != "review" {
		t.Fatal("advancement changed the loaded value before persistence")
	}
	for _, current := range []Adoption{{ProjectID: idstest.N(7), Revision: 4}, {ProjectID: idstest.N(8), Revision: 3}} {
		before := current
		if _, _, err := current.advance(plan, 3); !errors.Is(err, ErrPreparedRevision) {
			t.Fatalf("invalid plan admitted: %+v %v", current, err)
		}
		if current != before {
			t.Fatal("refused advancement mutated current")
		}
	}
	if _, _, err := a.advance(adoptionPlan{}, 3); !errors.Is(err, ErrPreparedRevision) {
		t.Fatalf("unprepared plan: %v", err)
	}
	c.ProjectID = idstest.N(8)
	if _, err := a.prepare(c, nil); err == nil {
		t.Fatal("another project's candidate admitted")
	}
}

func TestAdoptionReviewSettlesDriftWithoutNewHistory(t *testing.T) {
	t.Parallel()
	a := Adoption{ProjectID: idstest.N(7), Revision: 3, Drift: "review"}
	r := EmptyReceipt()
	prior := &RecordedRevision{ProjectID: idstest.N(7), Revision: 3, Digest: Digest(r), Receipt: r}
	plan, err := a.prepareRecord(r, AdoptionEdit, "reviewed", prior)
	if err != nil {
		t.Fatal(err)
	}
	next, event, err := a.advance(plan, 3)
	if err != nil || event.Recorded || next.Revision != 3 || next.Drift != "" {
		t.Fatalf("review: %+v %+v %v", next, event, err)
	}
	// Explicit adoption retains its separate intent, even for equal content.
	candidate := objectCandidate(a)
	candidate.Receipt = r
	plan, err = a.prepare(candidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	next, event, err = a.advance(plan, 3)
	if err != nil || !event.Recorded || next.Revision != 4 {
		t.Fatalf("explicit adopt: %+v %+v %v", next, event, err)
	}
	if _, err := a.prepareRecord(r, AdoptionEdit, "missing receipt", nil); !errors.Is(err, ErrRevisionUnavailable) {
		t.Fatalf("missing history: %v", err)
	}
}

func TestAdoptionCannotAdvanceToTargetOrOverflow(t *testing.T) {
	t.Parallel()
	for _, revision := range []int64{-1, math.MaxInt64} {
		a := Adoption{ProjectID: idstest.N(7), Revision: revision}
		plan, err := a.prepare(objectCandidate(a), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := a.advance(plan, revision); err == nil {
			t.Fatalf("invalid next revision from %d", revision)
		}
	}
	a := Adoption{ProjectID: idstest.N(7)}
	if _, err := a.prepareRecord(EmptyReceipt(), adoptionTarget, "target", nil); !errors.Is(err, errAdoptionKind) {
		t.Fatalf("target became current: %v", err)
	}
}

func TestAdoptionNoteDriftPreservesSelection(t *testing.T) {
	t.Parallel()
	a := Adoption{ProjectID: idstest.N(7), Revision: 3}
	for _, note := range []string{"exact review text", ""} {
		event := a.noteDrift(note)
		if a.ProjectID != idstest.N(7) || a.Revision != 3 || a.Drift != note || event.Drift != note || event.Recorded {
			t.Fatalf("drift changed selection: %+v %+v", a, event)
		}
	}
}
