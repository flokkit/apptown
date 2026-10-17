package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/complaint"
	"flokkit.io/forge/internal/ids"
)

func TestComplainFileAndList(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	box := complaint.NewBox(db, nil, nil)
	intake := NewComplaintIntake(box, nil, db)
	ctx := access.WithCaller(context.Background(), access.Caller{Receipt: "bls"})
	complain := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		intake.Complain(w, httptest.NewRequest("POST", "/api/v1/complaints", strings.NewReader(body)).WithContext(ctx))
		return w
	}
	// A complaint needs text.
	w := complain(`{"text":"  "}`)
	if w.Code != 400 {
		t.Fatalf("blank complaint must 400: %d %s", w.Code, w.Body.String())
	}
	// A good complaint files as a human source, attributed to the actor.
	w = complain(`{"text":"the docs lied","why":"lost an hour","wish":"a working example"}`)
	if w.Code != 201 {
		t.Fatalf("file: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		OK bool   `json:"ok"`
		ID ids.ID `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || !created.OK || created.ID.IsZero() {
		t.Fatalf("file response: %v %s", err, w.Body.String())
	}
	// List returns it, unseen, with the why/wish carried.
	w = httptest.NewRecorder()
	NewComplaints(box, nil, complaint.AgentInstructions, complaint.ConsoleRules).ListComplaints(w, httptest.NewRequest("GET", "/api/v1/complaints", nil).WithContext(ctx))
	if w.Code != 200 {
		t.Fatalf("list: %d", w.Code)
	}
	var listed struct {
		Complaints []struct {
			ID     ids.ID `json:"id"`
			Source string `json:"source"`
			Actor  string `json:"actor"`
			Text   string `json:"text"`
			Why    string `json:"why"`
			Wish   string `json:"wish"`
			Seen   bool   `json:"seen"`
			Answer string `json:"answer"`
		} `json:"complaints"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Complaints) != 1 {
		t.Fatalf("list size: %d", len(listed.Complaints))
	}
	c := listed.Complaints[0]
	if c.Source != "human" || c.Actor != "bls" || c.Text != "the docs lied" ||
		c.Why != "lost an hour" || c.Wish != "a working example" || c.Seen || c.Answer != "" {
		t.Fatalf("filed complaint wrong: %+v", c)
	}
}
