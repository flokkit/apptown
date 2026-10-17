package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/complaint"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestAnswerComplaint(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	box := complaint.NewBox(db, nil, nil)
	authority := identity.New(db, identity.Config{})
	if err := authority.SeedAdministrator(context.Background(), "bls"); err != nil {
		t.Fatal(err)
	}
	id, err := box.File(t.Context(), complaint.Entry{Source: "human", Actor: "kid", Text: "it broke"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewComplaints(box, authority, complaint.AgentInstructions, complaint.ConsoleRules)
	caller := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "bls", Via: "test"}, Receipt: "bls", Verified: true})
	answer := func(id ids.ID, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/v1/complaints/"+id.String()+"/answer", strings.NewReader(body)).WithContext(caller)
		r.SetPathValue("id", id.String())
		w := httptest.NewRecorder()
		handler.AnswerComplaint(w, r)
		return w
	}
	// An answer is required.
	w := answer(id, `{"answer":""}`)
	if w.Code != 400 {
		t.Fatalf("empty answer must 400: %d %s", w.Code, w.Body.String())
	}
	// Answering an unknown complaint is a 404.
	w = answer(idstest.N(9999), `{"answer":"fixed"}`)
	if w.Code != 404 {
		t.Fatalf("unknown complaint must 404: %d %s", w.Code, w.Body.String())
	}
	// A real answer lands on the record, attributed, and marks it seen.
	w = answer(id, `{"answer":"fixed in r12"}`)
	if w.Code != 200 {
		t.Fatalf("answer: %d %s", w.Code, w.Body.String())
	}
	complaints, err := box.All(caller, 10)
	if err != nil || len(complaints) != 1 {
		t.Fatal(err)
	}
	got := complaints[0]
	if got.ID != id || got.Answer != "fixed in r12" || got.AnsweredBy != "bls" || !got.Seen {
		t.Fatalf("answer not recorded: %+v", got)
	}
}
