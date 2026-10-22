package httpapi

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/complaint"
)

func TestComplaintsSeen(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	box := complaint.NewBox(db, nil, nil)
	for i := 0; i < 3; i++ {
		if _, err := box.File(t.Context(), complaint.Entry{Source: "human", Text: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	w := httptest.NewRecorder()
	NewComplaints(box, nil, complaint.AgentInstructions, complaint.ConsoleRules).ComplaintsSeen(w, httptest.NewRequest("POST", "/api/v1/complaints/seen", nil))
	if w.Code != 200 {
		t.Fatalf("seen: %d", w.Code)
	}
	all, err := box.All(context.Background(), 10)
	if err != nil || len(all) != 3 {
		t.Fatalf("complaints: %d %v", len(all), err)
	}
	unseen := 0
	for _, c := range all {
		if !c.Seen {
			unseen++
		}
	}
	if unseen != 0 {
		t.Fatalf("mark-all-seen left %d unseen", unseen)
	}
}
