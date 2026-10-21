package workflow

import "testing"

// A shipped change carries no code of its own only when every run that
// worked it was a landing and nothing was merged for it: one working run, or
// a definition that cannot be read, means it may have a branch; a merge
// means it had code; a change no run has worked has decided nothing; and an
// open change may yet land what a chat published to its branch.
func TestCarriesNoCodeOnlyWhenEveryRootRunIsALanding(t *testing.T) {
	t.Parallel()
	run := func(def string) *Run { return &Run{DefinitionJSON: def} }
	landing, working, record := run(`{"role":"landing"}`), run(`{"role":"working"}`), run(`{"role":"record"}`)
	for _, c := range []struct {
		name  string
		roots []*Run
		want  bool
	}{
		{"no runs", nil, false},
		{"one landing", []*Run{landing}, true},
		{"landings only", []*Run{landing, landing}, true},
		{"worked then landed", []*Run{working, landing}, false},
		{"a record", []*Run{record}, false},
		{"no role is working", []*Run{run(`{}`)}, false},
		{"unreadable", []*Run{landing, run(`{`)}, false},
	} {
		if got := CarriesNoCode(&Change{State: ChangeShipped}, c.roots); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if CarriesNoCode(&Change{State: ChangeShipped, LandedSHA: "abc"}, []*Run{landing}) {
		t.Error("a change a landing merged for carries code")
	}
	if CarriesNoCode(&Change{State: ChangeLanding}, []*Run{landing}) {
		t.Error("an open change was judged before it shipped")
	}
}
