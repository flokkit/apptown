package wow

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/wow/internal/wowsql"
	"github.com/flokkit/apptown/stock"
)

// Every version records where it came from: the stock sync's versions are
// the built-in source's, signed as shipped with the release that built them;
// what is authored here is this AppTown's, with no signer.
//
// Kills: a stock version without a source or recorded as authored, an
// authored one recorded as stock, or the shipped name not carried.
func TestEveryVersionSaysItsSourceAndSigner(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	s := New(db, Dependencies{Stock: stock.FS, ToolLines: stock.Versions(), Shipped: "v9.9.9"})
	for _, c := range []SyncCounts{s.SyncStockWorkflows(t.Context()), s.SyncStockGuidance(t.Context()), s.SyncStockPolicies(t.Context()), s.SyncStockPacks(t.Context())} {
		if c.Errors != 0 {
			t.Fatalf("%s: %+v", c.Kind, c)
		}
	}
	shipped := Origin{Source: SourceFlokkit, Signer: "shipped with AppTown v9.9.9, unsigned"}
	pubs, err := s.packs.PublicationsOf(t.Context(), "go")
	if err != nil || pubs[0].Origin != shipped {
		t.Fatalf("a stock pack version: %+v %v", pubs, err)
	}
	b, err := s.BlockByName(t.Context(), "prose/no-claude-speak")
	if err != nil {
		t.Fatal(err)
	}
	if vs, err := s.BlockVersions(t.Context(), b.ID); err != nil || vs[0].Origin != shipped {
		t.Fatalf("a stock block version: %+v %v", vs, err)
	}
	// The library's own query for a workflow version: its reads do not list
	// where a workflow's versions came from.
	if wf, err := wowsql.New(db.SQL()).WorkflowVersion(t.Context(), wowsql.WorkflowVersionParams{Name: "implement", Version: 1}); err != nil || (Origin{Source: wf.Source, Signer: wf.Signer}) != shipped {
		t.Fatalf("a stock workflow version: %q %q %v", wf.Source, wf.Signer, err)
	}

	mine, err := s.CreateBlock(t.Context(), "team/ours", "body", "", "someone")
	if err != nil {
		t.Fatal(err)
	}
	if vs, err := s.BlockVersions(t.Context(), mine.ID); err != nil || vs[0].Origin != AuthoredHere {
		t.Fatalf("an authored block version: %+v %v", vs, err)
	}
	if _, err := s.packs.Publish(t.Context(), "ours", "", `{"guidance":["build/registries"]}`, ""); err != nil {
		t.Fatal(err)
	}
	if pubs, err := s.packs.PublicationsOf(t.Context(), "ours"); err != nil || pubs[0].Origin != AuthoredHere {
		t.Fatalf("an authored pack version: %+v %v", pubs, err)
	}
}
