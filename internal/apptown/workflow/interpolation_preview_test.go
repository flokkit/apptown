package workflow

import (
	"context"
	"testing"
	"time"
)

// ${{preview.url}} and ${{preview.host}} read the run's latest recorded
// preview: the reviewer opens its pages by sending that Host to APPTOWN_URL.
// One run, previews recorded in turn, so each read sees the newest.
//
// Kills: the host without its port (the trial's previews are at
// …preview.localhost:7777, and an app builds its links from the Host it is
// sent); a direct published-port URL handed out as a Host (it routes to
// AppTown itself, not the preview); either variable left unexpanded.
func TestAReviewerIsToldWhereTheRunsPreviewIsAndHowToReachIt(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	ctx := context.Background()
	read := func() (string, string) {
		return Interpolate(ctx, q.db, "${{preview.url}}", run, nil, ""), Interpolate(ctx, q.db, "${{ preview.host }}", run, nil, "")
	}
	if url, host := read(); url != "No preview URL has been recorded for this run." || host != url {
		t.Fatalf("no preview yet: url %q, host %q", url, host)
	}

	at := time.Now()
	for _, c := range []struct{ recorded, host string }{
		{"https://change-01a15b12-cded-7000-8c0e-7f7cd52db9ea.preview.example.com", "change-01a15b12-cded-7000-8c0e-7f7cd52db9ea.preview.example.com"},
		{"http://run-01a15b12-cdff-7000-bd3e-05a9865c56bf.preview.localhost:7777", "run-01a15b12-cdff-7000-bd3e-05a9865c56bf.preview.localhost:7777"},
		{"http://127.0.0.1:49152", "This preview is not host routed; the console Host-header method cannot open it."},
	} {
		at = at.Add(time.Second)
		if _, err := AddArtifactIn(ctx, q.db, run.ID, "preview", "preview", c.recorded, "", at); err != nil {
			t.Fatal(err)
		}
		if url, host := read(); url != c.recorded || host != c.host {
			t.Errorf("preview %s: url %q, host %q; want host %q", c.recorded, url, host, c.host)
		}
	}
}
