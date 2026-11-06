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

func TestCreationIntentBindsDeploymentTarget(t *testing.T) {
	hashes := map[string]bool{}
	for _, target := range []string{"", "apps", "none"} {
		hash, err := creationHash(Draft{Slug: "ledger", Target: target}, "team")
		if err != nil {
			t.Fatal(err)
		}
		if hashes[hash] {
			t.Fatalf("target %q reused a different placement's intent", target)
		}
		hashes[hash] = true
	}
}
