package secret

import (
	"encoding/hex"
	"testing"
)

// A need credential derived for one owner is the same every time it is
// asked for, unrelated to another owner's or another name's, a 64-hex
// value safe in a URL, and made from this forge's key: another forge (its
// own key) derives another. Nothing is stored.
func TestADerivedValueIsTheOwnersAloneAndTheSameEachTime(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	v := NewVault(db)
	derive := func(v *Vault, scope, name string) string {
		t.Helper()
		got, err := v.Derived(t.Context(), scope, name)
		if err != nil {
			t.Fatal(err)
		}
		if raw, err := hex.DecodeString(got); err != nil || len(raw) != 32 {
			t.Fatalf("%s %s: %q is not 32 bytes as hex", scope, name, got)
		}
		return got
	}
	first := derive(v, "need:deployment:a", "postgres.password")
	if again := derive(NewVault(db), "need:deployment:a", "postgres.password"); again != first {
		t.Fatalf("the same owner's value changed: %s then %s", first, again)
	}
	seen := map[string]string{first: "need:deployment:a postgres.password"}
	for _, other := range [][2]string{
		{"need:deployment:b", "postgres.password"},
		{"need:deployment:a", "mariadb.password"},
		{"need:preview:a", "postgres.password"},
		// The scope and the name are kept apart: moving a character from
		// one to the other is another input.
		{"need:deployment:apostgres", ".password"},
	} {
		got := derive(v, other[0], other[1])
		if was, ok := seen[got]; ok {
			t.Fatalf("%s %s gives the value of %s", other[0], other[1], was)
		}
		seen[got] = other[0] + " " + other[1]
	}
	if elsewhere := derive(NewVault(testDB(t)), "need:deployment:a", "postgres.password"); elsewhere == first {
		t.Fatal("another forge, with its own key, derives this AppTown's value")
	}
	if values, err := v.Current(t.Context()); err != nil || len(values) != 0 {
		t.Fatalf("deriving stored something: %d values %v", len(values), err)
	}
}
