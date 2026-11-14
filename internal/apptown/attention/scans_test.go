package attention

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/fix"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A failed try's reason can be a whole tool output. Every Attention surface
// shows the item's detail as it is, so the item carries the first line and
// links to the change that holds the rest.
func TestAStuckQueueItemIsOneLineAndOpensItsChange(t *testing.T) {
	t.Parallel()
	change := idstest.N(7)
	reason := "update: part web: npm listing failed (exit 1): npm notice\nnpm notice New major version of npm available!\n    at walk ([eval]:8:34)\n"
	state := workflow.QueueStatus{Title: "Dependency updates", Queue: "updates", State: "stuck", Words: "stuck since 13 Nov", Reason: reason, ChangeID: change, Packages: []fix.Move{{Package: "vite", From: "5.0.0", To: "5.1.0"}}}
	item := queueItem(idstest.N(1), "Tidewell", "/projects/tidewell#janitor", state)
	if item.Detail != "update: part web: npm listing failed (exit 1): npm notice · moved: vite 5.0.0 → 5.1.0" {
		t.Fatalf("detail %q", item.Detail)
	}
	if item.Href != "/changes/"+change.String() {
		t.Fatalf("href %q", item.Href)
	}
	state.ChangeID = ids.Nil
	if item := queueItem(idstest.N(1), "Tidewell", "/projects/tidewell#janitor", state); item.Href != "/projects/tidewell#janitor" {
		t.Fatalf("a queue with no change lost its project link: %q", item.Href)
	}
}

// The scans owner knows which case applies; the detail says it and what would
// answer it, instead of one sentence for every case.
func TestANotKnownScanSaysItsCase(t *testing.T) {
	t.Parallel()
	for reason, want := range map[string]string{
		"gitleaks gave no result for the current code (5c406e206353)": "Gitleaks gave no result for the current code (5c406e206353). A new scan would answer it.",
		"osv: the database could not be read.\nstack":                 "Osv: the database could not be read. A new scan would answer it.",
		"": "AppTown has no complete current scan. A new scan would answer it.",
	} {
		if got := scanDetail(reason); got != want || strings.Contains(got, "\n") {
			t.Fatalf("%q became %q", reason, got)
		}
	}
}
