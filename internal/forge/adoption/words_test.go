package adoption

import (
	"encoding/json"
	"testing"
)

func TestAdoptionKindWords(t *testing.T) {
	for k := AdoptionCreate; k <= AdoptionLegacySnapshot; k++ {
		parsed, err := ParseAdoptionKind(k.String())
		if err != nil || parsed != k {
			t.Fatalf("%s did not round-trip: %v %v", k, parsed, err)
		}
	}
	if _, err := ParseAdoptionKind("drift"); err == nil {
		t.Fatal("an unknown kind was accepted")
	}
	var zero AdoptionKind
	if _, err := zero.Value(); err == nil {
		t.Fatal("the zero kind was stored")
	}
	if AdoptionTarget.Standing() || !AdoptionAdopt.Standing() || !AdoptionLegacySnapshot.Standing() || zero.Standing() {
		t.Fatal("only a target is not a revision a project stands on")
	}
	body, _ := json.Marshal(struct{ K AdoptionKind }{AdoptionLegacySnapshot})
	if string(body) != `{"K":"legacy-snapshot"}` {
		t.Fatalf("json: %s", body)
	}
}
