package wow

import "testing"

// A child that drops a policy its parent only names has nothing left to pin.
func TestAChildCanDropAnUnavailableInheritedPolicy(t *testing.T) {
	t.Parallel()
	s := New(testDB(t), Dependencies{})
	ctx := t.Context()
	if err := s.SaveLibraryPolicy(ctx, LibraryPolicy{Name: "gate", Rule: "requireStepBefore", With: map[string]any{"uses": "deploy", "requires": "scan"}}, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishPack(ctx, "parent", "", `{"policies":[{"name":"gate"}]}`, ""); err != nil {
		t.Fatal(err)
	}
	// Produce the supported name-only publication using the same owner
	// operations as TestANewPackPinsAPolicyItsLegacyParentOnlyNamed.
	if err := s.DeleteLibraryPolicy(ctx, "gate", "alice"); err != nil {
		t.Fatal(err)
	}
	if recuts, err := s.CutPacksOnPinChange(ctx); err != nil || len(recuts) != 1 || recuts[0].Version != 2 {
		t.Fatalf("parent recut: %+v %v", recuts, err)
	}
	if _, err := s.PublishPack(ctx, "child", "", `{"includes":"parent@2","drops":[{"item":"gate","reason":"We do not use this policy"}]}`, ""); err != nil {
		t.Fatalf("a dropped policy must not need a library pin: %v", err)
	}
	setup, err := PackSetupIn(ctx, s.db, "child", 1)
	if err != nil || len(setup.Policies) != 0 {
		t.Fatalf("child must install no policy: %+v %v", setup, err)
	}
}
