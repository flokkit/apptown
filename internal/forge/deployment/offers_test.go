package deployment

import (
	"errors"
	"strings"
	"testing"
)

func TestOfferCapsRefuseInWords(t *testing.T) {
	t.Parallel()
	target := &Target{Name: "syd"}
	o := &Offer{Caps: OfferCaps{Audiences: []string{"team"}, Data: []string{"synthetic"}, MaxStanding: 1}}
	if why := o.Permits(target, "app", Attributes{Audience: "public", DataClass: "synthetic"}, 0); !strings.Contains(why, "audiences team, not public") {
		t.Fatalf("audience: %q", why)
	}
	if why := o.Permits(target, "app", Attributes{Audience: "team", DataClass: "production"}, 0); !strings.Contains(why, "synthetic data, not production") {
		t.Fatalf("data: %q", why)
	}
	if why := o.Permits(target, "app", Attributes{Audience: "team", DataClass: "synthetic"}, 1); !strings.Contains(why, "1 standing deployment(s) per project; app already has 1") {
		t.Fatalf("cap: %q", why)
	}
	if why := o.Permits(target, "app", Attributes{Audience: "team", DataClass: "synthetic"}, 0); why != "" {
		t.Fatalf("within caps: %q", why)
	}
	if why := (&Offer{Implicit: true}).Permits(target, "app", Attributes{Audience: "public", DataClass: "production"}, 9); why != "" {
		t.Fatalf("an offer by construction has no caps: %q", why)
	}
	if err := ValidateCaps(OfferCaps{Audiences: []string{"everyone"}}); err == nil {
		t.Fatal("an unknown audience is refused")
	}
	if err := ValidateCaps(OfferCaps{Data: []string{"prod"}}); err == nil {
		t.Fatal("an unknown data class is refused")
	}
	if err := ValidateCaps(OfferCaps{Audiences: []string{"partners"}, Data: []string{"production"}}); err != nil {
		t.Fatalf("known words pass: %v", err)
	}
}

func TestRetentionSettlesOnce(t *testing.T) {
	t.Parallel()
	if got, err := RetainedOpen.Settle(RetainedReleased); err != nil || got != RetainedReleased {
		t.Fatalf("open to released: %s %v", got, err)
	}
	for _, from := range []RetentionState{RetainedReleased, RetainedKept} {
		if _, err := from.Settle(RetainedReleased); !errors.Is(err, ErrRetainedSettled) {
			t.Fatalf("%s never settles again: %v", from, err)
		}
	}
	// Kept is history: nothing is newly settled kept.
	if _, err := RetainedOpen.Settle(RetainedKept); !errors.Is(err, ErrRetentionState) {
		t.Fatalf("kept is no longer a settlement: %v", err)
	}
	if _, err := RetainedOpen.Settle(RetainedOpen); !errors.Is(err, ErrRetentionState) {
		t.Fatalf("retained is not a disposition: %v", err)
	}
	var s RetentionState
	if err := s.Scan("kept"); err != nil || s != RetainedKept {
		t.Fatalf("scan: %s %v", s, err)
	}
	if err := s.Scan("deleted"); !errors.Is(err, ErrRetentionState) {
		t.Fatalf("an unknown stored word is refused: %v", err)
	}
}
