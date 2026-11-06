package deployment

import (
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"strings"
	"testing"
)

// A downstream feed created after preflight is checked again under the
// settlement transaction's project lock, before retention or deletion.
func TestPromotionSourceRemovalRechecksAtSettlement(t *testing.T) {
	t.Parallel()
	w, downstream, _ := promotionFeedFixture(t)
	downstream, err := w.s.SetFeed(t.Context(), w.project, downstream.Name, FeedSettings{Revision: downstream.FeedRevision, Feed: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	var selected *Deployment
	if err := w.db.InTx(t.Context(), func(tx *database.Tx) error {
		var err error
		selected, err = SelectRemovalIn(t.Context(), tx, w.project, "staging", false)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.SetFeed(t.Context(), w.project, downstream.Name, FeedSettings{Revision: downstream.FeedRevision, Feed: "promotion", From: selected.Name}); err != nil {
		t.Fatal(err)
	}
	err = w.db.InTx(t.Context(), func(tx *database.Tx) error {
		_, err := RemoveRecordedIn(t.Context(), tx, selected, false, RemovalSettlement{At: w.s.now()})
		return err
	})
	if fault.KindOf(err) != fault.Conflict || !strings.Contains(err.Error(), "production") || !strings.Contains(err.Error(), "change their feeds first") {
		t.Fatalf("settlement refusal: %v", err)
	}
	if _, err := w.s.Deployments.ByID(t.Context(), selected.ID); err != nil {
		t.Fatal(err)
	}
}
