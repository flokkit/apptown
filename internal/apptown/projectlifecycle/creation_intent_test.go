package projectlifecycle

import "testing"

func TestCreationIntentPreservesServiceSelectionPresence(t *testing.T) {
	omitted, err := normalizeCreation(Draft{Slug: "ledger", Setup: &CreationSetup{Origin: "empty"}})
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := normalizeCreation(Draft{Slug: "ledger", Setup: &CreationSetup{Origin: "empty", Services: []CreationService{}}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := creationHash(omitted, "team")
	if err != nil {
		t.Fatal(err)
	}
	b, err := creationHash(explicit, "team")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("omitted services and an explicit empty selection have the same replay hash")
	}
}
