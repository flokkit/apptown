package fix

import "testing"

// A registration supplies all declaration rules. The alias exists only in this
// serial test; it proves parsing and targeting do not recognize a kind by name.
func TestKindRegistrationDefinesDeclarations(t *testing.T) {
	kinds["test-kind"] = kinds["update"]
	defer delete(kinds, "test-kind")
	ds, err := Parse(`[{"kind":"test-kind","ecosystem":"npm"}]`)
	if err != nil || len(ds) != 1 || !SupportsTargets(ds[0]) {
		t.Fatalf("registered kind refused: %+v %v", ds, err)
	}
	k := kinds["test-kind"]
	k.DeclarationFields = []DeclarationField{{"ecosystem", true}}
	kinds["test-kind"] = k
	if _, err := Parse(`[{"kind":"test-kind","ecosystem":"npm","files":["package.json"]}]`); err == nil {
		t.Fatal("declaration accepted a field its kind does not declare")
	}
	if _, err := Parse(`[{"kind":"test-kind","ecosystem":"npm"}]`); err != nil {
		t.Fatalf("optional registered field required: %v", err)
	}
	if _, err := Parse(`[{"kind":"test-kind"}]`); err == nil {
		t.Fatal("required registered field omitted")
	}
	if _, err := Parse(`[{"kind":"unknown","ecosystem":"npm"}]`); err == nil {
		t.Fatal("unregistered kind accepted")
	}
}

func TestAdoptIsDeclaredByThePublication(t *testing.T) {
	kind, ok := Lookup("adopt")
	if !ok || kind.Action != AdoptAction || kind.Classifier != AdoptClassifier || !kind.PublishedDeclaration {
		t.Fatalf("adopt registration: %+v", kind)
	}
	if _, err := Parse(`[{"kind":"adopt"}]`); err == nil {
		t.Fatal("guidance manufactured a pack adoption declaration")
	}
}
