// Package idstest gives tests readable, stable record ids, so a test can
// still say "change 7" where a fixture needs an id it did not mint.
package idstest

import (
	"testing"

	"flokkit.io/forge/internal/ids"
)

// N is the id a test calls n: a valid version-7 UUID whose time field is n,
// so ids differ by n and order by n (N(7) is 00000000-0007-7000-8000-000000000000).
func N(n uint64) ids.ID {
	var id ids.ID
	for i := 5; i >= 0; i-- {
		id[i] = byte(n)
		n >>= 8
	}
	id[6] = 0x70 // version 7
	id[8] = 0x80 // RFC 9562 variant
	return id
}

// Of is the n of an id N made, so a fake can key its table by small numbers.
func Of(id ids.ID) uint64 {
	var n uint64
	for i := 0; i < 6; i++ {
		n = n<<8 | uint64(id[i])
	}
	return n
}

// Parse is ids.Parse for a literal or a value a test was given: it fails
// the test where the text is no id.
func Parse(t testing.TB, s string) ids.ID {
	t.Helper()
	id, err := ids.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
