package repositorylayout

import (
	"testing"

	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestReferenceTranslationUsesOwnedPathBoundaries(t *testing.T) {
	owner := idstest.N(1)
	refs := []Reference{{Owner: owner, Before: "/old/hub", After: "/new/id"}}
	for _, tc := range []struct {
		in, want string
		matched  bool
	}{{"/old/hub", "/new/id", true}, {"/old/hub/sub/file", "/new/id/sub/file", true}, {"/old/hub-neighbor", "/old/hub-neighbor", false}} {
		got, matched, err := Translate(refs, owner, tc.in)
		if err != nil || got != tc.want || matched != tc.matched {
			t.Fatalf("%q: %q %v %v", tc.in, got, matched, err)
		}
	}
	if _, _, err := Translate(refs, idstest.N(2), "/old/hub"); err == nil {
		t.Fatal("translated another project's path")
	}
	if _, _, err := Translate(refs, owner, "/old/hub/../neighbor"); err == nil {
		t.Fatal("accepted unclean reference")
	}
	if _, _, err := Translate(refs, owner, "worker-key"); err == nil {
		t.Fatal("interpreted backend key as local path")
	}
	refs = append(refs, Reference{Owner: owner, Before: "/old/hub/sub", After: "/new/elsewhere"})
	if _, _, err := Translate(refs, owner, "/old/hub/sub/file"); err == nil {
		t.Fatal("silently selected overlapping mapping")
	}
}
