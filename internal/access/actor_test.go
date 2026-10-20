package access

import (
	"testing"

	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

// Every credential a request can carry becomes the way the act is recorded,
// and only a person acting directly assents.
func TestAPrincipalIsRecordedByHowItActed(t *testing.T) {
	t.Parallel()
	person := idstest.N(1)
	for _, c := range []struct {
		pr      Principal
		via     string
		assents bool
	}{
		{Principal{ID: person, Kind: "user", Name: "alice", Via: "session"}, "session", true},
		{Principal{ID: person, Kind: "user", Name: "alice", Via: "proxy"}, "header", true},
		{Principal{ID: person, Kind: "user", Name: "alice", Via: "open"}, "open", true},
		{Principal{ID: person, Kind: "user", Name: "alice", Via: "token", Purpose: TokenCLI, Credential: "token:laptop"}, "token-cli:laptop", true},
		{Principal{ID: person, Kind: "user", Name: "alice", Via: "token", Purpose: TokenAgent, Credential: "token:claude"}, "token-agent:claude", false},
		{Principal{ID: person, Kind: "user", Name: "alice", Via: "oauth", DelegationID: idstest.N(9)}, "oauth:" + idstest.N(9).String(), false},
		{Principal{ID: person, Kind: "instrument", Name: "alice via console/c1", Person: "alice", Instrument: "console", Via: "console", ConsoleID: "c1"}, "chat:c1", false},
		{Principal{Kind: "instrument", Name: "run/x", Instrument: "run", Via: "run"}, "run-step", false},
		{Principal{Kind: "anonymous", Name: "anonymous"}, "anonymous", false},
		{Principal{ID: ids.Nil, Kind: "user", Name: "alice", Via: "session"}, "session", true},
	} {
		a := c.pr.Actor()
		if a.Via != c.via || a.Assents() != c.assents || a.Name != c.pr.Name || a.Principal != c.pr.ID {
			t.Errorf("%+v recorded as %+v (assents %v)", c.pr, a, a.Assents())
		}
	}
}

// An actor reads in history by the person today: renamed, disabled,
// removed, or never resolved.
func TestAnActorIsShownAsThePersonIsNow(t *testing.T) {
	t.Parallel()
	p := idstest.N(1)
	system := func(name string) bool { return name == "apptown-system" }
	for _, c := range []struct {
		a    Actor
		now  Person
		want string
	}{
		{Actor{Principal: p, Name: "alice"}, Person{Known: true, Name: "alice"}, "alice"},
		{Actor{Principal: p, Name: "alice"}, Person{Known: true, Name: "alicia"}, "alicia (then alice)"},
		{Actor{Principal: p, Name: "alice via console/c1"}, Person{Known: true, Name: "alicia"}, "alicia (then alice) via console/c1"},
		{Actor{Principal: p, Name: "alice"}, Person{Known: true, Name: "alice", Disabled: true}, "alice (disabled)"},
		{Actor{Principal: p, Name: "alice"}, Person{Known: true, Name: "alice", Removed: true}, "former user (was: alice)"},
		{Actor{Name: "apptown-system"}, Person{}, "apptown-system"},
		{Actor{Name: "run/0193"}, Person{}, "run/0193"},
		{Actor{Name: "bob"}, Person{}, "unrecorded actor (written as: bob)"},
	} {
		if got := c.a.Shown(c.now, system); got != c.want {
			t.Errorf("%+v as %+v: %q, want %q", c.a, c.now, got, c.want)
		}
	}
}
