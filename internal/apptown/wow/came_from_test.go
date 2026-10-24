package wow

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/wow/internal/wowsql"
	"github.com/flokkit/apptown/stock"
)

// Every version records where it came from: the stock sync's versions name
// the shipped update they came in, and what is authored here says so.
//
// Kills: a stock version left unlabelled or labelled as authored, an authored
// one labelled as stock, or the shipped name not carried.
func TestEveryVersionSaysWhereItCameFrom(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	s := New(db, Dependencies{Stock: stock.FS, ToolLines: stock.Tools(), Shipped: "v9.9.9"})
	for _, c := range []SyncCounts{s.SyncStockWorkflows(t.Context()), s.SyncStockGuidance(t.Context()), s.SyncStockPolicies(t.Context()), s.SyncStockPacks(t.Context())} {
		if c.Errors != 0 {
			t.Fatalf("%s: %+v", c.Kind, c)
		}
	}
	const shipped = "AppTown v9.9.9"
	pubs, err := s.packs.PublicationsOf(t.Context(), "go-small")
	if err != nil || pubs[0].CameFrom != shipped {
		t.Fatalf("a stock pack version: %+v %v", pubs, err)
	}
	b, err := s.BlockByName(t.Context(), "prose/no-claude-speak")
	if err != nil {
		t.Fatal(err)
	}
	if vs, err := s.BlockVersions(t.Context(), b.ID); err != nil || vs[0].CameFrom != shipped {
		t.Fatalf("a stock block version: %+v %v", vs, err)
	}
	// The library's own query for a workflow version: its reads do not list
	// where a workflow's versions came from.
	if wf, err := wowsql.New(db.SQL()).WorkflowVersion(t.Context(), wowsql.WorkflowVersionParams{Name: "implement", Version: 1}); err != nil || wf.CameFrom != shipped {
		t.Fatalf("a stock workflow version: %q %v", wf.CameFrom, err)
	}

	mine, err := s.CreateBlock(t.Context(), "team/ours", "body", "", "someone")
	if err != nil {
		t.Fatal(err)
	}
	if vs, err := s.BlockVersions(t.Context(), mine.ID); err != nil || vs[0].CameFrom != CameFromHere {
		t.Fatalf("an authored block version: %+v %v", vs, err)
	}
	if _, err := s.packs.Publish(t.Context(), "ours", "", mustJSON(PackSpec{Includes: "go-small"}), ""); err != nil {
		t.Fatal(err)
	}
	if pubs, err := s.packs.PublicationsOf(t.Context(), "ours"); err != nil || pubs[0].CameFrom != CameFromHere {
		t.Fatalf("an authored pack version: %+v %v", pubs, err)
	}
}
