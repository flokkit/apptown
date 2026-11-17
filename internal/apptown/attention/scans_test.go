package attention

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A failed try's reason can be a whole tool output. Every Attention surface
// shows the item's detail as it is, so the item carries the first line and
// links to the change that holds the rest.
func TestADecisionQueueItemIsOneLineAndOpensItsChange(t *testing.T) {
	t.Parallel()
	change := idstest.N(7)
	reason := "update: part web: npm listing failed (exit 1): npm notice\nnpm notice New major version of npm available!\n    at walk ([eval]:8:34)\n"
	state := workflow.QueueStatus{Title: "Dependency updates", Queue: "updates", State: "decide", Words: "needs you", Reason: reason, ChangeID: change, Packages: []workflow.PackageMove{{Package: "vite", From: "5.0.0", To: "5.1.0"}}}
	item := queueItem(idstest.N(1), "Tidewell", "/maintenance/standards/projects/tidewell", state)
	if item.Detail != "update: part web: npm listing failed (exit 1): npm notice · moved: vite 5.0.0 → 5.1.0" {
		t.Fatalf("detail %q", item.Detail)
	}
	if item.Href != "/changes/"+change.String() {
		t.Fatalf("href %q", item.Href)
	}
	state.ChangeID = ids.Nil
	if item := queueItem(idstest.N(1), "Tidewell", "/maintenance/standards/projects/tidewell", state); item.Href != "/maintenance/standards/projects/tidewell" {
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

// Only a failed or overdue scan needs a person. Code or images newer than the
// last scan wait for the next scheduled scan, and a project that schedules no
// scan chose that; neither is an attention item.
func TestOnlyFailedOrOverdueScansNeedAttention(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		scans workflow.MainScans
		item  bool
	}{
		"newer than the last scan": {workflow.MainScans{Reason: "the image running on main has not been scanned since it was deployed"}, false},
		"no scan scheduled":        {workflow.MainScans{Unscheduled: true, Reason: "its packs schedule no scan"}, false},
		"scanner failed":           {workflow.MainScans{Failed: true, Reason: "osv: the database could not be read"}, true},
		"overdue":                  {workflow.MainScans{Stale: true, Reason: "the last scan was on 1 Nov, more than three days ago"}, true},
		"overdue but unscheduled":  {workflow.MainScans{Stale: true, Unscheduled: true}, false},
		"known":                    {workflow.MainScans{Known: true}, false},
	} {
		got := scanSnapshot(workflow.ScanAttentionProject{ProjectID: idstest.N(1), Slug: "p", Name: "P", Reaches: true, Scans: c.scans})
		if got.Href != "/maintenance/standards/projects/p" {
			t.Fatalf("scan link: %q", got.Href)
		}
		if c.item && got.Items[0].Href != "/maintenance/standards/projects/p" {
			t.Fatalf("scan item link: %q", got.Items[0].Href)
		}
		if (len(got.Items) == 1) != c.item || len(got.Items) > 1 {
			t.Errorf("%s: items %+v", name, got.Items)
		}
	}
}

func TestOnlyDecisionsAndStoppedQueuesNeedAttention(t *testing.T) {
	for _, state := range []string{"retrying", "decide", "stopped"} {
		got := scanSnapshot(workflow.ScanAttentionProject{ProjectID: idstest.N(1), Reaches: true, Scans: workflow.MainScans{Known: true}, Queues: []workflow.QueueStatus{{State: state}}})
		want := 1
		if state == "retrying" {
			want = 0
		}
		if len(got.Items) != want {
			t.Fatalf("%s: %+v", state, got)
		}
	}
}
