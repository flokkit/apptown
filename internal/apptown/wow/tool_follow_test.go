package wow

import (
	"testing"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/stock"
)

// An existing child may override services before its parent declares tool
// lines. A later parent recommendation must record that child's refusal,
// without blocking the parent or changing the child's publication.
func TestAChildThatContradictsItsParentsToolLinesIsRefusedItsFollow(t *testing.T) {
	s := New(testDB(t), Dependencies{ToolLines: stock.Tools()})
	packs := s.packs
	for _, p := range []struct{ name, spec string }{
		{"base", `{}`},
		{"team", `{"includes":"base","services":[{"name":"postgres","image":"postgres:15"}]}`},
		{"base", `{"tools":{"postgres":"16"}}`},
	} {
		if _, err := packs.Publish(t.Context(), p.name, "", p.spec, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := packs.Recommend(t.Context(), "base", 2, "alice", ids.Nil); err != nil {
		t.Fatalf("a conflicting child must not block its parent's recommendation: %v", err)
	}
	base, err := packs.History(t.Context(), "base")
	if err != nil || base.Recommended != 2 {
		t.Fatalf("base: %+v %v", base, err)
	}
	team, err := packs.History(t.Context(), "team")
	if err != nil || team.Latest != 1 || team.FollowRefused == "" {
		t.Fatalf("team: %+v %v", team, err)
	}
}
