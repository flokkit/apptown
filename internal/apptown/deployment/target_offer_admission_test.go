package deployment

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

type offerGroupDirectory struct {
	kind string
	err  error
	seen []string
}

func (g *offerGroupDirectory) GroupKind(_ context.Context, name string) (string, error) {
	g.seen = append(g.seen, name)
	return g.kind, g.err
}

// Direct callers receive the same group admission and attributed persistence
// as HTTP callers; a failed group read cannot overwrite an existing offer.
func TestCheckedOfferAdmitsGroupsAndAttributesTheCurrentCaller(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	targets := NewTargets(db, noRetirement{})
	target, err := targets.Put(t.Context(), &Target{Name: "shared", Kind: KindDockerHost, Origin: OriginAttached, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	groups := &offerGroupDirectory{kind: "team"}
	authority := &requestAuthority{}
	service := &Service{Targets: targets, authority: authority, groups: groups}
	if saved, err := service.OfferTarget(t.Context(), target.Name, " team ", OfferCaps{MaxStanding: 2}); err != nil || saved.ID != target.ID {
		t.Fatalf("admitted offer: %v %v", saved, err)
	}
	before := targetOffers(t, targets, target.ID)
	if len(before) != 1 || before[0].Group != "team" || before[0].CreatedBy != "alice" || before[0].Caps.MaxStanding != 2 || !reflect.DeepEqual(groups.seen, []string{"team"}) {
		t.Fatalf("offer or group admission: %+v %+v", before, groups.seen)
	}
	receipts := operationReceipts(t, db, "target.offered", ids.Nil)
	if len(receipts) != 1 || receipts[0].Actor != "alice" || receipts[0].Subject != target.Name {
		t.Fatalf("attribution: %+v", receipts)
	}
	readErr := errors.New("group directory read failed")
	for _, failure := range []struct {
		name, group, kind string
		err               error
	}{
		{name: "missing", group: "team"},
		{name: "unreadable", group: "team", err: readErr},
		{name: "blank", group: " "},
	} {
		t.Run(failure.name, func(t *testing.T) {
			groups.kind, groups.err = failure.kind, failure.err
			_, err := service.OfferTarget(t.Context(), target.Name, failure.group, OfferCaps{MaxStanding: 9})
			if failure.err != nil {
				if !errors.Is(err, readErr) {
					t.Fatalf("read failure: %v", err)
				}
			} else {
				want := fault.NotFound
				if failure.name == "blank" {
					want = fault.Unprocessable
				}
				if fault.KindOf(err) != want {
					t.Fatalf("group refusal: %v", err)
				}
			}
			if after := targetOffers(t, targets, target.ID); !reflect.DeepEqual(before, after) {
				t.Fatalf("refusal changed offer: %+v", after)
			}
			if after := operationReceipts(t, db, "target.offered", ids.Nil); !reflect.DeepEqual(receipts, after) {
				t.Fatalf("refusal changed receipts: %+v", after)
			}
		})
	}
	service.groups = nil
	if _, err := service.OfferTarget(t.Context(), target.Name, "*", OfferCaps{MaxStanding: 3}); err != nil {
		t.Fatalf("wildcard requires no group lookup: %v", err)
	}
	authority.deny = errors.New("grant revoked")
	before = targetOffers(t, targets, target.ID)
	if _, err := service.OfferTarget(t.Context(), target.Name, "*", OfferCaps{}); !errors.Is(err, authority.deny) {
		t.Fatalf("current authority: %v", err)
	}
	if after := targetOffers(t, targets, target.ID); !reflect.DeepEqual(before, after) {
		t.Fatalf("revocation changed offers: %+v", after)
	}
}
